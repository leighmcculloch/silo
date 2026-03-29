package daytona

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	daytonasdk "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	daytonaerrors "github.com/daytonaio/daytona/libs/sdk-go/pkg/errors"
	daytonaoptions "github.com/daytonaio/daytona/libs/sdk-go/pkg/options"
	daytonatypes "github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/moby/term"
)

const (
	labelSilo         = "silo"
	labelSiloName     = "silo-name"
	labelSiloSnapshot = "silo-snapshot"
	tmuxSessionName   = "silo"
	tmuxConfigPath    = "/tmp/.silo-tmux.conf"
	toolLaunchScript  = "/tmp/.silo-start-tool-session.sh"
	attachSessionPath = "/tmp/.silo-attach-session.sh"
	toolLogPath       = "/tmp/.silo-tool.log"
	listPageSize      = 100
)

// Client implements backend.Backend using Daytona sandboxes.
type Client struct {
	client *daytonasdk.Client
	apiURL string
	target string
}

// NewClient creates a new Daytona backend client.
func NewClient(apiURL, target string) (*Client, error) {
	var cfg *daytonatypes.DaytonaConfig
	if apiURL != "" || target != "" {
		cfg = &daytonatypes.DaytonaConfig{
			APIUrl: apiURL,
			Target: target,
		}
	}

	client, err := daytonasdk.NewClientWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize daytona backend: %w", err)
	}

	return &Client{
		client: client,
		apiURL: apiURL,
		target: target,
	}, nil
}

// Close is a no-op.
func (c *Client) Close() error { return nil }

// FileMountsAreSymlinks reports false; files are synced directly.
func (c *Client) FileMountsAreSymlinks() bool { return false }

// ImageExists reports whether a snapshot exists and is active.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	snapshot, err := c.client.Snapshot.Get(ctx, name)
	if err != nil {
		if isDaytonaNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("daytona backend error: %w", err)
	}
	return snapshot != nil && snapshotStateActive(snapshot), nil
}

// Build creates or reuses a Daytona snapshot from the Dockerfile.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}
	if tag == "" {
		return "", fmt.Errorf("daytona build requires an image tag")
	}

	if existing, err := c.client.Snapshot.Get(ctx, tag); err == nil {
		if snapshotStateActive(existing) {
			return tag, nil
		}
		_ = c.client.Snapshot.Delete(ctx, existing)
	} else if !isDaytonaNotFound(err) {
		return "", fmt.Errorf("failed to inspect daytona snapshot %s: %w", tag, err)
	}

	dockerfile := prepareDockerfileForDaytona(opts.Dockerfile, opts.BuildArgs)
	image := daytonasdk.FromDockerfile(dockerfile)

	snapshot, logCh, err := c.client.Snapshot.Create(ctx, &daytonatypes.CreateSnapshotParams{
		Name:  tag,
		Image: image,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create daytona snapshot %s: %w", tag, err)
	}

	const tailSize = 20
	var tailBuf [tailSize]string
	tailIdx := 0
	tailCount := 0

	for line := range logCh {
		if opts.OnProgress != nil {
			if strings.HasSuffix(line, "\n") {
				opts.OnProgress(line)
			} else {
				opts.OnProgress(line + "\n")
			}
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			tailBuf[tailIdx%tailSize] = trimmed
			tailIdx++
			tailCount++
		}
	}

	snapshot, err = c.client.Snapshot.Get(ctx, snapshot.ID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect created daytona snapshot %s: %w", tag, err)
	}
	if snapshotStateActive(snapshot) {
		return tag, nil
	}

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

	errMsg := fmt.Sprintf("daytona snapshot %s finished in state %s", tag, snapshot.State)
	if snapshot.ErrorReason != nil && *snapshot.ErrorReason != "" {
		errMsg += ": " + *snapshot.ErrorReason
	}
	if detail.Len() > 0 {
		errMsg += "\n" + detail.String()
	}
	return "", fmt.Errorf("%s", errMsg)
}

