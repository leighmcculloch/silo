package fly

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
)

// Client implements backend.Backend using Fly.io Machines.
type Client struct {
	app    string
	region string
}

// NewClient creates a new Fly.io backend client.
func NewClient(app, region string) (*Client, error) {
	if _, err := exec.LookPath("fly"); err != nil {
		return nil, fmt.Errorf("fly CLI not found (install from https://fly.io/docs/flyctl/install/): %w", err)
	}
	if app == "" {
		app = os.Getenv("FLY_APP")
	}
	if app == "" {
		return nil, fmt.Errorf("fly backend requires a Fly app name: set backends.fly.app in silo.jsonc or FLY_APP env var (create one with: fly apps create <name>)")
	}
	if region == "" {
		region = os.Getenv("FLY_REGION")
	}
	if region == "" {
		region = "syd"
	}
	return &Client{app: app, region: region}, nil
}

// Close is a no-op.
func (c *Client) Close() error { return nil }

// FileMountsAreSymlinks reports false; files are synced directly.
func (c *Client) FileMountsAreSymlinks() bool { return false }

// NeedsMountWait reports false; files are uploaded before the tool runs.
func (c *Client) NeedsMountWait() bool { return false }

// ImageExists checks if an image tag exists in the Fly.io registry.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	// Get fly auth token
	out, err := exec.CommandContext(ctx, "fly", "auth", "token", "-q").Output()
	if err != nil {
		return false, nil // can't check, assume not exists
	}
	token := strings.TrimSpace(string(out))

	// Check OCI registry API for the manifest.
	// Fly's registry uses Basic auth (user "x", password is the fly token)
	// and serves OCI image manifests.
	url := fmt.Sprintf("https://registry.fly.io/v2/%s/manifests/%s", c.app, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, nil
	}
	req.SetBasicAuth("x", token)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, nil
	}
	resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// Build builds a container image using Fly's remote builders and pushes to the
// Fly registry. This uses `fly deploy --remote-only` which also creates a
// machine as a side-effect; we destroy it afterward.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	tmpDir, err := os.MkdirTemp("", "silo-fly-build-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(opts.Dockerfile), 0644); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	flyToml := fmt.Sprintf("app = %q\nprimary_region = %q\n", c.app, c.region)
	if err := os.WriteFile(filepath.Join(tmpDir, "fly.toml"), []byte(flyToml), 0644); err != nil {
		return "", fmt.Errorf("failed to write fly.toml: %w", err)
	}

	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}

	args := []string{"deploy",
		"--app", c.app,
		"--remote-only",
		"--ha=false",
		"--strategy", "immediate",
		"--image-label", tag,
		"--dockerfile", filepath.Join(tmpDir, "Dockerfile"),
		"--config", filepath.Join(tmpDir, "fly.toml"),
	}
	if opts.Target != "" {
		args = append(args, "--build-target", opts.Target)
	}
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}

	cmd := exec.CommandContext(ctx, "fly", args...)

	// Use a PTY to capture combined output (fly sends to both stdout and stderr)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return "", fmt.Errorf("fly deploy failed to start: %w", err)
	}
	defer ptmx.Close()

	// Forward context cancellation
	go func() {
		<-ctx.Done()
		cmd.Process.Signal(syscall.SIGTERM)
	}()

	const tailSize = 20
	var tailBuf [tailSize]string
	tailIdx := 0
	tailCount := 0

	scanner := bufio.NewScanner(ptmx)
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
			return "", fmt.Errorf("fly build failed: %w\n%s", err, detail.String())
		}
		return "", fmt.Errorf("fly build failed: %w", err)
	}

	// Clean up machines created by fly deploy (side-effect of pushing the image)
	c.cleanupDeployMachines(ctx)

	return tag, nil
}

// cleanupDeployMachines destroys machines created by fly deploy that aren't
// silo-managed machines (i.e., machines without silo metadata).
func (c *Client) cleanupDeployMachines(ctx context.Context) {
	machines, err := c.listMachines(ctx)
	if err != nil {
		return
	}
	for _, m := range machines {
		if !isSiloMachine(m) {
			c.destroyMachine(ctx, m.ID)
		}
	}
}

