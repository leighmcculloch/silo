package freestyle

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
)

const (
	apiBase    = "https://api.freestyle.sh"
	sshHost    = "vm-ssh.freestyle.sh"
	tokenEnvar = "FREESTYLE_TOKEN"
)

// Client implements backend.Backend using Freestyle VMs.
type Client struct {
	token string
	logw  io.Writer
}

// NewClient creates a new Freestyle backend client.
// If configToken is non-empty it is used; otherwise FREESTYLE_TOKEN env var is read.
func NewClient(configToken string, logw io.Writer) (*Client, error) {
	token := configToken
	if token == "" {
		token = os.Getenv(tokenEnvar)
	}
	if token == "" {
		return nil, fmt.Errorf("freestyle backend requires %s environment variable or backends.freestyle.token in silo.jsonc (get a token at https://dash.freestyle.sh)", tokenEnvar)
	}
	if logw == nil {
		logw = os.Stderr
	}
	return &Client{token: token, logw: logw}, nil
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

// NeedsMountWait reports false; files are uploaded before the tool runs.
func (c *Client) NeedsMountWait() bool { return false }

// --- VM API types ---

type createVMRequest struct {
	IdleTimeoutSeconds *int           `json:"idleTimeoutSeconds,omitempty"`
	Persistence        *vmPersistence `json:"persistence,omitempty"`
}

type vmPersistence struct {
	Type     string `json:"type"`
	Priority *int   `json:"priority,omitempty"`
}

type forkVMRequest struct {
	IdleTimeoutSeconds *int           `json:"idleTimeoutSeconds,omitempty"`
	Persistence        *vmPersistence `json:"persistence,omitempty"`
}

type startVMRequest struct {
	IdleTimeoutSeconds *int `json:"idleTimeoutSeconds,omitempty"`
}

type execAwaitRequest struct {
	Command  string  `json:"command"`
	Terminal *string `json:"terminal,omitempty"`
}

type execAwaitResponse struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type vmInfo struct {
	ID    string            `json:"id"`
	State string            `json:"state"`
	Meta  map[string]string `json:"metadata"`
}

// --- REST API helpers ---

func (c *Client) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, result any) error {
	resp, err := c.doRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("freestyle API %s %s: %s %s", method, path, resp.Status, string(respBody))
	}
	if result != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, result)
	}
	return nil
}

// --- Image/Build tracking ---
//
// Freestyle has no image registry. Build creates a VM, runs setup commands,
// and stops it. We store tag→VM_ID mappings locally so ImageExists and Run
// can reference the built VM.

func (c *Client) imageStorePath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "silo", "freestyle-images.json")
}

func (c *Client) loadImageMap() map[string]string {
	data, err := os.ReadFile(c.imageStorePath())
	if err != nil {
		return make(map[string]string)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string]string)
	}
	return m
}

func (c *Client) saveImageMap(m map[string]string) error {
	path := c.imageStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Client) setImage(tag, vmID string) error {
	m := c.loadImageMap()
	m[tag] = vmID
	return c.saveImageMap(m)
}

func (c *Client) getImage(tag string) (string, bool) {
	m := c.loadImageMap()
	id, ok := m[tag]
	return id, ok
}

func (c *Client) removeImage(tag string) {
	m := c.loadImageMap()
	delete(m, tag)
	c.saveImageMap(m)
}

// --- Backend interface ---

// ImageExists checks if a built VM template exists for the given tag.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	vmID, ok := c.getImage(name)
	if !ok {
		return false, nil
	}
	// Verify the VM still exists
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/vms/"+vmID, nil)
	if err != nil {
		return false, nil
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.removeImage(name)
		return false, nil
	}
	return resp.StatusCode == http.StatusOK, nil
}