// Run creates a Daytona sandbox, syncs files, and connects interactively.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	sandbox, err := c.createSandbox(ctx, opts)
	if err != nil {
		return err
	}

	c.printPreviewLinks(ctx, sandbox, opts.Ports)

	initialMounts := append([]string{}, opts.MountsRO...)
	initialMounts = append(initialMounts, opts.MountsRW...)
	if err := c.uploadMounts(ctx, sandbox, initialMounts, opts.CleanMountPaths); err != nil {
		_ = sandbox.Delete(context.Background())
		return fmt.Errorf("failed to sync mounts: %w", err)
	}

	hooks := filterMountWait(opts.PreRunHooks)
	if len(hooks) > 0 {
		fmt.Fprintf(os.Stderr, "  → Running pre-run hooks...\n")
		if err := c.runCommand(ctx, sandbox, strings.Join(hooks, " && ")); err != nil {
			_ = sandbox.Delete(context.Background())
			return fmt.Errorf("pre-run hook failed: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "  → Connecting...\n")
	attachStarted := time.Now()
	connectErr := c.attachToolSession(ctx, sandbox, opts)

	fmt.Fprintf(os.Stderr, "  → Syncing final changes...\n")
	if syncErr := c.downloadMounts(ctx, sandbox, opts.MountsRW); syncErr != nil && connectErr == nil {
		connectErr = fmt.Errorf("failed to sync final changes: %w", syncErr)
	}

	tmuxAlive := c.isTmuxSessionAlive(ctx, sandbox)
	if !tmuxAlive && time.Since(attachStarted) < 5*time.Second {
		if output := c.readToolLogTail(ctx, sandbox); output != "" {
			fmt.Fprintf(os.Stderr, "  → Tool output before exit:\n%s\n", indentLines(output, "    "))
		}
	}

	if tmuxAlive {
		fmt.Fprintf(os.Stderr, "  → Detached. Sandbox %s still running — use 'silo reconnect %s --backend daytona' to reattach.\n", sandbox.ID, opts.Name)
		return nil
	}

	fmt.Fprintf(os.Stderr, "  → Destroying sandbox...\n")
	if err := sandbox.Delete(context.Background()); err != nil && connectErr == nil {
		connectErr = fmt.Errorf("failed to delete sandbox: %w", err)
	}

	return connectErr
}

// Exec runs a command inside a running Daytona sandbox with interactive TTY.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	sandbox, err := c.resolveSandbox(ctx, name, true)
	if err != nil {
		return err
	}
	return c.attachInteractiveCommand(ctx, sandbox, command)
}

// Reconnect syncs files back from the sandbox and reattaches to the tool session.
func (c *Client) Reconnect(ctx context.Context, name string, opts backend.RunOptions) error {
	sandbox, err := c.resolveSandbox(ctx, name, true)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "  → Syncing files...\n")
	if err := c.uploadMounts(ctx, sandbox, opts.MountsRO, nil); err != nil {
		return fmt.Errorf("failed to sync read-only mounts: %w", err)
	}
	if err := c.downloadMounts(ctx, sandbox, opts.MountsRW); err != nil {
		return fmt.Errorf("failed to sync read-write mounts: %w", err)
	}

	fmt.Fprintf(os.Stderr, "  → Reconnecting...\n")
	connectErr := c.attachTmuxSession(ctx, sandbox)

	fmt.Fprintf(os.Stderr, "  → Syncing final changes...\n")
	if syncErr := c.downloadMounts(ctx, sandbox, opts.MountsRW); syncErr != nil && connectErr == nil {
		connectErr = fmt.Errorf("failed to sync final changes: %w", syncErr)
	}

	if c.isTmuxSessionAlive(ctx, sandbox) {
		fmt.Fprintf(os.Stderr, "  → Detached. Sandbox %s still running — use 'silo reconnect %s --backend daytona' to reattach.\n", sandbox.ID, name)
		return nil
	}

	fmt.Fprintf(os.Stderr, "  → Destroying sandbox...\n")
	if err := sandbox.Delete(context.Background()); err != nil && connectErr == nil {
		connectErr = fmt.Errorf("failed to delete sandbox: %w", err)
	}

	return connectErr
}