// Run creates a Fly machine, syncs files, and connects interactively.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	imageRef := fmt.Sprintf("registry.fly.io/%s:%s", c.app, opts.Image)

	// 1. Create machine with sleep infinity (keeps it running for sync + reconnect)
	fmt.Fprintf(os.Stderr, "  → Creating machine...\n")
	machineID, err := c.createMachine(ctx, imageRef, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  → Machine created: %s\n", machineID)

	// 2. Wait for machine to start
	fmt.Fprintf(os.Stderr, "  → Waiting for machine to start...\n")
	if err := c.waitForState(ctx, machineID, "started", 5*time.Minute); err != nil {
		c.destroyMachine(ctx, machineID)
		return err
	}

	// 3. Wait for SSH to be ready
	fmt.Fprintf(os.Stderr, "  → Waiting for SSH...\n")
	if err := c.waitForSSH(ctx, machineID, 60*time.Second); err != nil {
		c.destroyMachine(ctx, machineID)
		return err
	}

	// 4. Start file sync (mutagen: initial sync + continuous bidirectional)
	fmt.Fprintf(os.Stderr, "  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, machineID, opts.MountsRO, opts.MountsRW, opts.CleanMountPaths)
	if err != nil {
		c.destroyMachine(ctx, machineID)
		return fmt.Errorf("file sync failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  → Files synced\n")

	// 5. Run pre-run hooks (skip mount wait since files are already synced)
	hooks := filterMountWait(opts.PreRunHooks)
	if len(hooks) > 0 {
		fmt.Fprintf(os.Stderr, "  → Running pre-run hooks...\n")
		hookScript := strings.Join(hooks, " && ")
		if err := c.sshExec(ctx, machineID, hookScript); err != nil {
			stopSync()
			c.destroyMachine(ctx, machineID)
			return fmt.Errorf("pre-run hook failed: %w", err)
		}
	}

	// 6. Connect interactively with the tool command (via tmux)
	fmt.Fprintf(os.Stderr, "  → Connecting...\n")
	connectErr := c.connectInteractive(ctx, machineID, opts)

	// 7. Check if the tmux session is still running (user detached vs tool exited).
	// If the session still exists, keep the machine alive for reconnect.
	// If it doesn't, the tool exited — destroy the machine.
	tmuxAlive := c.isTmuxSessionAlive(ctx, machineID)

	// 8. Stop continuous sync (flushes final changes)
	fmt.Fprintf(os.Stderr, "  → Syncing final changes...\n")
	stopSync()

	if tmuxAlive {
		fmt.Fprintf(os.Stderr, "  → Detached. Machine %s still running — use 'silo reconnect %s --backend fly' to reattach.\n", machineID, opts.Name)
		return nil
	}

	// Tool exited, destroy the machine
	fmt.Fprintf(os.Stderr, "  → Destroying machine...\n")
	destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.destroyMachine(destroyCtx, machineID)

	return connectErr
}

// Exec runs a command inside a running Fly machine with interactive TTY.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	machineID, err := c.resolveMachine(ctx, name)
	if err != nil {
		return err
	}

	user := currentUsername()
	if user == "" {
		user = "root"
	}

	cmdStr := shellquote.Join(command...)
	return c.flySSHInteractive(ctx, machineID, user, cmdStr)
}

// Reconnect re-syncs files and reattaches to the tool's tmux session.
func (c *Client) Reconnect(ctx context.Context, name string, opts backend.RunOptions) error {
	machineID, err := c.resolveMachine(ctx, name)
	if err != nil {
		return err
	}

	// Re-sync files (don't clean mount paths — tool is already running)
	fmt.Fprintf(os.Stderr, "  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, machineID, opts.MountsRO, opts.MountsRW, nil)
	if err != nil {
		return fmt.Errorf("file sync failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  → Files synced\n")

	// Reattach to the tmux session
	fmt.Fprintf(os.Stderr, "  → Reconnecting...\n")
	user := currentUsername()
	if user == "" {
		user = "root"
	}
	connectErr := c.flySSHInteractive(ctx, machineID, user,
		"export LANG=C.UTF-8 LC_ALL=C.UTF-8; tmux -u attach-session -t silo")

	// Stop sync (flush final changes)
	fmt.Fprintf(os.Stderr, "  → Syncing final changes...\n")
	stopSync()

	return connectErr
}

// List returns all silo-created Fly machines.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	machines, err := c.listMachines(ctx)
	if err != nil {
		return nil, err
	}

	var result []backend.ContainerInfo
	for _, m := range machines {
		if !isSiloMachine(m) {
			continue
		}
		isRunning := m.State == "started"
		result = append(result, backend.ContainerInfo{
			Name:      siloNameFromMachine(m),
			Image:     m.Config.Image,
			Status:    m.State,
			IsRunning: isRunning,
		})
	}
	return result, nil
}

// Remove destroys specific machines by name.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	machines, err := c.listMachines(ctx)
	if err != nil {
		return nil, err
	}

	toRemove := make(map[string]bool)
	for _, name := range names {
		toRemove[name] = true
	}

	var removed []string
	for _, m := range machines {
		if !isSiloMachine(m) {
			continue
		}
		name := siloNameFromMachine(m)
		if !toRemove[name] {
			continue
		}
		if err := c.destroyMachine(ctx, m.ID); err != nil {
			return removed, fmt.Errorf("failed to remove machine %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// NextContainerName returns the next sequential name for the given base name.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	machines, err := c.listMachines(ctx)
	if err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := baseName + "-"
	for _, m := range machines {
		if !isSiloMachine(m) {
			continue
		}
		name := siloNameFromMachine(m)
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
			var num int
			if _, err := fmt.Sscanf(suffix, "%d", &num); err == nil && num > maxNum {
				maxNum = num
			}
		}
	}

	return fmt.Sprintf("%s-%d", baseName, maxNum+1)
}

// --- Internal helpers ---

// flyMachine represents a Fly machine from the JSON API.
type flyMachine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Region   string `json:"region"`
	ImageRef struct {
		Tag string `json:"tag"`
	} `json:"image_ref"`
	Config struct {
		Image    string            `json:"image"`
		Metadata map[string]string `json:"metadata"`
		Env      map[string]string `json:"env"`
	} `json:"config"`
}

func isSiloMachine(m flyMachine) bool {
	return m.Config.Metadata["silo"] == "true"
}

func siloNameFromMachine(m flyMachine) string {
	if name := m.Config.Metadata["silo-name"]; name != "" {
		return name
	}
	return m.Name
}

func (c *Client) listMachines(ctx context.Context) ([]flyMachine, error) {
	cmd := exec.CommandContext(ctx, "fly", "machine", "list", "--app", c.app, "--json", "-q")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list fly machines: %w", err)
	}
	var machines []flyMachine
	if err := json.Unmarshal(output, &machines); err != nil {
		return nil, fmt.Errorf("failed to parse fly machine list: %w", err)
	}
	return machines, nil
}

func (c *Client) createMachine(ctx context.Context, imageRef string, opts backend.RunOptions) (string, error) {
	args := []string{"machine", "run", imageRef,
		"--app", c.app,
		"--region", c.region,
		"--restart", "no",
	}

	// Set VM size: 4 shared CPUs + 8GB RAM (max for shared CPUs)
	args = append(args, "--vm-cpus", "4", "--vm-memory", "8192")

	// Set metadata to identify this as a silo machine
	args = append(args, "-m", "silo=true")
	args = append(args, "-m", "silo-name="+opts.Name)

	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}

	// Keep machine running
	args = append(args, "--", "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "fly", args...)
	output, err := cmd.CombinedOutput()

	// Parse machine ID from output even on error, since fly machine run may
	// return an error if its internal start timeout expires, but the machine
	// was still created and may start successfully with more time.
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if id, ok := strings.CutPrefix(line, "Machine ID: "); ok {
			return strings.TrimSpace(id), nil
		}
	}

	if err != nil {
		return "", fmt.Errorf("failed to create fly machine: %w\n%s", err, string(output))
	}

	// Fallback: look up by name
	machines, listErr := c.listMachines(ctx)
	if listErr != nil {
		return "", fmt.Errorf("machine created but couldn't find ID: %w", listErr)
	}
	for _, m := range machines {
		if m.Config.Metadata["silo-name"] == opts.Name {
			return m.ID, nil
		}
	}

	return "", fmt.Errorf("machine created but couldn't find ID in output:\n%s", string(output))
}

