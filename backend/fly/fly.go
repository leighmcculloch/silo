package fly

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/leighmcculloch/silo/config"
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

// ImageExists checks if an image tag has been previously built and pushed.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	stateFile := filepath.Join(config.XDGStateHomeDir(), "silo", "fly-images", name)
	_, err := os.Stat(stateFile)
	return err == nil, nil
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

	// Record that this image has been built
	saveImageBuilt(tag)

	// Clean up machines created by fly deploy (side-effect)
	c.cleanupDeployMachines(ctx)

	return tag, nil
}

// Run creates a Fly machine, syncs files, and connects interactively.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	imageRef := fmt.Sprintf("registry.fly.io/%s:%s", c.app, opts.Image)

	// 1. Create machine with sleep infinity (keeps it running for sync + reconnect)
	machineID, err := c.createMachine(ctx, imageRef, opts)
	if err != nil {
		return err
	}

	// Always destroy on exit
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c.destroyMachine(destroyCtx, machineID)
	}()

	// 2. Wait for machine to start
	if err := c.waitForState(ctx, machineID, "started", 60*time.Second); err != nil {
		return err
	}

	// 3. Start file sync (mutagen: initial sync + continuous bidirectional)
	stopSync, err := c.startMutagenSync(ctx, machineID, opts.MountsRO, opts.MountsRW)
	if err != nil {
		return fmt.Errorf("file sync failed: %w", err)
	}

	// 4. Run pre-run hooks (skip mount wait since files are already synced)
	hooks := filterMountWait(opts.PreRunHooks)
	if len(hooks) > 0 {
		hookScript := strings.Join(hooks, " && ")
		if err := c.sshExec(ctx, machineID, hookScript); err != nil {
			stopSync()
			return fmt.Errorf("pre-run hook failed: %w", err)
		}
	}

	// 5. Connect interactively with the tool command
	connectErr := c.connectInteractive(ctx, machineID, opts)

	// 6. Stop continuous sync (flushes final changes)
	stopSync()

	return connectErr
}