// List returns all silo-created Daytona sandboxes.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	sandboxes, err := c.listSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]backend.ContainerInfo, 0, len(sandboxes))
	for _, sandbox := range sandboxes {
		state := strings.ToLower(string(sandbox.State))
		result = append(result, backend.ContainerInfo{
			Name:      sandbox.Name,
			Image:     "",
			Status:    state,
			IsRunning: sandboxStateStarted(sandbox),
		})
	}
	return result, nil
}

// Remove deletes specific sandboxes by name.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	sandboxes, err := c.listSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	toRemove := make(map[string]bool, len(names))
	for _, name := range names {
		toRemove[name] = true
	}

	var removed []string
	for _, sandbox := range sandboxes {
		if !toRemove[sandbox.Name] {
			continue
		}
		if err := sandbox.Delete(ctx); err != nil {
			return removed, fmt.Errorf("failed to remove sandbox %s: %w", sandbox.Name, err)
		}
		removed = append(removed, sandbox.Name)
	}
	return removed, nil
}

// NextContainerName returns the next sequential name for the given base name.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	sandboxes, err := c.listSandboxes(ctx)
	if err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := baseName + "-"
	for _, sandbox := range sandboxes {
		if suffix, ok := strings.CutPrefix(sandbox.Name, prefix); ok {
			var num int
			if _, err := fmt.Sscanf(suffix, "%d", &num); err == nil && num > maxNum {
				maxNum = num
			}
		}
	}

	return fmt.Sprintf("%s-%d", baseName, maxNum+1)
}

func (c *Client) createSandbox(ctx context.Context, opts backend.RunOptions) (*daytonasdk.Sandbox, error) {
	env := envSliceToMap(opts.Env)
	user := currentUsername()

	sandbox, err := c.client.Create(ctx, daytonatypes.SnapshotParams{
		Snapshot: opts.Image,
		SandboxBaseParams: daytonatypes.SandboxBaseParams{
			Name:      opts.Name,
			User:      user,
			EnvVars:   env,
			Ephemeral: true,
			Labels: map[string]string{
				labelSilo:         "true",
				labelSiloName:     opts.Name,
				labelSiloSnapshot: opts.Image,
			},
		},
	}, daytonaoptions.WithTimeout(10*time.Minute))
	if err != nil {
		return nil, fmt.Errorf("failed to create daytona sandbox: %w", err)
	}
	return sandbox, nil
}

func (c *Client) resolveSandbox(ctx context.Context, name string, mustBeStarted bool) (*daytonasdk.Sandbox, error) {
	sandboxes, err := c.listSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	for _, sandbox := range sandboxes {
		if sandbox.Name != name {
			continue
		}
		if mustBeStarted && !sandboxStateStarted(sandbox) {
			return nil, fmt.Errorf("sandbox %s is not running (state: %s)", name, sandbox.State)
		}
		return sandbox, nil
	}
	return nil, fmt.Errorf("container %s not found", name)
}

func (c *Client) listSandboxes(ctx context.Context) ([]*daytonasdk.Sandbox, error) {
	page := 1
	limit := listPageSize
	labels := map[string]string{labelSilo: "true"}

	var all []*daytonasdk.Sandbox
	for {
		result, err := c.client.List(ctx, labels, &page, &limit)
		if err != nil {
			return nil, fmt.Errorf("failed to list daytona sandboxes: %w", err)
		}

		all = append(all, result.Items...)
		if result.TotalPages == 0 || page >= result.TotalPages {
			break
		}
		page++
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})
	return all, nil
}