func (c *Client) waitForState(ctx context.Context, machineID, state string, timeout time.Duration) error {
	args := []string{"machine", "wait",
		"--app", c.app,
		"--state", state,
		"--wait-timeout", timeout.String(),
		machineID,
	}
	cmd := exec.CommandContext(ctx, "fly", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("waiting for machine %s: %w\n%s", state, err, string(output))
	}
	return nil
}

func (c *Client) waitForSSH(ctx context.Context, machineID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.CommandContext(ctx, "fly", "ssh", "console",
			"--app", c.app,
			"--machine", machineID,
			"--user", "root",
			"-q",
			"-C", "true",
		)
		if err := cmd.Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("SSH not ready after %s", timeout)
}

func (c *Client) destroyMachine(ctx context.Context, machineID string) error {
	cmd := exec.CommandContext(ctx, "fly", "machine", "destroy", machineID,
		"--app", c.app, "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to destroy machine %s: %w\n%s", machineID, err, string(output))
	}
	return nil
}

func (c *Client) resolveMachine(ctx context.Context, name string) (string, error) {
	machines, err := c.listMachines(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range machines {
		if !isSiloMachine(m) {
			continue
		}
		if siloNameFromMachine(m) == name {
			if m.State != "started" {
				return "", fmt.Errorf("machine %s is not running (state: %s)", name, m.State)
			}
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("container %s not found", name)
}

// flySshDir creates a directory containing an SSH wrapper script as "ssh",
// suitable for use as MUTAGEN_SSH_PATH (which expects a directory, not a file).
// The wrapper translates SSH invocations into `fly ssh console` commands.
func (c *Client) flySshDir(machineID string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "silo-fly-sshdir-*")
	if err != nil {
		return "", nil, err
	}

	// Use the current user (not root) so synced files have correct ownership.
	// Root access for mkdir/chown is done via separate sshExecAs calls.
	user := currentUsername()
	if user == "" {
		user = "root"
	}

	script := fmt.Sprintf(`#!/bin/sh
# SSH replacement for fly ssh console.
# Called as: ssh [-oKey=Value ...] <host> <command...>
# Skip flags and the host placeholder, run the rest on the Fly machine.
while [ $# -gt 0 ]; do
  case "$1" in
    -o*) shift ;;   # skip -oKey=Value (mutagen sends these)
    -*)  shift ;;   # skip other flags
    *)   shift; break ;;  # skip host, rest is command
  esac
done
exec fly ssh console --app %s --machine %s --user %s -q -C "$*"
`, c.app, machineID, user)

	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}

	return dir, func() { os.RemoveAll(dir) }, nil
}

// startMutagenSync starts continuous file sync using mutagen.
// RO mounts use one-way-replica (local→remote), RW mounts use two-way-safe.
// Returns a cleanup function that flushes final changes and terminates sessions.
func (c *Client) startMutagenSync(ctx context.Context, machineID string, mountsRO, mountsRW, cleanPaths []string) (cleanup func(), err error) {
	if len(mountsRO) == 0 && len(mountsRW) == 0 {
		return func() {}, nil
	}

	mutagenPath, err := exec.LookPath("mutagen")
	if err != nil {
		return nil, fmt.Errorf("mutagen is required for the fly backend (install from https://mutagen.io): %w", err)
	}

	// Create SSH dir for mutagen
	sshDir, sshCleanup, err := c.flySshDir(machineID)
	if err != nil {
		return nil, err
	}

	// Create a dedicated mutagen data directory for this session.
	// This gives us our own daemon instance with the correct SSH path,
	// without interfering with any existing mutagen daemon.
	mutagenDataDir, err := os.MkdirTemp("", "silo-mutagen-data-*")
	if err != nil {
		sshCleanup()
		return nil, err
	}

	// MUTAGEN_SSH_PATH must be set in the daemon's environment.
	// MUTAGEN_DATA_DIRECTORY isolates our daemon from any existing one.
	mutagenEnv := append(os.Environ(),
		"MUTAGEN_SSH_PATH="+sshDir,
		"MUTAGEN_DATA_DIRECTORY="+mutagenDataDir,
	)

	progress := newSyncProgress(os.Stderr)
	progress.setPhase("Starting sync daemon...")

	startCmd := exec.CommandContext(ctx, mutagenPath, "daemon", "start")
	startCmd.Env = mutagenEnv
	if err := startCmd.Run(); err != nil {
		progress.finish()
		os.RemoveAll(mutagenDataDir)
		sshCleanup()
		return nil, fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	type mount struct {
		path string
		mode string // "one-way-replica" or "two-way-safe"
	}
	var mounts []mount
	for _, p := range mountsRO {
		mounts = append(mounts, mount{path: p, mode: "one-way-replica"})
	}
	for _, p := range mountsRW {
		mounts = append(mounts, mount{path: p, mode: "two-way-safe"})
	}

	progress.setPhase("Preparing remote paths...")
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
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				p = filepath.Dir(p)
			}
			mkdirPaths = append(mkdirPaths, shellquote.Join(p))
		}
		scriptParts = append(scriptParts, fmt.Sprintf("mkdir -p %s", strings.Join(mkdirPaths, " ")))

		// Chown mount paths and .mutagen to the user so mutagen (which runs
		// as the user) can write files and execute its agent binary.
		user := currentUsername()
		if user != "" {
			var chownPaths []string
			for _, m := range mounts {
				if info, err := os.Stat(m.path); err == nil && info.IsDir() {
					chownPaths = append(chownPaths, shellquote.Join(m.path))
				}
			}
			// The mutagen agent is pre-installed in the image as root;
			// chown it so the user can execute it. Use explicit home path
			// since this script runs as root (where $HOME=/root).
			home := os.Getenv("HOME")
			if home != "" {
				chownPaths = append(chownPaths, shellquote.Join(filepath.Join(home, ".mutagen")))
			}
			scriptParts = append(scriptParts, fmt.Sprintf("chown -R %s:%s %s", user, user, strings.Join(chownPaths, " ")))
		}

		if err := c.sshExecAs(ctx, machineID, "root", strings.Join(scriptParts, " && ")); err != nil {
			progress.finish()
			os.RemoveAll(mutagenDataDir)
			sshCleanup()
			return nil, fmt.Errorf("failed to prepare remote directories: %w", err)
		}
	}

	var sessionNames []string
	cleanupSessions := func() {
		for _, name := range sessionNames {
			terminateCmd := exec.Command(mutagenPath, "sync", "terminate", name)
			terminateCmd.Env = mutagenEnv
			terminateCmd.Run()
		}
		// Stop our isolated daemon and clean up
		stopCmd := exec.Command(mutagenPath, "daemon", "stop")
		stopCmd.Env = mutagenEnv
		stopCmd.Run()
		os.RemoveAll(mutagenDataDir)
		sshCleanup()
	}

	// Resolve local paths and build session list
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

		// Resolve symlinks on the local path so mutagen can open the sync root
		localPath := m.path
		if resolved, err := filepath.EvalSymlinks(localPath); err == nil {
			localPath = resolved
		} else {
			if target, linkErr := os.Readlink(localPath); linkErr == nil {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(localPath), target)
				}
				localPath = target
			} else {
				fmt.Fprintf(os.Stderr, "    mutagen: warning: cannot resolve %s: %v\n", m.path, err)
			}
		}

		sessions = append(sessions, session{
			name:      fmt.Sprintf("silo-%s-%d", machineID, i),
			localPath: localPath,
			mount:     m,
		})
	}

	// Create all sessions in parallel
	type createResult struct {
		idx int
		err error
	}
	progress.setProgress("Creating sync sessions...", 0, len(sessions), "")
	results := make(chan createResult, len(sessions))
	for i, s := range sessions {
		sessionNames = append(sessionNames, s.name)
		go func(idx int, s session) {
			cmd := exec.CommandContext(ctx, mutagenPath, "sync", "create",
				"--name", s.name,
				"--sync-mode", s.mount.mode,
				s.localPath, "fly:"+s.mount.path,
			)
			cmd.Env = mutagenEnv
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("mutagen sync create failed for %s: %w\n%s", s.mount.path, err, string(out))
			}
			results <- createResult{idx: idx, err: err}
		}(i, s)
	}
	createdCount := 0
	for range sessions {
		r := <-results
		if r.err != nil {
			progress.finish()
			cleanupSessions()
			return nil, r.err
		}
		createdCount++
		progress.setProgress("Creating sync sessions...", createdCount, len(sessions), "")
	}

	// Flush all sessions in parallel to wait for initial sync.
	progress.setProgress("Syncing files...", 0, len(sessions), "")

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

	// Poll mutagen for detailed status while flushes run in background.
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
				detail = tildePath(sessions[r.idx].mount.path)
			}
			progress.setProgress("Syncing files...", syncedCount, len(sessions), detail)
		}
		close(flushDone)
	}()

	// Poll mutagen for detailed transfer status while waiting for flushes.
	mutagenListTemplate := `{{range .}}{{.Name}}|{{.Status}}` +
		`|{{with .BetaState}}{{with .StagingProgress}}{{.ReceivedSize}}/{{.TotalSize}}{{end}}{{end}}` +
		`{{"\n"}}{{end}}`

	pollStatus := func() {
		cmd := exec.CommandContext(ctx, mutagenPath, "sync", "list",
			"--template", mutagenListTemplate)
		cmd.Env = mutagenEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			return
		}
		// Find any session that's actively transferring or doing work
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.SplitN(line, "|", 3)
			if len(parts) < 2 {
				continue
			}
			status := strings.TrimSpace(parts[1])
			if status == "" || status == "Watching for changes" {
				continue
			}
			detail := status
			// Show transfer progress if available
			if len(parts) == 3 && parts[2] != "" {
				detail = status + " " + formatTransferSize(parts[2])
			}
			progress.setProgress("Syncing files...", syncedCount, len(sessions), detail)
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
		progress.finish()
		cleanupSessions()
		return nil, flushErr
	}

	progress.finish()

	return func() {
		// Flush final changes before terminating
		for _, name := range sessionNames {
			flushCmd := exec.Command(mutagenPath, "sync", "flush", name)
			flushCmd.Env = mutagenEnv
			flushCmd.Run()
		}
		cleanupSessions()
	}, nil
}

