// Package namespace implements the silo backend interface using Namespace
// devboxes (https://namespace.so/devbox). Image building is delegated to
// `devbox image build` which builds remotely and registers the image as a
// reusable base image. Devbox creation, listing, and SSH all go through the
// `devbox` CLI. File sync uses mutagen over the SSH config that
// `devbox configure-ssh` writes to ~/.ssh/config.
package namespace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/leighmcculloch/silo/backend/internal/syncprogress"
)

// All silo-managed devbox names and image repository names use this prefix.
// Devbox does not support metadata/labels at create time, so we identify
// silo-managed resources by name prefix.
const (
	siloPrefix      = "silo-"
	imageRepoPrefix = "silo/"
)

// Client implements backend.Backend using Namespace devboxes.
type Client struct {
	size         string
	site         string
	idleTimeout  string
	volumeSizeGB int
	logw         io.Writer
}

// NewClient creates a new Namespace backend client. volumeSizeGB of 0 means
// use the devbox default persistent volume size.
func NewClient(size, site, idleTimeout string, volumeSizeGB int, logw io.Writer) (*Client, error) {
	if _, err := exec.LookPath("devbox"); err != nil {
		return nil, fmt.Errorf("devbox CLI not found (install with: curl -fsSL https://get.namespace.so/devbox/install.sh | bash): %w", err)
	}
	if size == "" {
		size = "m"
	}
	if site == "" {
		site = "sjc1"
	}
	if idleTimeout == "" {
		idleTimeout = "15m"
	}
	if logw == nil {
		logw = os.Stderr
	}
	return &Client{size: size, site: site, idleTimeout: idleTimeout, volumeSizeGB: volumeSizeGB, logw: logw}, nil
}

func (c *Client) logf(format string, args ...any) {
	if c.logw != nil {
		fmt.Fprintf(c.logw, format, args...)
	}
}

// Close is a no-op.
func (c *Client) Close() error { return nil }

// FileMountsAreSymlinks reports false; files are synced directly.
func (c *Client) FileMountsAreSymlinks() bool { return false }

// imageRepoFromTag converts a silo image tag to a devbox image repository name.
// devbox accepts repository names like "myorg/myimage"; we use "silo/<tag>"
// so all silo-built images share a common namespace inside the user's
// Namespace workspace.
func imageRepoFromTag(tag string) string {
	return imageRepoPrefix + tag
}

// devboxNameFromContainer prefixes the silo container name with `silo-` so
// silo-managed devboxes can be identified by name.
func devboxNameFromContainer(name string) string {
	if strings.HasPrefix(name, siloPrefix) {
		return name
	}
	return siloPrefix + name
}

// containerNameFromDevbox strips the silo prefix from a devbox name.
func containerNameFromDevbox(name string) string {
	return strings.TrimPrefix(name, siloPrefix)
}

// ImageExists checks whether a base image with the given silo tag is
// registered in the user's Namespace workspace.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	repo := imageRepoFromTag(name)
	cmd := exec.CommandContext(ctx, "devbox", "image", "describe", "--name", repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil
	}
	// `devbox image describe` exits 0 even when the image is missing; it
	// prints "no image found" to stdout. Treat that as not found.
	if strings.Contains(string(out), "no image found") {
		return false, nil
	}
	// A real image describe response is JSON starting with '{'. Anything
	// else (empty output, error message) is not a hit.
	trimmed := strings.TrimSpace(string(out))
	return strings.HasPrefix(trimmed, "{"), nil
}

