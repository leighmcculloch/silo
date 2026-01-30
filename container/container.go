//go:build darwin

package container

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/adrg/xdg"
	"github.com/creack/pty"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
)

// resourceArgs returns CLI flags for --cpus (all CPUs) and --memory (half system RAM).
func resourceArgs() []string {
	cpus := runtime.NumCPU()
	var memMB uint64
	if memBytes, err := unix.SysctlUint64("hw.memsize"); err == nil {
		memMB = memBytes / 2 / (1024 * 1024) // half, in MiB
	}
	args := []string{"-c", fmt.Sprintf("%d", cpus)}
	if memMB > 0 {
		args = append(args, "-m", fmt.Sprintf("%dM", memMB))
	}
	return args
}

// Client implements backend.Backend using the Apple container CLI.
type Client struct{}

// NewClient creates a new container CLI client.
func NewClient() (*Client, error) {
	if _, err := exec.LookPath("container"); err != nil {
		return nil, fmt.Errorf("container command not found (install with: brew install container): %w", err)
	}
	return &Client{}, nil
}

// Close is a no-op for the CLI backend.
func (c *Client) Close() error {
	return nil
}

// ImageExists returns true if an image with the given name exists locally.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "container", "image", "inspect", name)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}

// Build builds a container image using the container CLI.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	// Write Dockerfile to a temp dir as the build context
	tmpDir, err := os.MkdirTemp("", "silo-build-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(opts.Dockerfile), 0644); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}

	args := []string{"build",
		"-f", dockerfilePath,
		"-t", tag,
		"--progress", "plain",
	}
	args = append(args, resourceArgs()...)

	if opts.Target != "" {
		args = append(args, "--target", opts.Target)
	}

	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}

	args = append(args, tmpDir)

	cmd := exec.Command("container", args...)
	cmd.Stderr = os.Stderr

	// Stream stdout for progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start build: %w", err)
	}

	// Forward context cancellation as SIGTERM so intermediate build
	// containers are cleaned up gracefully.
	go func() {
		<-ctx.Done()
		cmd.Process.Signal(syscall.SIGTERM)
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if opts.OnProgress != nil {
			opts.OnProgress(scanner.Text() + "\n")
		}
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	return tag, nil
}

// dockerStartHook is a pre-run hook that starts the Docker daemon in the VM.
// It checks if Docker is already running and starts it if not.
const dockerStartHook = `if [ ! -S /var/run/docker.sock ]; then sudo dockerd --iptables=false > /tmp/dockerd.log 2>&1 & fi`

// generateMountWaitScript generates a bash script that waits for all mount paths to exist.
// It polls each path at 0.01s intervals for up to 10s total timeout, with logging.
func generateMountWaitScript(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var quotedPaths []string
	for _, p := range paths {
		quotedPaths = append(quotedPaths, shellquote.Join(p))
	}
	return fmt.Sprintf(`__silo_wait_for_mount() {
  local p=$1 timeout=10000 i=0
  if [ -e "$p" ]; then
    echo "silo: mount ready: $p" >&2
    return 0
  fi
  echo "silo: mount not yet ready, waiting: $p" >&2
  while [ ! -e "$p" ] && [ $i -lt $timeout ]; do
    sleep 0.001
    i=$((i+1))
  done
  if [ -e "$p" ]; then
    echo "silo: mount ready after ${i}ms: $p" >&2
    return 0
  fi
  echo "silo: mount not ready after 10s: $p" >&2
  return 1
}
__silo_wait_for_mounts() {
  local paths=(%s)
  local pids=() p
  echo "silo: waiting for ${#paths[@]} mount(s) in parallel..." >&2
  for p in "${paths[@]}"; do
    __silo_wait_for_mount "$p" &
    pids+=($!)
  done
  local failed=0
  for pid in "${pids[@]}"; do
    wait $pid || failed=1
  done
  if [ $failed -eq 1 ]; then
    exit 1
  fi
  echo "silo: all mounts ready" >&2
}; __silo_wait_for_mounts`, strings.Join(quotedPaths, " "))
}