// Build creates a freestyle VM and runs Dockerfile commands to set it up.
// The VM is stopped afterward and stored as a template for future Run calls.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	tag := opts.Tag
	if tag == "" {
		tag = opts.Target
	}

	// Remove any existing template VM for this tag
	if oldID, ok := c.getImage(tag); ok {
		c.deleteVM(ctx, oldID)
		c.removeImage(tag)
	}

	// Create a new VM
	c.logf("  → Creating freestyle VM...\n")
	timeout := intPtr(3600) // 1 hour idle timeout during build
	var vm vmInfo
	err := c.doJSON(ctx, http.MethodPost, "/v1/vms", &createVMRequest{
		IdleTimeoutSeconds: timeout,
		Persistence:        &vmPersistence{Type: "persistent"},
	}, &vm)
	if err != nil {
		return "", fmt.Errorf("failed to create freestyle VM: %w", err)
	}
	c.logf("  → VM created: %s\n", vm.ID)

	// Wait for the VM to be ready
	c.logf("  → Waiting for VM to be ready...\n")
	if err := c.waitForReady(ctx, vm.ID, 5*time.Minute); err != nil {
		c.deleteVM(ctx, vm.ID)
		return "", err
	}

	// Parse Dockerfile and execute RUN commands
	c.logf("  → Running setup commands...\n")
	commands := parseDockerfileCommands(opts.Dockerfile, opts.Target, opts.BuildArgs)
	for i, cmd := range commands {
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("[%d/%d] %s\n", i+1, len(commands), truncate(cmd, 120)))
		}
		result, err := c.execAwait(ctx, vm.ID, cmd)
		if err != nil {
			c.deleteVM(ctx, vm.ID)
			return "", fmt.Errorf("build command failed: %w", err)
		}
		if result.ExitCode != 0 {
			detail := result.Stderr
			if detail == "" {
				detail = result.Stdout
			}
			c.deleteVM(ctx, vm.ID)
			return "", fmt.Errorf("build command exited %d: %s\n%s", result.ExitCode, truncate(cmd, 200), truncate(detail, 500))
		}
	}

	// Stop the VM to preserve it as a template
	c.logf("  → Stopping template VM...\n")
	c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/vms/%s/stop", vm.ID), nil, nil)

	// Store the tag→VM mapping
	if err := c.setImage(tag, vm.ID); err != nil {
		return "", fmt.Errorf("failed to save image mapping: %w", err)
	}

	return tag, nil
}

// Run forks the template VM, syncs files, and connects interactively.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	if opts.NoTTY {
		return fmt.Errorf("--no-tty is not supported with the freestyle backend")
	}

	// Look up the template VM
	templateID, ok := c.getImage(opts.Image)
	if !ok {
		return fmt.Errorf("freestyle image %q not found (run build first)", opts.Image)
	}

	// Start the template VM if it's stopped (fork requires a running VM)
	c.logf("  → Starting template VM...\n")
	c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/vms/%s/start", templateID), &startVMRequest{
		IdleTimeoutSeconds: intPtr(300),
	}, nil)

	if err := c.waitForReady(ctx, templateID, 5*time.Minute); err != nil {
		return fmt.Errorf("template VM failed to start: %w", err)
	}

	// Fork the template to create a working VM
	c.logf("  → Forking VM...\n")
	var workVM vmInfo
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/vms/%s/fork", templateID), &forkVMRequest{
		IdleTimeoutSeconds: intPtr(3600),
		Persistence:        &vmPersistence{Type: "ephemeral"},
	}, &workVM)
	if err != nil {
		return fmt.Errorf("failed to fork VM: %w", err)
	}
	c.logf("  → Work VM created: %s\n", workVM.ID)

	// Stop the template again to save resources
	c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/vms/%s/stop", templateID), nil, nil)

	// Set silo metadata on the work VM via exec
	metaCmd := fmt.Sprintf("mkdir -p /etc/silo && printf '%%s' %s > /etc/silo/name && printf '%%s' %s > /etc/silo/image",
		shellquote.Join(opts.Name), shellquote.Join(opts.Image))
	c.execAwait(ctx, workVM.ID, metaCmd)

	// Set environment variables
	if len(opts.Env) > 0 {
		var envLines []string
		for _, e := range opts.Env {
			envLines = append(envLines, fmt.Sprintf("export %s", shellquote.Join(e)))
		}
		envScript := strings.Join(envLines, "\n")
		envCmd := fmt.Sprintf("printf '%%s\\n' %s >> /etc/profile.d/silo-env.sh", shellquote.Join(envScript))
		c.execAwait(ctx, workVM.ID, envCmd)
	}

	// Wait for SSH to be ready
	c.logf("  → Waiting for SSH...\n")
	if err := c.waitForSSH(ctx, workVM.ID, 60*time.Second); err != nil {
		c.deleteVM(ctx, workVM.ID)
		return err
	}

	// Start file sync (mutagen over SSH)
	c.logf("  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, workVM.ID, opts.MountsRO, opts.MountsRW, opts.CleanMountPaths)
	if err != nil {
		c.deleteVM(ctx, workVM.ID)
		return fmt.Errorf("file sync failed: %w", err)
	}
	c.logf("  → Files synced\n")

	// Run pre-run hooks
	hooks := filterMountWait(opts.PreRunHooks)
	if len(hooks) > 0 {
		c.logf("  → Running pre-run hooks...\n")
		hookScript := strings.Join(hooks, " && ")
		if err := c.sshExec(ctx, workVM.ID, hookScript); err != nil {
			stopSync()
			c.deleteVM(ctx, workVM.ID)
			return fmt.Errorf("pre-run hook failed: %w", err)
		}
	}

	// Connect interactively via tmux
	c.logf("  → Connecting...\n")
	connectErr := c.connectInteractive(ctx, workVM.ID, opts)

	// Check if tmux session is still alive (user detached vs tool exited)
	tmuxAlive := c.isTmuxSessionAlive(ctx, workVM.ID)

	// Stop sync (flush final changes)
	c.logf("  → Syncing final changes...\n")
	stopSync()

	if tmuxAlive {
		c.logf("  → Detached. VM %s still running — use 'silo reconnect %s --backend freestyle' to reattach.\n", workVM.ID, opts.Name)
		return nil
	}

	// Tool exited, destroy the work VM
	c.logf("  → Destroying VM...\n")
	destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.deleteVM(destroyCtx, workVM.ID)

	return connectErr
}