// Build invokes `devbox image build` to build the image remotely and
// register it as a base image. The Dockerfile is written to a temp dir which
// becomes the build context.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	tmpDir, err := os.MkdirTemp("", "silo-namespace-build-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// devbox image build has no --build-arg flag, so inline build-arg defaults
	// into the Dockerfile's ARG declarations. Otherwise ARGs without defaults
	// expand to empty strings inside RUN commands.
	dockerfile := injectBuildArgDefaults(opts.Dockerfile, opts.BuildArgs)
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}
	repo := imageRepoFromTag(tag)

	// Determine the user/home that the runtime image expects. The base
	// Dockerfile creates a user matching the host user; mirror those values
	// in --user / --workspace_dir so devbox sets up the SSH session under
	// the same identity.
	username := opts.BuildArgs["USER"]
	if username == "" {
		username = currentUsername()
	}
	home := opts.BuildArgs["HOME"]
	if home == "" {
		home = "/root"
	}

	// `--optimize=true` (the default) triggers an interactive site-selection
	// prompt during build, which corrupts silo's pty. We optimize as a
	// separate step below, passing --site explicitly.
	args := []string{"image", "build", tmpDir,
		"--name", repo,
		"--user", username,
		"--workspace_dir", home,
		"--shell", "/bin/bash",
		"--optimize=false",
	}

	cmd := exec.CommandContext(ctx, "devbox", args...)

	// Use a PTY so devbox emits its progress UI (it gates rich output on TTY).
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("devbox image build failed to start: %w", err)
	}
	defer ptmx.Close()

	go func() {
		<-ctx.Done()
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()

	const tailSize = 20
	var tailBuf [tailSize]string
	tailIdx := 0
	tailCount := 0

	scanner := bufio.NewScanner(ptmx)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if opts.OnProgress != nil {
			opts.OnProgress(line + "\n")
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			tailBuf[tailIdx%tailSize] = trimmed
			tailIdx++
			tailCount++
		}
	}

	if err := cmd.Wait(); err != nil {
		var detail strings.Builder
		start := 0
		count := tailCount
		if count > tailSize {
			start = tailIdx % tailSize
			count = tailSize
		}
		for i := 0; i < count; i++ {
			detail.WriteString("  ")
			detail.WriteString(tailBuf[(start+i)%tailSize])
			detail.WriteString("\n")
		}
		if detail.Len() > 0 {
			return "", fmt.Errorf("devbox image build failed: %w\n%s", err, detail.String())
		}
		return "", fmt.Errorf("devbox image build failed: %w", err)
	}

	// Optimize the image for the target site so devbox create can boot it
	// quickly. Without this step, `devbox create` fails with "destroyed
	// while waiting for readiness". `devbox image build --optimize=true`
	// would do this implicitly, but it requires interactive site selection.
	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Optimizing image for site %s...\n", c.site))
	}
	optCmd := exec.CommandContext(ctx, "devbox", "image", "optimize",
		"--name", repo, "--site", c.site)
	if out, err := optCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("devbox image optimize (--site %s) failed: %w\n%s", c.site, err, strings.TrimSpace(string(out)))
	}

	return tag, nil
}

// Run creates a devbox, waits for it to come up, syncs files, and connects.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	if opts.NoTTY {
		return fmt.Errorf("--no-tty is not supported with the namespace backend")
	}

	devboxName := devboxNameFromContainer(opts.Name)
	imageRepo := imageRepoFromTag(opts.Image)

	// 1. Create devbox.
	c.logf("  → Creating devbox...\n")
	devboxID, err := c.createDevbox(ctx, devboxName, imageRepo)
	if err != nil {
		return err
	}
	c.logf("  → Devbox created: %s\n", devboxID)

	// 2. Wait for SSH to be ready (devbox create with --activate normally
	// returns once the box is reachable, but we poll defensively).
	c.logf("  → Waiting for SSH...\n")
	if err := c.waitForSSH(ctx, devboxName, 5*time.Minute); err != nil {
		_ = c.expireDevbox(context.Background(), devboxName)
		return err
	}

	// 3. Start file sync (mutagen: initial sync + continuous bidirectional).
	c.logf("  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, devboxName, opts.MountsRO, opts.MountsRW, opts.CleanMountPaths)
	if err != nil {
		_ = c.expireDevbox(context.Background(), devboxName)
		return fmt.Errorf("file sync failed: %w", err)
	}
	c.logf("  → Files synced\n")

	// 4. Run pre-run hooks (skip mount wait — files are already synced).
	envExports := buildEnvExports(opts.Env)
	hooks := filterMountWait(opts.PreRunHooks)
	if len(hooks) > 0 {
		c.logf("  → Running pre-run hooks...\n")
		hookScript := envExports + strings.Join(hooks, " && ")
		if err := c.sshExec(ctx, devboxName, hookScript); err != nil {
			stopSync()
			_ = c.expireDevbox(context.Background(), devboxName)
			return fmt.Errorf("pre-run hook failed: %w", err)
		}
	}

	// 5. Connect interactively with the tool command (via tmux).
	c.logf("  → Connecting...\n")
	connectErr := c.connectInteractive(ctx, devboxName, opts)

	// 6. If the tmux session is still alive the user only detached — keep
	// the devbox alive so they can reconnect. Otherwise destroy it.
	tmuxAlive := c.isTmuxSessionAlive(ctx, devboxName)

	c.logf("  → Syncing final changes...\n")
	stopSync()

	if tmuxAlive {
		c.logf("  → Detached. Devbox %s still running — use 'silo reconnect %s --backend namespace' to reattach.\n", devboxName, opts.Name)
		return nil
	}

	c.logf("  → Destroying devbox...\n")
	destroyCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = c.expireDevbox(destroyCtx, devboxName)

	return connectErr
}

// Exec runs a command inside a running devbox with interactive TTY.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	devboxName := devboxNameFromContainer(name)
	if _, err := c.findDevbox(ctx, devboxName); err != nil {
		return err
	}
	return c.devboxSSHInteractive(ctx, devboxName, shellquote.Join(command...))
}