func (c *Client) runCommand(ctx context.Context, sandbox *daytonasdk.Sandbox, command string) error {
	result, err := sandbox.Process.ExecuteCommand(ctx, command)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		output := strings.TrimSpace(result.Result)
		if output != "" {
			return fmt.Errorf("command exited with status %d: %s", result.ExitCode, output)
		}
		return fmt.Errorf("command exited with status %d", result.ExitCode)
	}
	return nil
}

func (c *Client) attachToolSession(ctx context.Context, sandbox *daytonasdk.Sandbox, opts backend.RunOptions) error {
	scriptPath, err := c.prepareToolLaunchScript(ctx, sandbox, opts)
	if err != nil {
		return err
	}
	return c.attachPTY(ctx, sandbox, "exec "+shellquote.Join("bash", scriptPath))
}

func (c *Client) prepareToolLaunchScript(ctx context.Context, sandbox *daytonasdk.Sandbox, opts backend.RunOptions) (string, error) {
	fullCmd := append([]string{}, opts.Command...)
	fullCmd = append(fullCmd, opts.Args...)

	var toolParts []string
	if opts.WorkDir != "" {
		toolParts = append(toolParts, fmt.Sprintf("cd %s", shellquote.Join(opts.WorkDir)))
	}
	if len(fullCmd) > 0 {
		toolParts = append(toolParts, "exec "+shellquote.Join(fullCmd...))
	} else {
		toolParts = append(toolParts, "exec bash -l")
	}

	toolCmd := strings.Join(toolParts, " && ")
	tmuxConf := strings.Join([]string{
		`set -g status off`,
		`set -g mouse on`,
		`set -g default-terminal "tmux-256color"`,
		"",
	}, "\n")
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -e",
		"export LANG=C.UTF-8",
		"export LC_ALL=C.UTF-8",
		fmt.Sprintf("printf %%s %s > %s", shellquote.Join(tmuxConf), shellquote.Join(tmuxConfigPath)),
		fmt.Sprintf(": > %s", shellquote.Join(toolLogPath)),
		fmt.Sprintf("if tmux has-session -t %s 2>/dev/null; then", tmuxSessionName),
		fmt.Sprintf("  exec tmux -u attach-session -t %s", tmuxSessionName),
		"fi",
		fmt.Sprintf("exec tmux -u -f %s new-session -s %s -e LANG=C.UTF-8 -e LC_ALL=C.UTF-8 %s \\; pipe-pane -o %s",
			shellquote.Join(tmuxConfigPath),
			tmuxSessionName,
			shellquote.Join("bash", "-l", "-c", toolCmd),
			shellquote.Join("cat >> "+toolLogPath),
		),
		"",
	}, "\n")
	writeCmd := fmt.Sprintf(
		"printf %%s %s > %s && chmod 755 %s",
		shellquote.Join(script),
		shellquote.Join(toolLaunchScript),
		shellquote.Join(toolLaunchScript),
	)
	if err := c.runCommand(ctx, sandbox, writeCmd); err != nil {
		return "", fmt.Errorf("failed to prepare tool launch script: %w", err)
	}
	return toolLaunchScript, nil
}

func (c *Client) attachTmuxSession(ctx context.Context, sandbox *daytonasdk.Sandbox) error {
	scriptPath, err := c.prepareAttachScript(ctx, sandbox)
	if err != nil {
		return err
	}
	return c.attachPTY(ctx, sandbox, "exec "+shellquote.Join("bash", scriptPath))
}

func (c *Client) attachInteractiveCommand(ctx context.Context, sandbox *daytonasdk.Sandbox, command []string) error {
	var remoteCmd string
	if len(command) == 0 {
		remoteCmd = "exec bash -l"
	} else {
		remoteCmd = "exec " + shellquote.Join(command...)
	}
	return c.attachPTY(ctx, sandbox, remoteCmd)
}