// Run runs a container using the container CLI.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	// Prepend Docker daemon startup hook for VM environments
	opts.PreRunHooks = append([]string{dockerStartHook}, opts.PreRunHooks...)

	// Build full command: Command + Args
	fullCmd := append(opts.Command, opts.Args...)

	// Determine the entrypoint override and arguments
	var entrypoint string
	var runArgs []string

	if len(fullCmd) > 0 {
		if len(opts.PreRunHooks) > 0 {
			// Create a bash script that runs pre-run hooks then execs the command
			var script strings.Builder
			for _, hook := range opts.PreRunHooks {
				script.WriteString(hook)
				script.WriteString(" && ")
			}
			script.WriteString("exec ")
			script.WriteString(shellquote.Join(fullCmd...))
			entrypoint = "/bin/bash"
			runArgs = []string{"-c", script.String()}
		} else {
			entrypoint = fullCmd[0]
			if len(fullCmd) > 1 {
				runArgs = fullCmd[1:]
			}
		}
	}

	args := []string{"run",
		"--rm",
		"-i",
		"-t",
	}
	args = append(args, resourceArgs()...)

	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}

	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}

	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}

	// Mounts — Apple's container CLI only supports directories, so file
	// mounts are staged into a directory and symlinked inside the container.
	var symlinkCmds []string
	var mountPaths []string // Paths to wait for before running hooks
	for _, m := range opts.MountsRO {
		// Check if path exists (use Lstat to not follow symlinks for existence check)
		if _, err := os.Lstat(m); err != nil {
			continue
		}
		// Get info following symlinks to check if target is a directory
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.IsDir() {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", m, m))
			mountPaths = append(mountPaths, m)
		} else {
			stagingDir, containerDir, err := stageFileMount(m)
			if err != nil {
				return fmt.Errorf("staging file mount %s: %w", m, err)
			}
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", stagingDir, containerDir))
			mountPaths = append(mountPaths, containerDir)
			symlinkCmds = append(symlinkCmds, fmt.Sprintf("mkdir -p %s && ln -sf %s %s",
				shellquote.Join(filepath.Dir(m)),
				shellquote.Join(filepath.Join(containerDir, filepath.Base(m))),
				shellquote.Join(m),
			))
		}
	}
	for _, m := range opts.MountsRW {
		// Check if path exists (use Lstat to not follow symlinks for existence check)
		if _, err := os.Lstat(m); err != nil {
			continue
		}
		// Get info following symlinks to check if target is a directory
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.IsDir() {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", m, m))
			mountPaths = append(mountPaths, m)
		} else {
			stagingDir, containerDir, err := stageFileMount(m)
			if err != nil {
				return fmt.Errorf("staging file mount %s: %w", m, err)
			}
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", stagingDir, containerDir))
			mountPaths = append(mountPaths, containerDir)
			symlinkCmds = append(symlinkCmds, fmt.Sprintf("mkdir -p %s && ln -sf %s %s",
				shellquote.Join(filepath.Dir(m)),
				shellquote.Join(filepath.Join(containerDir, filepath.Base(m))),
				shellquote.Join(m),
			))
		}
	}

	// Generate mount wait hook if we have any mounts
	mountWaitHook := generateMountWaitScript(mountPaths)

	// Build the complete list of pre-run hooks in order:
	// 1. Mount wait hook (waits for mounts to be ready)
	// 2. Symlink commands (creates symlinks for file mounts)
	// 3. Docker daemon hook and user pre-run hooks (already in opts.PreRunHooks)
	var allPreRunHooks []string
	if mountWaitHook != "" {
		allPreRunHooks = append(allPreRunHooks, mountWaitHook)
	}
	allPreRunHooks = append(allPreRunHooks, symlinkCmds...)
	allPreRunHooks = append(allPreRunHooks, opts.PreRunHooks...)

	// Rebuild entrypoint to include all hooks if we have any
	if len(allPreRunHooks) > 0 && len(fullCmd) > 0 {
		var script strings.Builder
		for _, hook := range allPreRunHooks {
			script.WriteString(hook)
			script.WriteString(" && ")
		}
		script.WriteString("exec ")
		script.WriteString(shellquote.Join(fullCmd...))
		entrypoint = "/bin/bash"
		runArgs = []string{"-c", script.String()}
	} else if len(allPreRunHooks) > 0 {
		// No command — just run the hooks.
		var script strings.Builder
		for i, hook := range allPreRunHooks {
			if i > 0 {
				script.WriteString(" && ")
			}
			script.WriteString(hook)
		}
		entrypoint = "/bin/bash"
		runArgs = []string{"-c", script.String()}
	}

	if entrypoint != "" {
		args = append(args, "--entrypoint", entrypoint)
	}

	// Image
	args = append(args, opts.Image)

	// Command arguments
	args = append(args, runArgs...)

	cmd := exec.Command("container", args...)

	// Save terminal state and ensure it's restored on exit
	fd := int(os.Stdin.Fd())
	oldState, _ := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	defer func() {
		// Restore termios state
		if oldState != nil {
			unix.IoctlSetTermios(fd, unix.TIOCSETA, oldState)
		}
		// Reset terminal modes (mouse tracking, alternate screen, etc.)
		// These are escape sequences not covered by termios
		os.Stdout.WriteString("\x1b[?1000l") // Disable mouse click tracking
		os.Stdout.WriteString("\x1b[?1002l") // Disable mouse button tracking
		os.Stdout.WriteString("\x1b[?1003l") // Disable all mouse tracking
		os.Stdout.WriteString("\x1b[?1006l") // Disable SGR mouse mode
		os.Stdout.WriteString("\x1b[?25h")   // Show cursor
		os.Stdout.WriteString("\x1b[?1049l") // Exit alternate screen buffer
	}()

	// Start command with PTY so container gets a real terminal
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	defer ptmx.Close()

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

	// Put our terminal in raw mode
	if oldState != nil {
		newState := *oldState
		newState.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
		newState.Iflag &^= unix.IXON | unix.ICRNL
		unix.IoctlSetTermios(fd, unix.TIOCSETA, &newState)
	}

	// On signal or context cancellation, force-remove the container
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		select {
		case <-sigCh:
		case <-ctx.Done():
		}
		if opts.Name != "" {
			exec.Command("container", "rm", "-f", opts.Name).Run()
		}
	}()

	// Copy container output to stdout
	go func() {
		io.Copy(os.Stdout, ptmx)
	}()

	// Copy stdin to container, intercepting double Ctrl-C to kill
	var lastCtrlC time.Time
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			// Check for Ctrl-C (0x03)
			for i := 0; i < n; i++ {
				if buf[i] == 0x03 {
					now := time.Now()
					if now.Sub(lastCtrlC) < time.Second {
						// Double Ctrl-C - kill container
						if opts.Name != "" {
							exec.Command("container", "rm", "-f", opts.Name).Run()
						}
						return nil
					}
					lastCtrlC = now
				}
			}
			ptmx.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("container exited with status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("container error: %w", waitErr)
	}

	return nil
}