// Reconnect re-syncs files and reattaches to the running tool's tmux session.
func (c *Client) Reconnect(ctx context.Context, name string, opts backend.RunOptions) error {
	devboxName := devboxNameFromContainer(name)
	if _, err := c.findDevbox(ctx, devboxName); err != nil {
		return err
	}

	c.logf("  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, devboxName, opts.MountsRO, opts.MountsRW, nil)
	if err != nil {
		return fmt.Errorf("file sync failed: %w", err)
	}
	c.logf("  → Files synced\n")

	c.logf("  → Reconnecting...\n")
	connectErr := c.devboxSSHInteractive(ctx, devboxName,
		"export LANG=C.UTF-8 LC_ALL=C.UTF-8; tmux -u attach-session -t silo")

	tmuxAlive := c.isTmuxSessionAlive(ctx, devboxName)

	c.logf("  → Syncing final changes...\n")
	stopSync()

	if tmuxAlive {
		c.logf("  → Detached. Devbox %s still running — use 'silo reconnect %s --backend namespace' to reattach.\n", devboxName, name)
		return nil
	}

	c.logf("  → Destroying devbox...\n")
	destroyCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = c.expireDevbox(destroyCtx, devboxName)

	return connectErr
}

// List returns all silo-managed devboxes.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	devboxes, err := c.listDevboxes(ctx)
	if err != nil {
		return nil, err
	}

	var result []backend.ContainerInfo
	for _, d := range devboxes {
		if !strings.HasPrefix(d.Name, siloPrefix) {
			continue
		}
		result = append(result, backend.ContainerInfo{
			Name:      containerNameFromDevbox(d.Name),
			Image:     d.imageDisplayName(),
			Status:    "running (" + d.Site + ")",
			IsRunning: d.IsRunning(),
		})
	}
	return result, nil
}

// Remove destroys specific devboxes by silo container name.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	devboxes, err := c.listDevboxes(ctx)
	if err != nil {
		return nil, err
	}

	wantedDevboxNames := make(map[string]string, len(names)) // devboxName -> containerName
	for _, name := range names {
		wantedDevboxNames[devboxNameFromContainer(name)] = name
	}

	var removed []string
	for _, d := range devboxes {
		if !strings.HasPrefix(d.Name, siloPrefix) {
			continue
		}
		container, ok := wantedDevboxNames[d.Name]
		if !ok {
			continue
		}
		if err := c.expireDevbox(ctx, d.Name); err != nil {
			return removed, fmt.Errorf("failed to remove devbox %s: %w", container, err)
		}
		removed = append(removed, container)
	}
	return removed, nil
}

// NextContainerName returns the next sequential name for the given base name.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	devboxes, err := c.listDevboxes(ctx)
	if err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := siloPrefix + baseName + "-"
	for _, d := range devboxes {
		if !strings.HasPrefix(d.Name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(d.Name, prefix)
		var num int
		if _, err := fmt.Sscanf(suffix, "%d", &num); err == nil && num > maxNum {
			maxNum = num
		}
	}

	return fmt.Sprintf("%s-%d", baseName, maxNum+1)
}

// --- Internal helpers ---

// devbox represents the JSON shape returned by `devbox list -o json`.
// Fields not currently consumed by silo are omitted; unknown fields are
// ignored by encoding/json so this stays forward-compatible.
type devbox struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	Site          string `json:"site"`
	ImageRef      string `json:"image_ref"`
	ResolvedImage struct {
		Name string `json:"name"`
	} `json:"resolved_image"`
}

// imageDisplayName returns a friendly image string for the silo container
// listing — the silo image tag if recognizable, otherwise the full image
// repo/ref.
func (d devbox) imageDisplayName() string {
	if name := strings.TrimPrefix(d.ResolvedImage.Name, imageRepoPrefix); name != "" && name != d.ResolvedImage.Name {
		return name
	}
	if d.ResolvedImage.Name != "" {
		return d.ResolvedImage.Name
	}
	return d.ImageRef
}

// IsRunning reports whether the devbox is in an active/running state.
// `devbox list` (without --show-all) only includes active devboxes, so
// any devbox returned by listDevboxes is considered running.
func (d devbox) IsRunning() bool { return true }

