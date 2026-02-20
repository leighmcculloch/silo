package ssh

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/moby/term"
	cryptossh "golang.org/x/crypto/ssh"
)

// Client implements backend.Backend by executing Docker commands on a
// remote host over SSH. File mounts are synced to the remote before
// container operations, and interactive sessions use PTY forwarding.
type Client struct {
	cfg     SSHBackendConfig
	sshConn *cryptossh.Client
	syncer  *Syncer
}

// NewClient connects to the remote host and returns a ready-to-use Client.
func NewClient(cfg SSHBackendConfig) (*Client, error) {
	conn, err := Connect(cfg)
	if err != nil {
		return nil, err
	}
	syncer := NewSyncer(cfg, conn)
	return &Client{
		cfg:     cfg,
		sshConn: conn,
		syncer:  syncer,
	}, nil
}

// Build writes the Dockerfile to a temp directory on the remote host,
// runs docker build via SSH, and streams output back through OnProgress.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	// Create temp directory on remote.
	tmpDir, err := execRemote(c.sshConn, "mktemp -d")
	if err != nil {
		return "", fmt.Errorf("ssh build: create temp dir: %w", err)
	}
	defer func() {
		// Best-effort cleanup.
		_, _ = execRemote(c.sshConn, fmt.Sprintf("rm -rf %s", shellQuote(tmpDir)))
	}()

	// Write Dockerfile to remote.
	if err := writeRemoteFile(c.sshConn, tmpDir+"/Dockerfile", opts.Dockerfile); err != nil {
		return "", fmt.Errorf("ssh build: write Dockerfile: %w", err)
	}

	// Determine the image tag.
	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}

	// Build docker build command.
	args := []string{"docker", "build", "-f", tmpDir + "/Dockerfile"}
	if opts.Target != "" {
		args = append(args, "--target", opts.Target)
	}
	args = append(args, "-t", tag)
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, tmpDir)

	cmd := shellquote.Join(args...)
	if err := execRemoteStreaming(ctx, c.sshConn, cmd, opts.OnProgress); err != nil {
		return "", fmt.Errorf("ssh build: %w", err)
	}
	return tag, nil
}

// ImageExists checks whether a Docker image exists on the remote host.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	cmd := fmt.Sprintf("docker image inspect %s", shellQuote(name))
	_, err := execRemote(c.sshConn, cmd)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "no such image") || strings.Contains(errMsg, "not found") {
			return false, nil
		}
		return false, fmt.Errorf("ssh image check %s: %w", name, err)
	}
	return true, nil
}

// NextContainerName queries running/stopped containers on the remote host
// and returns the next sequential name (baseName-N).
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	cmd := fmt.Sprintf("docker ps -a --format '{{.Names}}' --filter 'name=%s-'", shellQuote(baseName))
	output, err := execRemote(c.sshConn, cmd)
	if err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := baseName + "-"
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
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

// Run syncs local mounts to the remote, maps paths, and executes docker run
// with interactive PTY forwarding over SSH.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	// Filter out non-existent paths (matching Docker/Container backend behavior).
	var mountsRO, mountsRW []string
	for _, m := range opts.MountsRO {
		if _, err := os.Lstat(m); err == nil {
			mountsRO = append(mountsRO, m)
		}
	}
	for _, m := range opts.MountsRW {
		if _, err := os.Lstat(m); err == nil {
			mountsRW = append(mountsRW, m)
		}
	}

	// Collect all local paths that need syncing.
	allLocalPaths := make([]string, 0, len(mountsRO)+len(mountsRW))
	allLocalPaths = append(allLocalPaths, mountsRO...)
	allLocalPaths = append(allLocalPaths, mountsRW...)

	// Push all files to remote.
	remotePaths, err := c.syncer.Push(ctx, allLocalPaths)
	if err != nil {
		return fmt.Errorf("ssh run: push: %w", err)
	}

	// Start background pull-back for RW mounts.
	if len(mountsRW) > 0 {
		rwMappings := make(map[string]string, len(mountsRW))
		for _, localPath := range mountsRW {
			if remotePath, ok := remotePaths[localPath]; ok {
				rwMappings[localPath] = remotePath
			}
		}
		if err := c.syncer.WatchAndPullBack(ctx, rwMappings); err != nil {
			return fmt.Errorf("ssh run: watch: %w", err)
		}
	}

	// Build docker run command with security hardening matching the Docker backend.
	args := []string{"docker", "run", "-it", "--rm",
		"--init",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--ipc", "private",
	}
	args = append(args, "--name", opts.Name)

	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}

	// Map read-only mounts.
	for _, localPath := range mountsRO {
		remotePath, ok := remotePaths[localPath]
		if !ok {
			continue
		}
		// Mount from remote sync path to the same path inside the container.
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro", remotePath, localPath))
	}

	// Map read-write mounts.
	for _, localPath := range mountsRW {
		remotePath, ok := remotePaths[localPath]
		if !ok {
			continue
		}
		args = append(args, "-v", fmt.Sprintf("%s:%s", remotePath, localPath))
	}

	// Environment variables.
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}

	// Build entrypoint/command.
	fullCmd := append(opts.Command, opts.Args...)
	if len(opts.PreRunHooks) > 0 {
		// Hooks present: run via bash with hooks chained before the command.
		var script strings.Builder
		for _, hook := range opts.PreRunHooks {
			script.WriteString(hook)
			script.WriteString(" && ")
		}
		if len(fullCmd) > 0 {
			script.WriteString("exec ")
			script.WriteString(shellquote.Join(fullCmd...))
		} else {
			script.WriteString("exec /bin/bash")
		}
		args = append(args, "--entrypoint", "/bin/bash")
		args = append(args, opts.Image, "-c", script.String())
	} else if len(fullCmd) > 0 {
		// Override entrypoint to match Docker backend behavior.
		args = append(args, "--entrypoint", fullCmd[0])
		args = append(args, opts.Image)
		args = append(args, fullCmd[1:]...)
	} else {
		args = append(args, opts.Image)
	}

	cmd := shellquote.Join(args...)
	return c.execInteractive(ctx, cmd)
}