// Exec runs a command inside a running Freestyle VM with interactive TTY.
func (c *Client) Exec(ctx context.Context, name string, command []string) error {
	vmID, err := c.resolveVM(ctx, name)
	if err != nil {
		return err
	}

	cmdStr := shellquote.Join(command...)
	return c.sshInteractive(ctx, vmID, cmdStr)
}

// Reconnect re-syncs files and reattaches to the tool's tmux session.
func (c *Client) Reconnect(ctx context.Context, name string, opts backend.RunOptions) error {
	vmID, err := c.resolveVM(ctx, name)
	if err != nil {
		return err
	}

	// Re-sync files
	c.logf("  → Syncing files...\n")
	stopSync, err := c.startMutagenSync(ctx, vmID, opts.MountsRO, opts.MountsRW, nil)
	if err != nil {
		return fmt.Errorf("file sync failed: %w", err)
	}
	c.logf("  → Files synced\n")

	// Reattach to tmux
	c.logf("  → Reconnecting...\n")
	connectErr := c.sshInteractive(ctx, vmID,
		"export LANG=C.UTF-8 LC_ALL=C.UTF-8; tmux -u attach-session -t silo")

	tmuxAlive := c.isTmuxSessionAlive(ctx, vmID)

	c.logf("  → Syncing final changes...\n")
	stopSync()

	if tmuxAlive {
		c.logf("  → Detached. VM %s still running — use 'silo reconnect %s --backend freestyle' to reattach.\n", vmID, name)
		return nil
	}

	// Tool exited, destroy VM
	c.logf("  → Destroying VM...\n")
	destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.deleteVM(destroyCtx, vmID)

	return connectErr
}

// List returns all silo-created Freestyle VMs.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	var vms []vmInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/vms", nil, &vms); err != nil {
		return nil, err
	}

	var result []backend.ContainerInfo
	for _, vm := range vms {
		name := c.siloName(ctx, vm.ID)
		if name == "" {
			continue // not a silo VM
		}
		image := c.siloImage(ctx, vm.ID)
		isRunning := vm.State == "started" || vm.State == "running"
		result = append(result, backend.ContainerInfo{
			Name:      name,
			Image:     image,
			Status:    vm.State,
			IsRunning: isRunning,
		})
	}
	return result, nil
}