func (c *Client) listDevboxes(ctx context.Context) ([]devbox, error) {
	cmd := exec.CommandContext(ctx, "devbox", "list", "-o", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list devboxes: %w", err)
	}

	// devbox prints human-readable status lines (e.g. "No devbox available
	// yet. Try running `devbox create`.") to stdout *before* the JSON, so
	// we can't unmarshal the whole buffer. Skip any leading content
	// up to the first `[` or `{` and parse from there.
	jsonStart := -1
	for i, b := range out {
		if b == '[' || b == '{' {
			jsonStart = i
			break
		}
	}
	if jsonStart < 0 {
		return nil, nil
	}
	jsonBytes := bytes.TrimSpace(out[jsonStart:])
	if len(jsonBytes) == 0 {
		return nil, nil
	}

	if jsonBytes[0] == '[' {
		var arr []devbox
		if err := json.Unmarshal(jsonBytes, &arr); err != nil {
			return nil, fmt.Errorf("failed to parse devbox list: %w", err)
		}
		return arr, nil
	}
	var wrapper struct {
		Items    []devbox `json:"items"`
		Devboxes []devbox `json:"devboxes"`
	}
	if err := json.Unmarshal(jsonBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to parse devbox list: %w", err)
	}
	if len(wrapper.Items) > 0 {
		return wrapper.Items, nil
	}
	return wrapper.Devboxes, nil
}

func (c *Client) findDevbox(ctx context.Context, name string) (devbox, error) {
	devboxes, err := c.listDevboxes(ctx)
	if err != nil {
		return devbox{}, err
	}
	for _, d := range devboxes {
		if d.Name == name {
			if !d.IsRunning() {
				return devbox{}, fmt.Errorf("devbox %s is not running", name)
			}
			return d, nil
		}
	}
	return devbox{}, fmt.Errorf("container %s not found", containerNameFromDevbox(name))
}

func (c *Client) createDevbox(ctx context.Context, name, imageRepo string) (string, error) {
	args := []string{"create",
		"--name", name,
		"--image", imageRepo,
		"--size", c.size,
		"--persistent=false",
		"--activate=true",
		"--closest=true",
		"--auto_stop_idle_timeout=" + c.idleTimeout,
	}
	if c.volumeSizeGB > 0 {
		args = append(args, fmt.Sprintf("--volume_size_gb=%d", c.volumeSizeGB))
	}

	cmd := exec.CommandContext(ctx, "devbox", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create devbox: %w\n%s", err, string(output))
	}

	// devbox create prints the new id; rather than parse output, look it up
	// by name afterward — more robust against output format changes.
	devboxes, listErr := c.listDevboxes(ctx)
	if listErr != nil {
		return "", fmt.Errorf("devbox created but couldn't find ID: %w", listErr)
	}
	for _, d := range devboxes {
		if d.Name == name {
			if d.ID != "" {
				return d.ID, nil
			}
			return name, nil
		}
	}
	return "", fmt.Errorf("devbox created but not found in list output:\n%s", string(output))
}

func (c *Client) expireDevbox(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "devbox", "expire", name, "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("devbox expire %s: %w\n%s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c *Client) waitForSSH(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// `devbox ssh -- true` is the cheapest way to confirm the SSH
		// channel is live.
		cmd := exec.CommandContext(ctx, "devbox", "ssh", name, "-T", "--", "true")
		if err := cmd.Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("SSH not ready after %s", timeout)
}

// sshExec runs a non-interactive command via `devbox ssh`. Everything after
// `--` is joined into a single shell command line by devbox, so the script
// is wrapped in `bash -l -c '...'` as a single arg rather than passed as
// separate argv elements.
func (c *Client) sshExec(ctx context.Context, name, script string) error {
	wrapped := "bash -l -c " + shellquote.Join(script)
	cmd := exec.CommandContext(ctx, "devbox", "ssh", name, "-T", "--", wrapped)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// devboxSSHInteractive runs an interactive `devbox ssh` session.
func (c *Client) devboxSSHInteractive(ctx context.Context, name, remoteCmd string) error {
	wrapped := "bash -c " + shellquote.Join(remoteCmd)
	args := []string{"ssh", name, "-t", "--", wrapped}
	cmd := exec.CommandContext(ctx, "devbox", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				return nil // killed by signal
			}
			return fmt.Errorf("session exited with status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("session error: %w", err)
	}
	return nil
}

func (c *Client) connectInteractive(ctx context.Context, name string, opts backend.RunOptions) error {
	fullCmd := append(opts.Command, opts.Args...)
	var innerCmd string
	if len(fullCmd) > 0 {
		parts := []string{}
		if opts.WorkDir != "" {
			parts = append(parts, fmt.Sprintf("cd %s", shellquote.Join(opts.WorkDir)))
		}
		parts = append(parts, "exec "+shellquote.Join(fullCmd...))
		innerCmd = strings.Join(parts, " && ")
	}

	tmuxConf := `set -g status off
set -g mouse on
set -g default-terminal "tmux-256color"
`
	_ = c.sshExec(ctx, name, fmt.Sprintf("printf %%s %s > /tmp/.silo-tmux.conf",
		shellquote.Join(tmuxConf)))

	// Build the tmux env arguments so the tool inherits opts.Env. devbox
	// create has no --env flag, so the SSH session is the earliest place
	// these can be injected.
	var tmuxEnvArgs strings.Builder
	tmuxEnvArgs.WriteString("-e LANG=C.UTF-8 -e LC_ALL=C.UTF-8")
	for _, kv := range opts.Env {
		tmuxEnvArgs.WriteString(" -e ")
		tmuxEnvArgs.WriteString(shellquote.Join(kv))
	}

	envExports := buildEnvExports(opts.Env)
	tmuxCmd := fmt.Sprintf(
		"%sexport LANG=C.UTF-8 LC_ALL=C.UTF-8; "+
			"if tmux has-session -t silo 2>/dev/null; then "+
			"tmux attach-session -t silo; "+
			"else "+
			"tmux -u -f /tmp/.silo-tmux.conf new-session -s silo %s %s; "+
			"fi",
		envExports,
		tmuxEnvArgs.String(),
		shellquote.Join("bash", "-l", "-c", innerCmd),
	)

	return c.devboxSSHInteractive(ctx, name, tmuxCmd)
}

// buildEnvExports returns a bash snippet of `export KEY='VALUE'; ...` for
// each entry in env. Entries without `=` are skipped — those are
// pass-through requests that the silo run loop already resolved before
// reaching the backend.
func buildEnvExports(env []string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		b.WriteString("export ")
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(shellquote.Join(val))
		b.WriteString("; ")
	}
	return b.String()
}