func (c *Client) prepareAttachScript(ctx context.Context, sandbox *daytonasdk.Sandbox) (string, error) {
	script := strings.Join([]string{
		"#!/usr/bin/env bash",
		"set -e",
		"export LANG=C.UTF-8",
		"export LC_ALL=C.UTF-8",
		fmt.Sprintf("exec tmux -u attach-session -t %s", tmuxSessionName),
		"",
	}, "\n")
	writeCmd := fmt.Sprintf(
		"printf %%s %s > %s && chmod 755 %s",
		shellquote.Join(script),
		shellquote.Join(attachSessionPath),
		shellquote.Join(attachSessionPath),
	)
	if err := c.runCommand(ctx, sandbox, writeCmd); err != nil {
		return "", fmt.Errorf("failed to prepare tmux attach script: %w", err)
	}
	return attachSessionPath, nil
}

func (c *Client) attachPTY(ctx context.Context, sandbox *daytonasdk.Sandbox, remoteCmd string) error {
	ptyID := fmt.Sprintf("silo-%d", time.Now().UnixNano())
	createOpts := []func(*daytonaoptions.CreatePty){}

	fd := os.Stdin.Fd()
	if term.IsTerminal(fd) {
		if winsize, err := term.GetWinsize(fd); err == nil {
			createOpts = append(createOpts, daytonaoptions.WithCreatePtySize(daytonatypes.PtySize{
				Rows: int(winsize.Height),
				Cols: int(winsize.Width),
			}))
		}
	}
	createOpts = append(createOpts, daytonaoptions.WithCreatePtyEnv(map[string]string{
		"TERM": "xterm-256color",
	}))

	handle, err := sandbox.Process.CreatePty(ctx, ptyID, createOpts...)
	if err != nil {
		return fmt.Errorf("failed to create PTY session: %w", err)
	}
	defer handle.Disconnect()

	if err := handle.WaitForConnection(ctx); err != nil {
		return fmt.Errorf("PTY connection failed: %w", err)
	}

	restore, err := makeRawTerminal(fd)
	if err != nil {
		return err
	}
	defer restore()

	resizeCtx, resizeCancel := context.WithCancel(context.Background())
	defer resizeCancel()
	go monitorPtySize(resizeCtx, handle, fd)

	outputDone := make(chan struct{})
	go func() {
		_ = copyPTYOutput(os.Stdout, handle, remoteCmd)
		close(outputDone)
	}()

	// Daytona marks the websocket connected before the interactive shell is
	// always ready to read input. Give it a brief moment so the first command
	// isn't dropped or partially echoed.
	time.Sleep(150 * time.Millisecond)

	if remoteCmd != "" {
		_ = handle.SendInput([]byte("stty -echo\n"))
		time.Sleep(50 * time.Millisecond)
		if err := handle.SendInput([]byte(remoteCmd + "\n")); err != nil {
			return fmt.Errorf("failed to send command to PTY: %w", err)
		}
	}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if sendErr := handle.SendInput(buf[:n]); sendErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	result, err := handle.Wait(ctx)
	resizeCancel()
	<-outputDone
	if err != nil {
		return fmt.Errorf("session error: %w", err)
	}
	if result.Error != nil && *result.Error != "" {
		return fmt.Errorf("session error: %s", *result.Error)
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		return fmt.Errorf("session exited with status %d", *result.ExitCode)
	}
	return nil
}

func copyPTYOutput(dst io.Writer, src io.Reader, remoteCmd string) error {
	if remoteCmd == "" {
		_, err := io.Copy(dst, src)
		return err
	}

	type readResult struct {
		data []byte
		err  error
	}

	results := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			chunk := append([]byte(nil), buf[:n]...)
			results <- readResult{data: chunk, err: err}
			if err != nil {
				close(results)
				return
			}
		}
	}()

	var (
		initial bytes.Buffer
		timer   *time.Timer
		timerCh <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	flushInitial := func() error {
		if initial.Len() == 0 {
			return nil
		}
		cleaned := trimBootstrapOutput(initial.Bytes(), remoteCmd)
		initial.Reset()
		if len(cleaned) == 0 {
			return nil
		}
		_, err := dst.Write(cleaned)
		return err
	}

	buffering := true
	for buffering {
		select {
		case result, ok := <-results:
			if !ok {
				if err := flushInitial(); err != nil {
					return err
				}
				return nil
			}
			if len(result.data) > 0 {
				initial.Write(result.data)
				if timer == nil {
					timer = time.NewTimer(500 * time.Millisecond)
					timerCh = timer.C
				}
			}
			if result.err != nil {
				if err := flushInitial(); err != nil {
					return err
				}
				if result.err == io.EOF {
					return nil
				}
				return result.err
			}
		case <-timerCh:
			if err := flushInitial(); err != nil {
				return err
			}
			buffering = false
		}
	}

	for result := range results {
		if len(result.data) > 0 {
			if _, err := dst.Write(result.data); err != nil {
				return err
			}
		}
		if result.err != nil {
			if result.err == io.EOF {
				return nil
			}
			return result.err
		}
	}

	return nil
}