// NextContainerName returns the next sequential container name for the given
// base name. It lists existing containers with the same prefix and returns
// baseName-N where N is one more than the highest existing suffix.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	// List all containers (running and stopped)
	cmd := exec.CommandContext(ctx, "container", "ls", "-a", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	var containers []struct {
		Configuration struct {
			ID string `json:"id"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(output, &containers); err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := baseName + "-"
	for _, ctr := range containers {
		if suffix, ok := strings.CutPrefix(ctr.Configuration.ID, prefix); ok {
			var num int
			if _, err := fmt.Sscanf(suffix, "%d", &num); err == nil {
				if num > maxNum {
					maxNum = num
				}
			}
		}
	}

	return fmt.Sprintf("%s-%d", baseName, maxNum+1)
}

// List returns all silo-created containers (those with silo- image prefix)
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	cmd := exec.CommandContext(ctx, "container", "ls", "-a", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var containers []struct {
		Configuration struct {
			ID    string `json:"id"`
			Image struct {
				Reference string `json:"reference"`
			} `json:"image"`
		} `json:"configuration"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &containers); err != nil {
		return nil, fmt.Errorf("failed to parse container list: %w", err)
	}

	var result []backend.ContainerInfo
	for _, ctr := range containers {
		// Check if it's a silo container by image name prefix
		if strings.HasPrefix(ctr.Configuration.Image.Reference, "silo-") {
			result = append(result, backend.ContainerInfo{
				Name:   ctr.Configuration.ID,
				Image:  ctr.Configuration.Image.Reference,
				Status: ctr.Status,
			})
		}
	}

	return result, nil
}

// Remove removes specific containers by name
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "container", "ls", "-a", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var containers []struct {
		Configuration struct {
			ID    string `json:"id"`
			Image struct {
				Reference string `json:"reference"`
			} `json:"image"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(output, &containers); err != nil {
		return nil, fmt.Errorf("failed to parse container list: %w", err)
	}

	// Build a set of names to remove
	toRemove := make(map[string]bool)
	for _, name := range names {
		toRemove[name] = true
	}

	var removed []string
	for _, ctr := range containers {
		// Check if it's a silo container by image name prefix
		if !strings.HasPrefix(ctr.Configuration.Image.Reference, "silo-") {
			continue
		}

		if !toRemove[ctr.Configuration.ID] {
			continue
		}

		rmCmd := exec.CommandContext(ctx, "container", "rm", "-f", ctr.Configuration.ID)
		if err := rmCmd.Run(); err != nil {
			return removed, fmt.Errorf("failed to remove container %s: %w", ctr.Configuration.ID, err)
		}
		removed = append(removed, ctr.Configuration.ID)
	}

	return removed, nil
}

// stageFileMount creates a staging directory containing a hard link to the
// given file. It returns the host staging directory path and the corresponding
// container-side mount target path.
func stageFileMount(filePath string) (hostDir, containerDir string, err error) {
	h := sha256.Sum256([]byte(filePath))
	hash := hex.EncodeToString(h[:])
	hostDir = filepath.Join(xdg.StateHome, "silo", "mounts", hash)
	containerDir = filepath.Join("/silo/mounts", hash)
	if err := os.MkdirAll(hostDir, 0755); err != nil {
		return "", "", err
	}
	linkPath := filepath.Join(hostDir, filepath.Base(filePath))
	// Remove any existing link before creating a new one.
	os.Remove(linkPath)
	if err := os.Link(filePath, linkPath); err != nil {
		return "", "", err
	}
	return hostDir, containerDir, nil
}