func (c *Client) isTmuxSessionAlive(ctx context.Context, name string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.sshExec(checkCtx, name, "tmux has-session -t silo") == nil
}

// startMutagenSync starts continuous file sync using mutagen over the SSH
// config that `devbox configure-ssh` writes into the user's ~/.ssh/config.
// RO mounts use one-way-replica (local→remote), RW mounts use two-way-resolved.
func (c *Client) startMutagenSync(ctx context.Context, devboxName string, mountsRO, mountsRW, cleanPaths []string) (cleanup func(), err error) {
	if len(mountsRO) == 0 && len(mountsRW) == 0 {
		return func() {}, nil
	}

	mutagenPath, err := exec.LookPath("mutagen")
	if err != nil {
		return nil, fmt.Errorf("mutagen is required for the namespace backend (install from https://mutagen.io): %w", err)
	}

	progress := syncprogress.New(c.logw)

	// Resolve the SSH host alias devbox uses for this devbox.
	sshHost, err := c.resolveSSHHost(devboxName)
	if err != nil {
		progress.Finish()
		return nil, err
	}

	// devbox's SSH proxy takes ~7-9s to establish a fresh wireguard tunnel,
	// which is longer than mutagen's default agent connection timeout. Build
	// an SSH wrapper directory with ControlMaster so mutagen reuses a single
	// SSH connection across all of its agent operations.
	progress.SetPhase("Establishing SSH tunnel...")
	sshDir, controlPath, sshCleanup, err := buildSSHWrapper(sshHost)
	if err != nil {
		progress.Finish()
		return nil, fmt.Errorf("ssh wrapper setup: %w", err)
	}

	// Establish a backgrounded ControlMaster connection so mutagen's
	// subsequent ssh calls multiplex onto it. Without an explicit master,
	// parallel mutagen sessions race to set up the multiplex socket and
	// one of them fails before the master is ready.
	masterCmd := exec.CommandContext(ctx, "ssh",
		"-fN",
		"-o", "ControlMaster=yes",
		"-o", "ControlPath="+controlPath,
		"-o", "ControlPersist=300",
		"-o", "ConnectTimeout=60",
		sshHost)
	if out, werr := masterCmd.CombinedOutput(); werr != nil {
		progress.Finish()
		sshCleanup()
		return nil, fmt.Errorf("ssh master setup: %w: %s", werr, strings.TrimSpace(string(out)))
	}

	// Use a dedicated mutagen data directory for this session so we get our
	// own daemon instance — avoids interfering with any existing mutagen
	// daemon the user may already be running.
	mutagenDataDir, err := os.MkdirTemp("", "silo-mutagen-data-*")
	if err != nil {
		progress.Finish()
		sshCleanup()
		return nil, err
	}
	mutagenEnv := append(os.Environ(),
		"MUTAGEN_DATA_DIRECTORY="+mutagenDataDir,
		"MUTAGEN_SSH_PATH="+sshDir,
	)

	progress.SetPhase("Starting sync daemon...")
	startCmd := exec.CommandContext(ctx, mutagenPath, "daemon", "start")
	startCmd.Env = mutagenEnv
	if err := startCmd.Run(); err != nil {
		progress.Finish()
		os.RemoveAll(mutagenDataDir)
		return nil, fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	type mount struct {
		path string
		mode string
	}
	var mounts []mount
	for _, p := range mountsRO {
		mounts = append(mounts, mount{path: p, mode: "one-way-replica"})
	}
	for _, p := range mountsRW {
		mounts = append(mounts, mount{path: p, mode: "two-way-resolved"})
	}

	progress.SetPhase("Preparing remote paths...")
	{
		var scriptParts []string

		if len(cleanPaths) > 0 {
			var rmPaths []string
			for _, p := range cleanPaths {
				rmPaths = append(rmPaths, shellquote.Join(p))
			}
			scriptParts = append(scriptParts, fmt.Sprintf("rm -rf %s", strings.Join(rmPaths, " ")))
		}

		var mkdirPaths []string
		for _, m := range mounts {
			p := m.path
			if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
				p = filepath.Dir(p)
			}
			mkdirPaths = append(mkdirPaths, shellquote.Join(p))
		}
		scriptParts = append(scriptParts, fmt.Sprintf("mkdir -p %s", strings.Join(mkdirPaths, " ")))

		script := strings.Join(scriptParts, " && ")
		if err := c.sshExec(ctx, devboxName, script); err != nil {
			progress.Finish()
			os.RemoveAll(mutagenDataDir)
			return nil, fmt.Errorf("failed to prepare remote directories: %w (script: %s)", err, script)
		}
	}

	var sessionNames []string
	cleanupSessions := func() {
		for _, name := range sessionNames {
			terminateCmd := exec.Command(mutagenPath, "sync", "terminate", name)
			terminateCmd.Env = mutagenEnv
			_ = terminateCmd.Run()
		}
		stopCmd := exec.Command(mutagenPath, "daemon", "stop")
		stopCmd.Env = mutagenEnv
		_ = stopCmd.Run()
		os.RemoveAll(mutagenDataDir)
		sshCleanup()
	}

	// Resolve local paths (mutagen needs the symlink target).
	type session struct {
		name      string
		localPath string
		mount     mount
	}
	var sessions []session
	for i, m := range mounts {
		if _, err := os.Stat(m.path); err != nil {
			continue
		}

		localPath := m.path
		if resolved, err := filepath.EvalSymlinks(localPath); err == nil {
			localPath = resolved
		} else if target, linkErr := os.Readlink(localPath); linkErr == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(localPath), target)
			}
			localPath = target
		} else {
			c.logf("    mutagen: warning: cannot resolve %s: %v\n", m.path, err)
		}

		sessions = append(sessions, session{
			name:      fmt.Sprintf("silo-%s-%d", strings.ReplaceAll(devboxName, ".", "-"), i),
			localPath: localPath,
			mount:     m,
		})
	}

	// Create sessions serially. Mutagen spawns a fresh ssh process per
	// session to install/probe the agent; running these in parallel races
	// the SSH ControlMaster setup and a parallel session can fail before
	// the master socket is fully established.
	type createResult struct {
		idx int
		err error
	}
	progress.SetProgress("Creating sync sessions...", 0, len(sessions), "")
	for i, s := range sessions {
		sessionNames = append(sessionNames, s.name)
		cmd := exec.CommandContext(ctx, mutagenPath, "sync", "create",
			"--name", s.name,
			"--sync-mode", s.mount.mode,
			"--ignore-vcs",
			s.localPath, sshHost+":"+s.mount.path,
		)
		cmd.Env = mutagenEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			progress.Finish()
			cleanupSessions()
			return nil, fmt.Errorf("mutagen sync create failed for %s: %w\n%s", s.mount.path, err, string(out))
		}
		progress.SetProgress("Creating sync sessions...", i+1, len(sessions), "")
	}

	// Flush all sessions in parallel to wait for initial sync.
	progress.SetProgress("Syncing files...", 0, len(sessions), "")
	flushResults := make(chan createResult, len(sessions))
	for i, s := range sessions {
		go func(idx int, s session) {
			flushCmd := exec.CommandContext(ctx, mutagenPath, "sync", "flush", s.name)
			flushCmd.Env = mutagenEnv
			out, err := flushCmd.CombinedOutput()
			if err != nil {
				listCmd := exec.CommandContext(ctx, mutagenPath, "sync", "list", s.name)
				listCmd.Env = mutagenEnv
				listOut, _ := listCmd.CombinedOutput()
				err = fmt.Errorf("mutagen sync flush failed for %s: %w\n%s\nSession status:\n%s", s.name, err, string(out), string(listOut))
			}
			flushResults <- createResult{idx: idx, err: err}
		}(i, s)
	}

	flushDone := make(chan struct{})
	var flushErr error
	syncedCount := 0
	go func() {
		for range sessions {
			r := <-flushResults
			if r.err != nil && flushErr == nil {
				flushErr = r.err
			}
			syncedCount++
			detail := ""
			if r.idx < len(sessions) {
				detail = syncprogress.TildePath(sessions[r.idx].mount.path)
			}
			progress.SetProgress("Syncing files...", syncedCount, len(sessions), detail)
		}
		close(flushDone)
	}()

	mutagenListTemplate := `{{range .}}{{.Name}}|{{.Status}}` +
		`|{{with .BetaState}}{{with .StagingProgress}}{{.ReceivedSize}}/{{.TotalSize}}{{end}}{{end}}` +
		`{{"\n"}}{{end}}`

	pollStatus := func() {
		cmd := exec.CommandContext(ctx, mutagenPath, "sync", "list", "--template", mutagenListTemplate)
		cmd.Env = mutagenEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			return
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			parts := strings.SplitN(line, "|", 3)
			if len(parts) < 2 {
				continue
			}
			status := strings.TrimSpace(parts[1])
			if status == "" || status == "Watching for changes" {
				continue
			}
			detail := status
			if len(parts) == 3 && parts[2] != "" {
				detail = status + " " + syncprogress.FormatTransferSize(parts[2])
			}
			progress.SetProgress("Syncing files...", syncedCount, len(sessions), detail)
			return
		}
	}

	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()