// Exec runs a command inside a running Fly machine with interactive TTY.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	machineID, err := c.resolveMachine(ctx, name)
	if err != nil {
		return err
	}

	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}

	cmdStr := shellquote.Join(command...)
	args := []string{"ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--pty",
		"--user", user,
		"-C", cmdStr,
		"-q",
	}

	cmd := exec.Command("fly", args...)

	// Use PTY for terminal handling
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer ptmx.Close()

	return c.handleInteractiveSession(ctx, cmd, ptmx)
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
	if err != nil {
		return "", fmt.Errorf("failed to create fly machine: %w\n%s", err, string(output))
	}

	// Parse machine ID from output. fly machine run outputs:
	//   " Machine ID: 819525a99ed768"
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if id, ok := strings.CutPrefix(line, "Machine ID: "); ok {
			return strings.TrimSpace(id), nil
		}
	}

	// Fallback: look up by name
	machines, err := c.listMachines(ctx)
	if err != nil {
		return "", fmt.Errorf("machine created but couldn't find ID: %w", err)
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
exec fly ssh console --app %s --machine %s --user root -q -C "$*"
`, c.app, machineID)

	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}

	return dir, func() { os.RemoveAll(dir) }, nil
}

// installMutagenAgent uploads the mutagen agent binary to the remote machine.
// Mutagen expects the agent at ~/.mutagen/agents/<version>/mutagen-agent.
func (c *Client) installMutagenAgent(ctx context.Context, machineID string) error {
	mutagenPath, err := exec.LookPath("mutagen")
	if err != nil {
		return fmt.Errorf("mutagen not found: %w", err)
	}

	// Get mutagen version
	out, err := exec.CommandContext(ctx, mutagenPath, "version").Output()
	if err != nil {
		return fmt.Errorf("failed to get mutagen version: %w", err)
	}
	version := strings.TrimSpace(string(out))

	// Check if agent is already installed on remote
	agentPath := fmt.Sprintf(".mutagen/agents/%s/mutagen-agent", version)
	checkCmd := fmt.Sprintf("test -x %s", shellquote.Join(agentPath))
	if err := c.sshExec(ctx, machineID, checkCmd); err == nil {
		return nil // already installed
	}

	// Find the agents archive next to the mutagen binary
	mutagenDir := filepath.Dir(mutagenPath)
	agentsArchive := filepath.Join(mutagenDir, "mutagen-agents.tar.gz")
	if _, err := os.Stat(agentsArchive); err != nil {
		return fmt.Errorf("mutagen-agents.tar.gz not found at %s: %w", agentsArchive, err)
	}

	// Extract the linux_amd64 agent (Fly machines are always linux/amd64)
	tmpDir, err := os.MkdirTemp("", "silo-mutagen-agent-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	extractCmd := exec.CommandContext(ctx, "tar", "xzf", agentsArchive, "-C", tmpDir, "linux_amd64")
	if err := extractCmd.Run(); err != nil {
		return fmt.Errorf("failed to extract mutagen agent: %w", err)
	}

	agentBinary, err := os.ReadFile(filepath.Join(tmpDir, "linux_amd64"))
	if err != nil {
		return fmt.Errorf("failed to read agent binary: %w", err)
	}

	// Upload agent binary to remote
	installScript := fmt.Sprintf("mkdir -p .mutagen/agents/%s && cat > %s && chmod +x %s",
		version, shellquote.Join(agentPath), shellquote.Join(agentPath))
	cmd := exec.CommandContext(ctx, "fly", "ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--user", "root",
		"-q",
		"-C", fmt.Sprintf("bash -c %s", shellquote.Join(installScript)),
	)
	cmd.Stdin = strings.NewReader(string(agentBinary))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to install mutagen agent: %w\n%s", err, string(out))
	}

	return nil
}

// startMutagenSync starts continuous file sync using mutagen.
// RO mounts use one-way-replica (local→remote), RW mounts use two-way-safe.
// Returns a cleanup function that flushes final changes and terminates sessions.
func (c *Client) startMutagenSync(ctx context.Context, machineID string, mountsRO, mountsRW []string) (cleanup func(), err error) {
	if len(mountsRO) == 0 && len(mountsRW) == 0 {
		return func() {}, nil
	}

	mutagenPath, err := exec.LookPath("mutagen")
	if err != nil {
		return nil, fmt.Errorf("mutagen is required for the fly backend (install from https://mutagen.io): %w", err)
	}

	// Install mutagen agent on remote
	if err := c.installMutagenAgent(ctx, machineID); err != nil {
		return nil, fmt.Errorf("failed to install mutagen agent on remote: %w", err)
	}

	// Create SSH dir for mutagen
	sshDir, sshCleanup, err := c.flySshDir(machineID)
	if err != nil {
		return nil, err
	}

	// Ensure mutagen daemon is running
	exec.CommandContext(ctx, mutagenPath, "daemon", "start").Run()

	// Env for mutagen commands
	mutagenEnv := append(os.Environ(), "MUTAGEN_SSH_PATH="+sshDir)

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

	var sessionNames []string
	cleanupSessions := func() {
		for _, name := range sessionNames {
			terminateCmd := exec.Command(mutagenPath, "sync", "terminate", name)
			terminateCmd.Env = mutagenEnv
			terminateCmd.Run()
		}
		sshCleanup()
	}

	for i, m := range mounts {
		if _, err := os.Stat(m.path); err != nil {
			continue
		}

		sessionName := fmt.Sprintf("silo-%s-%d", machineID, i)
		cmd := exec.CommandContext(ctx, mutagenPath, "sync", "create",
			"--name", sessionName,
			"--sync-mode", m.mode,
			m.path, "fly:"+m.path,
		)
		cmd.Env = mutagenEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			cleanupSessions()
			return nil, fmt.Errorf("mutagen sync create failed for %s: %w\n%s", m.path, err, string(out))
		}
		sessionNames = append(sessionNames, sessionName)
	}

	// Flush to ensure initial sync completes
	for _, name := range sessionNames {
		flushCmd := exec.CommandContext(ctx, mutagenPath, "sync", "flush", name)
		flushCmd.Env = mutagenEnv
		flushCmd.Run()
	}

	// Fix ownership on remote (mutagen runs as root via fly ssh)
	user := os.Getenv("USER")
	if user != "" {
		for _, m := range mounts {
			c.sshExec(ctx, machineID, fmt.Sprintf("chown -R %s:%s %s", user, user, shellquote.Join(m.path)))
		}
	}

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
	cmd := exec.CommandContext(ctx, "fly", "ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--user", "root",
		"-q",
		"-C", fmt.Sprintf("bash -c %s", shellquote.Join(script)),
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
	var cmdStr string
	if len(fullCmd) > 0 {
		parts := []string{}
		if opts.WorkDir != "" {
			parts = append(parts, fmt.Sprintf("cd %s", shellquote.Join(opts.WorkDir)))
		}
		parts = append(parts, "exec "+shellquote.Join(fullCmd...))
		cmdStr = strings.Join(parts, " && ")
	}

	// Determine the user to connect as (matches Dockerfile USER directive)
	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}

	args := []string{"ssh", "console",
		"--app", c.app,
		"--machine", machineID,
		"--pty",
		"--user", user,
		"-q",
	}
	if cmdStr != "" {
		args = append(args, "-C", fmt.Sprintf("bash -c %s", shellquote.Join(cmdStr)))
	}

	cmd := exec.Command("fly", args...)

	// Use PTY for proper terminal handling
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to connect to machine: %w", err)
	}
	defer ptmx.Close()

	return c.handleInteractiveSession(ctx, cmd, ptmx)
}

func (c *Client) handleInteractiveSession(ctx context.Context, cmd *exec.Cmd, ptmx *os.File) error {
	// Handle terminal resize
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize
	defer signal.Stop(ch)

	// Save and set raw terminal mode
	fd := int(os.Stdin.Fd())
	oldState, _ := getTermios(fd)
	if oldState != nil {
		setRawTermios(fd, oldState)
	}
	defer func() {
		if oldState != nil {
			restoreTermios(fd, oldState)
		}
		// Reset terminal modes
		os.Stdout.WriteString("\x1b[?1000l") // Disable mouse click tracking
		os.Stdout.WriteString("\x1b[?1002l") // Disable mouse button tracking
		os.Stdout.WriteString("\x1b[?1003l") // Disable all mouse tracking
		os.Stdout.WriteString("\x1b[?1006l") // Disable SGR mouse mode
		os.Stdout.WriteString("\x1b[?25h")   // Show cursor
	}()

	// Forward signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			cmd.Process.Kill()
		case <-ctx.Done():
			cmd.Process.Kill()
		}
	}()

	// Copy output to stdout
	go func() {
		io.Copy(os.Stdout, ptmx)
	}()

	// Copy stdin to PTY with triple Ctrl-C detection
	go func() {
		var firstCtrlC time.Time
		var ctrlCCount int
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for i := 0; i < n; i++ {
					if buf[i] == 0x03 {
						now := time.Now()
						if ctrlCCount > 0 && now.Sub(firstCtrlC) < time.Second {
							ctrlCCount++
							if ctrlCCount >= 3 {
								cmd.Process.Kill()
								return
							}
						} else {
							firstCtrlC = now
							ctrlCCount = 1
						}
					}
				}
				ptmx.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	waitErr := cmd.Wait()
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			if exitErr.ExitCode() == -1 {
				return nil // killed by signal
			}
			return fmt.Errorf("session exited with status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("session error: %w", waitErr)
	}
	return nil
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

func saveImageBuilt(tag string) {
	dir := filepath.Join(config.XDGStateHomeDir(), "silo", "fly-images")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, tag), []byte(tag), 0o644)
}

// Ensure Client implements backend.Backend at compile time.
var _ backend.Backend = (*Client)(nil)