func (c *Client) sshExec(ctx context.Context, machineID, script string) error {
	return c.sshExecAs(ctx, machineID, "", script)
}

func (c *Client) sshExecAs(ctx context.Context, machineID, user, script string) error {
	if user == "" {
		user = currentUsername()
		if user == "" {
			user = "root"
		}
	}
	cmd := exec.CommandContext(ctx, "fly", "ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--user", user,
		"-q",
		"-C", fmt.Sprintf("bash -l -c %s", shellquote.Join(script)),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c *Client) connectInteractive(ctx context.Context, machineID string, opts backend.RunOptions) error {
	// Build the command to run inside the machine
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

	// Determine the user to connect as (matches Dockerfile USER directive)
	user := currentUsername()
	if user == "" {
		user = "root"
	}

	// Write tmux config: hide status bar (single window), enable mouse, UTF-8.
	tmuxConf := `set -g status off
set -g mouse on
set -g default-terminal "tmux-256color"
`
	_ = c.sshExec(ctx, machineID, fmt.Sprintf("printf %%s %s > /tmp/.silo-tmux.conf",
		shellquote.Join(tmuxConf)))

	// Launch the tool inside a tmux session so it survives SSH disconnects.
	// If the tmux session already exists (reconnect), attach to it.
	// Otherwise, create a new session running the tool command.
	tmuxCmd := fmt.Sprintf(
		"export LANG=C.UTF-8 LC_ALL=C.UTF-8; "+
			"if tmux has-session -t silo 2>/dev/null; then "+
			"tmux attach-session -t silo; "+
			"else "+
			"tmux -u -f /tmp/.silo-tmux.conf new-session -s silo -e LANG=C.UTF-8 -e LC_ALL=C.UTF-8 %s; "+
			"fi",
		shellquote.Join("bash", "-l", "-c", innerCmd),
	)

	return c.flySSHInteractive(ctx, machineID, user, tmuxCmd)
}

// isTmuxSessionAlive checks if the "silo" tmux session is still running on the machine.
func (c *Client) isTmuxSessionAlive(ctx context.Context, machineID string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.sshExec(checkCtx, machineID, "tmux has-session -t silo")
	return err == nil
}

// flySSHInteractive runs an interactive fly ssh session with the given command.
func (c *Client) flySSHInteractive(ctx context.Context, machineID, user, remoteCmd string) error {
	args := []string{"ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--pty",
		"--user", user,
		"-q",
		"-C", fmt.Sprintf("bash -c %s", shellquote.Join(remoteCmd)),
	}

	cmd := exec.CommandContext(ctx, "fly", args...)
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

// filterMountWait removes the mount wait hook from pre-run hooks.
// The mount wait hook is identified by its use of __silo_mount_ready.
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
// os/user.Current if the USER environment variable is not set.
func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
}

// Ensure Client implements backend.Backend at compile time.
var _ backend.Backend = (*Client)(nil)