pollLoop:
	for {
		select {
		case <-flushDone:
			break pollLoop
		case <-pollTicker.C:
			pollStatus()
		}
	}

	if flushErr != nil {
		progress.Finish()
		cleanupSessions()
		return nil, flushErr
	}

	progress.Finish()

	return func() {
		// Flush final changes before terminating.
		for _, name := range sessionNames {
			flushCmd := exec.Command(mutagenPath, "sync", "flush", name)
			flushCmd.Env = mutagenEnv
			_ = flushCmd.Run()
		}
		cleanupSessions()
	}, nil
}

// buildSSHWrapper writes a temporary directory containing `ssh` and `scp`
// shell scripts that invoke the system tools with ControlMaster enabled,
// so all mutagen calls reuse a single underlying SSH connection. This is
// critical because devbox's SSH ProxyCommand takes ~7-9s to set up a fresh
// wireguard tunnel, which exceeds mutagen's default agent handshake
// timeout (5s).
//
// The wrappers also `cd $HOME` before running the remote command, because
// devbox SSH sessions start in /workspaces — but mutagen invokes the
// agent via a path relative to $HOME (e.g., `.mutagen/agents/0.18.1/...`).
//
// Returns the wrapper directory (suitable for MUTAGEN_SSH_PATH) and a
// cleanup function that removes the directory and tears down the
// ControlMaster socket.
func buildSSHWrapper(sshHost string) (dir, controlPath string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "silo-namespace-sshdir-*")
	if err != nil {
		return "", "", nil, err
	}

	// Use /tmp for the control socket — Unix socket paths are limited to
	// 104 bytes on macOS, so the temp dir's longer path may not fit.
	controlPath = fmt.Sprintf("/tmp/silo-ns-ssh-%d", os.Getpid())

	commonOpts := fmt.Sprintf(
		`-o ControlMaster=auto -o ControlPath=%s -o ControlPersist=300 `+
			`-o ConnectTimeout=60 -o ServerAliveInterval=15 -o ServerAliveCountMax=3 `+
			`-o LogLevel=ERROR`,
		controlPath,
	)

	// SSH wrapper: strip mutagen's options and substitute our own. Hard-code
	// the host so mutagen's host placeholder is ignored. Prepend `cd && `
	// so mutagen's relative agent paths (e.g. `.mutagen/agents/...`)
	// resolve relative to $HOME — devbox SSH sessions start in /workspaces.
	//
	// Mutagen invokes ssh as `ssh [opts] <host> <full-remote-shell-line>`,
	// passing the remote command as ONE arg already shell-formatted. So
	// we don't re-quote — just prefix with `cd && `.
	sshScript := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift; shift ;;
    -o*) shift ;;
    -*) shift ;;
    *) shift; break ;;
  esac