// Exec runs a command inside a running container on the remote host
// with interactive PTY forwarding.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	args := []string{"docker", "exec", "-it", name}
	args = append(args, command...)
	cmd := shellquote.Join(args...)
	return c.execInteractive(ctx, cmd)
}

// List queries Docker on the remote host for silo containers and returns
// their info.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	cmd := "docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.State}}' --filter 'name=silo-'"
	output, err := execRemote(c.sshConn, cmd)
	if err != nil {
		return nil, fmt.Errorf("ssh list: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return nil, nil
	}

	var containers []backend.ContainerInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}

		name := parts[0]
		image := parts[1]
		status := parts[2]
		state := parts[3]

		// Only include containers with silo- image prefix.
		if !strings.HasPrefix(image, "silo-") {
			continue
		}

		containers = append(containers, backend.ContainerInfo{
			Name:      name,
			Image:     image,
			Status:    status,
			IsRunning: state == "running",
		})
	}
	return containers, nil
}

// Remove removes the named containers on the remote host.
// Only containers with a silo- image prefix are removed, matching the
// Docker and Container backend safety behavior.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	var removed []string
	for _, name := range names {
		// Verify the container has a silo- image before removing.
		inspectCmd := fmt.Sprintf("docker inspect --format '{{.Config.Image}}' %s", shellQuote(name))
		image, err := execRemote(c.sshConn, inspectCmd)
		if err != nil {
			return removed, fmt.Errorf("ssh remove %s: %w", name, err)
		}
		if !strings.HasPrefix(image, "silo-") {
			continue
		}

		cmd := fmt.Sprintf("docker rm -f %s", shellQuote(name))
		if _, err := execRemote(c.sshConn, cmd); err != nil {
			return removed, fmt.Errorf("ssh remove %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// Close closes the SSH connection and any persistent sync sessions.
func (c *Client) Close() error {
	var errs []string
	if c.syncer != nil {
		if err := c.syncer.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.sshConn != nil {
		if err := c.sshConn.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("ssh close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// execInteractive runs a command on the remote host with full PTY forwarding.
// It allocates a PTY on the remote, sets the local terminal to raw mode, and
// forwards SIGWINCH for window resize.
func (c *Client) execInteractive(ctx context.Context, cmd string) error {
	session, err := c.sshConn.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Get local terminal size.
	fd := os.Stdin.Fd()
	if term.IsTerminal(fd) {
		winsize, err := term.GetWinsize(fd)
		if err != nil {
			return fmt.Errorf("get terminal size: %w", err)
		}

		// Request PTY on remote.
		modes := cryptossh.TerminalModes{
			cryptossh.ECHO:          1,
			cryptossh.TTY_OP_ISPEED: 14400,
			cryptossh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty("xterm-256color", int(winsize.Height), int(winsize.Width), modes); err != nil {
			return fmt.Errorf("request pty: %w", err)
		}

		// Set local terminal to raw mode.
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("set raw terminal: %w", err)
		}
		defer term.RestoreTerminal(fd, oldState)

		// Handle window resize signals.
		resizeCtx, resizeCancel := context.WithCancel(ctx)
		defer resizeCancel()
		go c.watchWindowSize(resizeCtx, session, fd)
	} else {
		// No terminal -- request PTY anyway for remote docker -it to work,
		// using a default size.
		modes := cryptossh.TerminalModes{
			cryptossh.ECHO:          1,
			cryptossh.TTY_OP_ISPEED: 14400,
			cryptossh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
			return fmt.Errorf("request pty: %w", err)
		}
	}

	// Wire stdin/stdout/stderr.
	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// Forward SIGINT/SIGTERM to the remote session.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case sig := <-sigCh:
				switch sig {
				case syscall.SIGINT:
					_ = session.Signal(cryptossh.SIGINT)
				case syscall.SIGTERM:
					_ = session.Signal(cryptossh.SIGTERM)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := session.Run(cmd); err != nil {
		// If it's an ExitError, propagate the exit status.
		if exitErr, ok := err.(*cryptossh.ExitError); ok {
			return fmt.Errorf("command exited with status %d", exitErr.ExitStatus())
		}
		// An io.EOF when context is done is expected (session closed).
		if ctx.Err() != nil && err == io.EOF {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// watchWindowSize monitors SIGWINCH and updates the remote PTY size.
func (c *Client) watchWindowSize(ctx context.Context, session *cryptossh.Session, fd uintptr) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			winsize, err := term.GetWinsize(fd)
			if err != nil {
				continue
			}
			_ = session.WindowChange(int(winsize.Height), int(winsize.Width))
		}
	}
}