// Remove destroys specific VMs by silo name.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	var vms []vmInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/vms", nil, &vms); err != nil {
		return nil, err
	}

	toRemove := make(map[string]bool)
	for _, name := range names {
		toRemove[name] = true
	}

	var removed []string
	for _, vm := range vms {
		name := c.siloName(ctx, vm.ID)
		if !toRemove[name] {
			continue
		}
		if err := c.deleteVM(ctx, vm.ID); err != nil {
			return removed, fmt.Errorf("failed to remove VM %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// NextContainerName returns the next sequential name for the given base name.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	var vms []vmInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/vms", nil, &vms); err != nil {
		return fmt.Sprintf("%s-1", baseName)
	}

	maxNum := 0
	prefix := baseName + "-"
	for _, vm := range vms {
		name := c.siloName(ctx, vm.ID)
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

func (c *Client) deleteVM(ctx context.Context, vmID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/vms/"+vmID, nil, nil)
}

func (c *Client) execAwait(ctx context.Context, vmID, command string) (*execAwaitResponse, error) {
	var result execAwaitResponse
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v1/vms/%s/exec-await", vmID), &execAwaitRequest{
		Command: command,
	}, &result)
	return &result, err
}

func (c *Client) waitForReady(ctx context.Context, vmID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var vm vmInfo
		if err := c.doJSON(ctx, http.MethodGet, "/v1/vms/"+vmID, nil, &vm); err == nil {
			if vm.State == "started" || vm.State == "running" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("VM %s not ready after %s", vmID, timeout)
}

func (c *Client) waitForSSH(ctx context.Context, vmID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.CommandContext(ctx, "ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=5",
			"-o", "LogLevel=ERROR",
			fmt.Sprintf("%s:%s@%s", vmID, c.token, sshHost),
			"true",
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

func (c *Client) resolveVM(ctx context.Context, name string) (string, error) {
	var vms []vmInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/vms", nil, &vms); err != nil {
		return "", err
	}
	for _, vm := range vms {
		if c.siloName(ctx, vm.ID) == name {
			if vm.State != "started" && vm.State != "running" {
				return "", fmt.Errorf("VM %s is not running (state: %s)", name, vm.State)
			}
			return vm.ID, nil
		}
	}
	return "", fmt.Errorf("container %s not found", name)
}

// siloName reads the silo name from a VM's /etc/silo/name file.
func (c *Client) siloName(ctx context.Context, vmID string) string {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/vms/%s/files/etc/silo/name", vmID), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()

	var fileResp struct {
		Content string `json:"content"`
	}
	body, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(body, &fileResp) == nil && fileResp.Content != "" {
		return fileResp.Content
	}
	// Fall back to raw body if not JSON-wrapped
	return strings.TrimSpace(string(body))
}

// siloImage reads the silo image tag from a VM's /etc/silo/image file.
func (c *Client) siloImage(ctx context.Context, vmID string) string {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/vms/%s/files/etc/silo/image", vmID), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()

	var fileResp struct {
		Content string `json:"content"`
	}
	body, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(body, &fileResp) == nil && fileResp.Content != "" {
		return fileResp.Content
	}
	return strings.TrimSpace(string(body))
}

// sshExec runs a non-interactive SSH command.
func (c *Client) sshExec(ctx context.Context, vmID, script string) error {
	u := currentUsername()
	if u == "" {
		u = "root"
	}
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		fmt.Sprintf("%s+%s:%s@%s", vmID, u, c.token, sshHost),
		fmt.Sprintf("bash -l -c %s", shellquote.Join(script)),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// sshExecAsRoot runs a non-interactive SSH command as root.
func (c *Client) sshExecAsRoot(ctx context.Context, vmID, script string) error {
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		fmt.Sprintf("%s:%s@%s", vmID, c.token, sshHost),
		fmt.Sprintf("bash -l -c %s", shellquote.Join(script)),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// sshInteractive runs an interactive SSH session.
func (c *Client) sshInteractive(ctx context.Context, vmID, remoteCmd string) error {
	u := currentUsername()
	if u == "" {
		u = "root"
	}
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-t", // force PTY allocation
		fmt.Sprintf("%s+%s:%s@%s", vmID, u, c.token, sshHost),
		fmt.Sprintf("bash -c %s", shellquote.Join(remoteCmd)),
	}

	cmd := exec.CommandContext(ctx, "ssh", args...)
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

func (c *Client) connectInteractive(ctx context.Context, vmID string, opts backend.RunOptions) error {
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

	// Write tmux config
	tmuxConf := `set -g status off
set -g mouse on
set -g default-terminal "tmux-256color"
`
	_ = c.sshExec(ctx, vmID, fmt.Sprintf("printf %%s %s > /tmp/.silo-tmux.conf",
		shellquote.Join(tmuxConf)))

	// Launch tool inside tmux
	tmuxCmd := fmt.Sprintf(
		"export LANG=C.UTF-8 LC_ALL=C.UTF-8; "+
			"if tmux has-session -t silo 2>/dev/null; then "+
			"tmux attach-session -t silo; "+
			"else "+
			"tmux -u -f /tmp/.silo-tmux.conf new-session -s silo -e LANG=C.UTF-8 -e LC_ALL=C.UTF-8 %s; "+
			"fi",
		shellquote.Join("bash", "-l", "-c", innerCmd),
	)

	return c.sshInteractive(ctx, vmID, tmuxCmd)
}

func (c *Client) isTmuxSessionAlive(ctx context.Context, vmID string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.sshExec(checkCtx, vmID, "tmux has-session -t silo")
	return err == nil
}

// freestyleSshDir creates a directory containing an SSH wrapper script for mutagen.
func (c *Client) freestyleSshDir(vmID string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "silo-freestyle-sshdir-*")
	if err != nil {
		return "", nil, err
	}

	u := currentUsername()
	if u == "" {
		u = "root"
	}

	// Write SSH config
	sshConfig := fmt.Sprintf(`Host freestyle
  HostName %s
  User %s+%s:%s
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  ServerAliveInterval 5
  ServerAliveCountMax 3
  LogLevel ERROR
`, sshHost, vmID, u, c.token)

	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte(sshConfig), 0600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}

	// Write SSH wrapper script for mutagen
	script := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift; shift ;;
    -o*) shift ;;
    -*) shift ;;
    *) shift; break ;;
  esac
done
exec ssh -F %s freestyle "$@"
`, configPath)

	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}

	return dir, func() { os.RemoveAll(dir) }, nil
}

// startMutagenSync starts continuous file sync using mutagen over SSH.
func (c *Client) startMutagenSync(ctx context.Context, vmID string, mountsRO, mountsRW, cleanPaths []string) (cleanup func(), err error) {
	if len(mountsRO) == 0 && len(mountsRW) == 0 {
		return func() {}, nil
	}

	mutagenPath, err := exec.LookPath("mutagen")
	if err != nil {
		return nil, fmt.Errorf("mutagen is required for the freestyle backend (install from https://mutagen.io): %w", err)
	}

	// Create SSH directory for mutagen
	sshDir, sshCleanup, err := c.freestyleSshDir(vmID)
	if err != nil {
		return nil, err
	}

	// Create a dedicated mutagen data directory
	mutagenDataDir, err := os.MkdirTemp("", "silo-mutagen-data-*")
	if err != nil {
		sshCleanup()
		return nil, err
	}

	mutagenEnv := append(os.Environ(),
		"MUTAGEN_SSH_PATH="+sshDir,
		"MUTAGEN_DATA_DIRECTORY="+mutagenDataDir,
	)

	startCmd := exec.CommandContext(ctx, mutagenPath, "daemon", "start")
	startCmd.Env = mutagenEnv
	if err := startCmd.Run(); err != nil {
		os.RemoveAll(mutagenDataDir)
		sshCleanup()
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

	// Prepare remote directories
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

		u := currentUsername()
		if u != "" {
			var chownPaths []string
			for _, m := range mounts {
				if info, err := os.Stat(m.path); err == nil && info.IsDir() {
					chownPaths = append(chownPaths, shellquote.Join(m.path))
				}
			}
			home := os.Getenv("HOME")
			if home != "" {
				chownPaths = append(chownPaths, shellquote.Join(filepath.Join(home, ".mutagen")))
			}
			if len(chownPaths) > 0 {
				scriptParts = append(scriptParts, fmt.Sprintf("chown -R %s:%s %s", u, u, strings.Join(chownPaths, " ")))
			}
		}

		if err := c.sshExecAsRoot(ctx, vmID, strings.Join(scriptParts, " && ")); err != nil {
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
		stopCmd := exec.Command(mutagenPath, "daemon", "stop")
		stopCmd.Env = mutagenEnv
		stopCmd.Run()
		os.RemoveAll(mutagenDataDir)
		sshCleanup()
	}

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
		} else {
			if target, linkErr := os.Readlink(localPath); linkErr == nil {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(localPath), target)
				}
				localPath = target
			}
		}
		sessions = append(sessions, session{
			name:      fmt.Sprintf("silo-%s-%d", vmID, i),
			localPath: localPath,
			mount:     m,
		})
	}

	// Create sync sessions
	type createResult struct {
		idx int
		err error
	}
	results := make(chan createResult, len(sessions))
	for i, s := range sessions {
		sessionNames = append(sessionNames, s.name)
		go func(idx int, s session) {
			cmd := exec.CommandContext(ctx, mutagenPath, "sync", "create",
				"--name", s.name,
				"--sync-mode", s.mount.mode,
				s.localPath, "freestyle:"+s.mount.path,
			)
			cmd.Env = mutagenEnv
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("mutagen sync create failed for %s: %w\n%s", s.mount.path, err, string(out))
			}
			results <- createResult{idx: idx, err: err}
		}(i, s)
	}
	for range sessions {
		r := <-results
		if r.err != nil {
			cleanupSessions()
			return nil, r.err
		}
	}

	// Flush to wait for initial sync
	flushResults := make(chan createResult, len(sessions))
	for i, s := range sessions {
		go func(idx int, s session) {
			flushCmd := exec.CommandContext(ctx, mutagenPath, "sync", "flush", s.name)
			flushCmd.Env = mutagenEnv
			out, err := flushCmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("mutagen sync flush failed for %s: %w\n%s", s.name, err, string(out))
			}
			flushResults <- createResult{idx: idx, err: err}
		}(i, s)
	}
	for range sessions {
		r := <-flushResults
		if r.err != nil {
			cleanupSessions()
			return nil, r.err
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

// --- Dockerfile parsing ---

// parseDockerfileCommands extracts executable shell commands from a Dockerfile,
// up to and including the specified target stage. It handles FROM, ARG, ENV,
// USER, WORKDIR, RUN, and SHELL directives.
func parseDockerfileCommands(dockerfile, target string, buildArgs map[string]string) []string {
	args := make(map[string]string, len(buildArgs))
	maps.Copy(args, buildArgs)

	expand := func(s string) string {
		for k, v := range args {
			s = strings.ReplaceAll(s, "${"+k+"}", v)
			s = strings.ReplaceAll(s, "$"+k, v)
		}
		return s
	}

	var commands []string
	currentUser := "root"
	workDir := "/"
	inTargetStage := false
	pastBase := false
	shell := []string{"/bin/sh", "-c"}

	// Join continuation lines
	lines := joinContinuationLines(dockerfile)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		directive := strings.ToUpper(parts[0])
		rest := ""
		if len(parts) > 1 {
			rest = strings.TrimSpace(parts[1])
		}

		switch directive {
		case "FROM":
			// Parse "FROM image AS name"
			fields := strings.Fields(rest)
			stageName := ""
			for i, f := range fields {
				if strings.EqualFold(f, "AS") && i+1 < len(fields) {
					stageName = fields[i+1]
					break
				}
			}
			if stageName == target {
				inTargetStage = true
			} else if inTargetStage {
				// We've passed the target stage
				break
			} else if stageName == "base" || stageName == "" {
				inTargetStage = true // include base stage
				pastBase = false
			} else {
				pastBase = true
				inTargetStage = false
			}

		case "ARG":
			// ARG NAME or ARG NAME=default
			argParts := strings.SplitN(rest, "=", 2)
			name := strings.TrimSpace(argParts[0])
			if _, ok := args[name]; !ok && len(argParts) == 2 {
				args[name] = strings.TrimSpace(argParts[1])
			}

		case "ENV":
			if !inTargetStage {
				continue
			}
			expanded := expand(rest)
			// Parse ENV KEY=VALUE or ENV KEY VALUE
			if strings.Contains(expanded, "=") {
				// Could be multiple KEY=VALUE pairs
				envPairs := parseEnvLine(expanded)
				for k, v := range envPairs {
					args[k] = v // make available for future expansion
					commands = append(commands, fmt.Sprintf("export %s=%s", k, shellquote.Join(v)))
				}
			} else {
				kv := strings.SplitN(expanded, " ", 2)
				if len(kv) == 2 {
					args[kv[0]] = kv[1]
					commands = append(commands, fmt.Sprintf("export %s=%s", kv[0], shellquote.Join(kv[1])))
				}
			}

		case "USER":
			if !inTargetStage {
				continue
			}
			currentUser = expand(strings.TrimSpace(rest))

		case "WORKDIR":
			if !inTargetStage {
				continue
			}
			workDir = expand(strings.TrimSpace(rest))
			commands = append(commands, fmt.Sprintf("mkdir -p %s && cd %s", shellquote.Join(workDir), shellquote.Join(workDir)))

		case "RUN":
			if !inTargetStage {
				continue
			}
			expanded := expand(rest)
			// Build the command with proper user and workdir context
			var cmd string
			if workDir != "/" && workDir != "" {
				cmd = fmt.Sprintf("cd %s && ", shellquote.Join(workDir))
			}
			if currentUser != "root" {
				cmd = fmt.Sprintf("su -l %s -c %s", currentUser, shellquote.Join(cmd+expanded))
			} else {
				cmd = cmd + expanded
			}
			commands = append(commands, cmd)

		case "SHELL":
			if !inTargetStage {
				continue
			}
			// Parse JSON array
			var newShell []string
			if err := json.Unmarshal([]byte(rest), &newShell); err == nil {
				shell = newShell
			}

		case "COPY", "ADD":
			// Skip COPY/ADD - files will be synced via mutagen
			continue
		}
		_ = shell
		_ = pastBase
	}

	return commands
}

// joinContinuationLines joins lines ending with backslash.
func joinContinuationLines(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var result []string
	var current strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t")
		if withoutBackslash, ok := strings.CutSuffix(trimmed, "\\"); ok {
			current.WriteString(withoutBackslash)
			current.WriteString(" ")
		} else {
			current.WriteString(line)
			result = append(result, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

// parseEnvLine parses "KEY1=val1 KEY2=val2" or "KEY1=\"val with spaces\" KEY2=val2"
func parseEnvLine(line string) map[string]string {
	result := make(map[string]string)
	remaining := line
	for remaining != "" {
		remaining = strings.TrimSpace(remaining)
		if remaining == "" {
			break
		}
		eqIdx := strings.Index(remaining, "=")
		if eqIdx < 0 {
			break
		}
		key := remaining[:eqIdx]
		remaining = remaining[eqIdx+1:]

		var value string
		if strings.HasPrefix(remaining, "\"") {
			// Quoted value
			end := strings.Index(remaining[1:], "\"")
			if end >= 0 {
				value = remaining[1 : end+1]
				remaining = remaining[end+2:]
			} else {
				value = remaining[1:]
				remaining = ""
			}
		} else {
			// Unquoted value - ends at next space or end
			spIdx := strings.IndexByte(remaining, ' ')
			if spIdx >= 0 {
				value = remaining[:spIdx]
				remaining = remaining[spIdx:]
			} else {
				value = remaining
				remaining = ""
			}
		}
		result[key] = value
	}
	return result
}

// filterMountWait removes the mount wait hook from pre-run hooks.
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

func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func intPtr(i int) *int {
	return &i
}

// Ensure Client implements backend.Backend at compile time.
var _ backend.Backend = (*Client)(nil)