done
if [ $# -eq 0 ]; then
  exec ssh %s %s
fi
exec ssh %s %s "cd && $*"
`, commonOpts, shellquote.Join(sshHost), commonOpts, shellquote.Join(sshHost))

	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(sshScript), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", "", nil, err
	}

	// SCP wrapper: strip mutagen's options and substitute our own. Walk
	// the remaining args in order: locals come first (one or more), then
	// the destination (last). Rewrite the destination to use our
	// pre-resolved host alias so it routes through the same ControlMaster
	// as ssh (avoids the slow proxy on second connection).
	scpScript := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift; shift ;;
    -o*) shift ;;
    -*) shift ;;
    *) break ;;
  esac
done
# Last arg is destination; rewrite its host part. Everything before is
# forwarded to scp verbatim. eval is used because positional args need
# regrouping; we use a sentinel to find the last index.
n=$#
i=1
locals=""
dest=""
for a in "$@"; do
  if [ $i -lt $n ]; then
    locals="$locals \"$a\""
  else
    dest=$a
  fi
  i=$((i+1))
done
case "$dest" in
  *:*) dest_path=${dest#*:} ;;
  *)   dest_path=$dest ;;
esac
# devbox SSH sessions start in /workspaces; mutagen relative dest paths
# would land there, but the subsequent install command runs from HOME
# because our ssh wrapper changes directory. Prefix relative dests with
# tilde-slash so the remote SFTP server places them in HOME.
case "$dest_path" in
  /*) ;;
  *) dest_path="~/$dest_path" ;;
esac
# -O forces the legacy SCP protocol; without it, scp negotiates SFTP
# over SSH which exits non-zero on success here (ownership ends up as
# root vs the user, and mutagen fails the install).
eval exec scp -O %s $locals %s:"'$dest_path'"
`, commonOpts, shellquote.Join(sshHost))

	if err := os.WriteFile(filepath.Join(dir, "scp"), []byte(scpScript), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", "", nil, err
	}

	return dir, controlPath, func() {
		_ = exec.Command("ssh", "-O", "exit", "-o", "ControlPath="+controlPath, sshHost).Run()
		_ = os.Remove(controlPath)
		os.RemoveAll(dir)
	}, nil
}