func trimBootstrapOutput(data []byte, remoteCmd string) []byte {
	data = trimEchoedCommand(data, []byte("stty -echo"))
	if remoteCmd != "" {
		data = trimEchoedCommand(data, []byte(remoteCmd))
	}
	return data
}

func trimEchoedCommand(data, needle []byte) []byte {
	idx := bytes.Index(data, needle)
	if idx == -1 {
		return data
	}

	end := idx + len(needle)
	for end < len(data) && data[end] != '\r' && data[end] != '\n' {
		end++
	}
	for end < len(data) && (data[end] == '\r' || data[end] == '\n') {
		end++
	}
	return data[end:]
}

func (c *Client) isTmuxSessionAlive(ctx context.Context, sandbox *daytonasdk.Sandbox) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := sandbox.Process.ExecuteCommand(checkCtx, fmt.Sprintf("tmux has-session -t %s", tmuxSessionName))
	if err != nil {
		return false
	}
	return result.ExitCode == 0
}

func (c *Client) readToolLogTail(ctx context.Context, sandbox *daytonasdk.Sandbox) string {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := sandbox.Process.ExecuteCommand(checkCtx, fmt.Sprintf("tail -n 80 %s 2>/dev/null || true", shellquote.Join(toolLogPath)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Result)
}

func (c *Client) printPreviewLinks(ctx context.Context, sandbox *daytonasdk.Sandbox, ports []string) {
	for _, port := range ports {
		containerPort, ok := containerPortFromMapping(port)
		if !ok {
			continue
		}
		preview, err := sandbox.GetPreviewLink(ctx, containerPort)
		if err != nil || preview == nil || preview.URL == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "  → Preview %d: %s\n", containerPort, preview.URL)
	}
}

func (c *Client) uploadMounts(ctx context.Context, sandbox *daytonasdk.Sandbox, mounts, extraClean []string) error {
	mounts = reduceOverlappingMounts(mounts)
	if len(mounts) == 0 {
		return nil
	}

	cleanSet := make(map[string]struct{}, len(mounts)+len(extraClean))
	for _, path := range mounts {
		if _, err := os.Lstat(path); err == nil {
			cleanSet[path] = struct{}{}
		}
	}
	for _, path := range extraClean {
		if path != "" {
			cleanSet[path] = struct{}{}
		}
	}

	if len(cleanSet) > 0 {
		var cleanPaths []string
		parentSet := map[string]struct{}{}
		for path := range cleanSet {
			cleanPaths = append(cleanPaths, path)
			parentSet[filepath.Dir(path)] = struct{}{}
		}
		sort.Strings(cleanPaths)

		var parents []string
		for path := range parentSet {
			parents = append(parents, path)
		}
		sort.Strings(parents)

		var scriptParts []string
		scriptParts = append(scriptParts, "rm -rf "+shellquote.Join(cleanPaths...))
		scriptParts = append(scriptParts, "mkdir -p "+shellquote.Join(parents...))
		if err := c.runCommand(ctx, sandbox, strings.Join(scriptParts, " && ")); err != nil {
			return err
		}
	}

	for i, path := range mounts {
		if _, err := os.Lstat(path); err != nil {
			continue
		}

		tarPath, cleanup, err := createTarArchive(path)
		if err != nil {
			return fmt.Errorf("failed to archive %s: %w", path, err)
		}

		remoteTar := fmt.Sprintf("/tmp/silo-upload-%d-%d.tar", time.Now().UnixNano(), i)
		if err := sandbox.FileSystem.UploadFile(ctx, tarPath, remoteTar); err != nil {
			cleanup()
			return fmt.Errorf("failed to upload %s: %w", path, err)
		}
		cleanup()

		extractCmd := fmt.Sprintf("tar -xf %s -C / && rm -f %s",
			shellquote.Join(remoteTar),
			shellquote.Join(remoteTar),
		)
		if err := c.runCommand(ctx, sandbox, extractCmd); err != nil {
			return fmt.Errorf("failed to extract %s: %w", path, err)
		}
	}

	return nil
}