// resolveSSHHost finds the host alias that `devbox configure-ssh` set up
// for the given devbox name. devbox writes per-devbox SSH config files
// into ~/.namespace/ssh/ and adds an Include line to ~/.ssh/config. The
// alias is `<name>.devbox.namespace`. If we can't find one, the bare
// devbox name is returned as a best-effort fallback so the failure
// surfaces inside mutagen's own "host not found" rather than an opaque
// silo error.
func (c *Client) resolveSSHHost(devboxName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return devboxName, nil
	}

	// Primary location: devbox writes one file per devbox here.
	candidates := []string{}
	if entries, err := os.ReadDir(filepath.Join(home, ".namespace", "ssh")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".ssh") {
				candidates = append(candidates, filepath.Join(home, ".namespace", "ssh", e.Name()))
			}
		}
	}
	// Fallbacks for users who pointed the Include elsewhere.
	candidates = append(candidates, filepath.Join(home, ".ssh", "config"))
	if entries, err := os.ReadDir(filepath.Join(home, ".ssh", "config.d")); err == nil {
		for _, e := range entries {
			candidates = append(candidates, filepath.Join(home, ".ssh", "config.d", e.Name()))
		}
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(strings.ToLower(line), "host ") {
				continue
			}
			fields := strings.Fields(line)[1:]
			for _, alias := range fields {
				if alias == devboxName || strings.HasPrefix(alias, devboxName+".") {
					return alias, nil
				}
			}
		}
	}

	return devboxName, nil
}

// injectBuildArgDefaults rewrites bare `ARG NAME` lines to `ARG NAME=VALUE`
// when a value is provided in args. devbox image build has no
// --build-arg flag so silo build args must be baked into the Dockerfile.
// Lines that already specify a default are left alone.
func injectBuildArgDefaults(dockerfile string, args map[string]string) string {
	if len(args) == 0 {
		return dockerfile
	}
	var out strings.Builder
	out.Grow(len(dockerfile) + 64)
	for line := range strings.SplitSeq(dockerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ARG ") && !strings.Contains(trimmed, "=") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "ARG "))
			if val, ok := args[name]; ok {
				// Preserve original indentation by copying everything up to ARG.
				prefix, _, _ := strings.Cut(line, "ARG ")
				out.WriteString(prefix)
				out.WriteString("ARG ")
				out.WriteString(name)
				out.WriteByte('=')
				out.WriteString(val)
				out.WriteByte('\n')
				continue
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	// SplitSeq + adding "\n" each iteration appends a trailing newline that
	// the source may not have had — trim back to match the original ending.
	result := out.String()
	if !strings.HasSuffix(dockerfile, "\n") {
		result = strings.TrimSuffix(result, "\n")
	}
	return result
}

// filterMountWait removes the mount wait hook from pre-run hooks. The mount
// wait hook is identified by its use of __silo_mount_ready and is unnecessary
// when files are pre-synced before the tool starts.
func filterMountWait(hooks []string) []string {
	var result []string
	for _, h := range hooks {
		if strings.Contains(h, "__silo_mount_ready") {
			continue
		}
		result = append(result, h)
	}
	return result
}

// currentUsername returns the current user's username, falling back to
// os/user.Current if USER is unset.
func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
}

var _ backend.Backend = (*Client)(nil)