func (c *Client) downloadMounts(ctx context.Context, sandbox *daytonasdk.Sandbox, mounts []string) error {
	mounts = reduceOverlappingMounts(mounts)
	if len(mounts) == 0 {
		return nil
	}

	for i, path := range mounts {
		if _, err := sandbox.FileSystem.GetFileInfo(ctx, path); err != nil {
			if isDaytonaNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to stat remote path %s: %w", path, err)
		}

		remoteTar := fmt.Sprintf("/tmp/silo-download-%d-%d.tar", time.Now().UnixNano(), i)
		extractPath := strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator))
		createCmd := fmt.Sprintf("tar -cf %s -C / %s",
			shellquote.Join(remoteTar),
			shellquote.Join(extractPath),
		)
		if err := c.runCommand(ctx, sandbox, createCmd); err != nil {
			return fmt.Errorf("failed to archive remote path %s: %w", path, err)
		}

		localTar, err := os.CreateTemp("", "silo-daytona-download-*.tar")
		if err != nil {
			_ = c.runCommand(context.Background(), sandbox, "rm -f "+shellquote.Join(remoteTar))
			return fmt.Errorf("failed to create local temp archive: %w", err)
		}
		localTar.Close()

		localPath := localTar.Name()
		if _, err := sandbox.FileSystem.DownloadFile(ctx, remoteTar, &localPath); err != nil {
			_ = os.Remove(localPath)
			_ = c.runCommand(context.Background(), sandbox, "rm -f "+shellquote.Join(remoteTar))
			return fmt.Errorf("failed to download %s: %w", path, err)
		}

		if err := extractTarArchive(localPath); err != nil {
			_ = os.Remove(localPath)
			_ = c.runCommand(context.Background(), sandbox, "rm -f "+shellquote.Join(remoteTar))
			return fmt.Errorf("failed to extract %s: %w", path, err)
		}

		_ = os.Remove(localPath)
		_ = c.runCommand(context.Background(), sandbox, "rm -f "+shellquote.Join(remoteTar))
	}

	return nil
}

func snapshotStateActive(snapshot *daytonatypes.Snapshot) bool {
	return snapshot != nil && strings.EqualFold(snapshot.State, "active")
}

func sandboxStateStarted(sandbox *daytonasdk.Sandbox) bool {
	return sandbox != nil && strings.EqualFold(string(sandbox.State), "started")
}

func isDaytonaNotFound(err error) bool {
	var notFound *daytonaerrors.DaytonaNotFoundError
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "not found") || errors.As(err, &notFound))
}

func prepareDockerfileForDaytona(dockerfile string, buildArgs map[string]string) string {
	lines := strings.Split(dockerfile, "\n")
	var rewritten []string
	for _, line := range lines {
		rewritten = append(rewritten, line)
		if strings.HasPrefix(strings.TrimSpace(line), "FROM ") {
			rewritten = append(rewritten, "ENV DEBIAN_FRONTEND=noninteractive")
		}
	}

	if len(buildArgs) == 0 {
		return strings.Join(rewritten, "\n")
	}

	lines = rewritten
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ARG ") {
			continue
		}

		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "ARG "))
		if idx := strings.Index(name, "="); idx >= 0 {
			name = name[:idx]
		}
		value, ok := buildArgs[name]
		if !ok {
			continue
		}

		prefix := line[:strings.Index(line, "ARG ")]
		lines[i] = prefix + "ARG " + name + "=" + value
	}
	return strings.Join(lines, "\n")
}

func envSliceToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}

	result := make(map[string]string, len(env))
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		result[k] = v
	}
	return result
}

func reduceOverlappingMounts(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(paths))
	var unique []string
	for _, path := range paths {
		clean := filepath.Clean(path)
		if clean == "." || clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		unique = append(unique, clean)
	}

	sort.Slice(unique, func(i, j int) bool {
		if len(unique[i]) != len(unique[j]) {
			return len(unique[i]) < len(unique[j])
		}
		return unique[i] < unique[j]
	})

	var result []string
	for _, candidate := range unique {
		covered := false
		for _, existing := range result {
			if samePathOrChild(candidate, existing) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, candidate)
		}
	}
	return result
}

func samePathOrChild(path, parent string) bool {
	if path == parent {
		return true
	}
	parent = filepath.Clean(parent)
	path = filepath.Clean(path)
	if !strings.HasSuffix(parent, string(filepath.Separator)) {
		parent += string(filepath.Separator)
	}
	return strings.HasPrefix(path+string(filepath.Separator), parent)
}

func createTarArchive(path string) (string, func(), error) {
	tmpFile, err := os.CreateTemp("", "silo-daytona-upload-*.tar")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() {
		_ = os.Remove(tmpFile.Name())
	}

	tw := tar.NewWriter(tmpFile)
	err = filepath.WalkDir(path, func(walkPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		info, err := os.Lstat(walkPath)
		if err != nil {
			return err
		}

		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(walkPath)
			if err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(strings.TrimPrefix(filepath.Clean(walkPath), string(filepath.Separator)))
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(walkPath)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
	closeErr := tw.Close()
	syncErr := tmpFile.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if closeErr != nil {
		cleanup()
		return "", nil, closeErr
	}
	if syncErr != nil {
		cleanup()
		return "", nil, syncErr
	}

	return tmpFile.Name(), cleanup, nil
}

func extractTarArchive(tarPath string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(string(filepath.Separator), filepath.Clean(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			linkTarget := filepath.Join(string(filepath.Separator), filepath.Clean(header.Linkname))
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
}

func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "root"
}

func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func makeRawTerminal(fd uintptr) (func(), error) {
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("failed to set raw terminal: %w", err)
	}
	restore := func() {
		_ = term.RestoreTerminal(fd, oldState)
		os.Stdout.WriteString("\x1b[?1000l")
		os.Stdout.WriteString("\x1b[?1002l")
		os.Stdout.WriteString("\x1b[?1003l")
		os.Stdout.WriteString("\x1b[?1006l")
		os.Stdout.WriteString("\x1b[?25h")
	}
	return restore, nil
}

func monitorPtySize(ctx context.Context, handle *daytonasdk.PtyHandle, fd uintptr) {
	if !term.IsTerminal(fd) {
		return
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	resize := func() {
		winsize, err := term.GetWinsize(fd)
		if err != nil {
			return
		}
		_, _ = handle.Resize(context.Background(), int(winsize.Width), int(winsize.Height))
	}

	resize()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			resize()
		}
	}
}

func containerPortFromMapping(mapping string) (int, bool) {
	parts := strings.Split(mapping, ":")
	portStr := parts[len(parts)-1]
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

func filterMountWait(hooks []string) []string {
	var result []string
	for _, hook := range hooks {
		if strings.Contains(hook, "__silo_mount_ready") {
			continue
		}
		result = append(result, hook)
	}
	return result
}

// Ensure Client implements backend.Backend at compile time.
var _ backend.Backend = (*Client)(nil)
