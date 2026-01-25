package lima

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"text/template"

	"github.com/adrg/xdg"
	"github.com/leighmcculloch/silo/backend"
	"github.com/moby/term"
	"golang.org/x/sys/unix"
)

// getSystemMemoryBytes returns the total system memory in bytes
func getSystemMemoryBytes() (uint64, error) {
	// Use sysctl to get hw.memsize on macOS
	val, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, err
	}
	return val, nil
}

//go:embed template.yaml
var templateYAML string

// Client wraps the Lima CLI (limactl) with silo-specific functionality
type Client struct {
	cacheDir string
	vmName   string
}

// NewClient creates a new Lima client
func NewClient() (*Client, error) {
	// Check if limactl is available
	if _, err := exec.LookPath("limactl"); err != nil {
		return nil, fmt.Errorf("limactl not found in PATH: %w (install with 'brew install lima')", err)
	}

	cacheDir := filepath.Join(xdg.CacheHome, "silo", "lima")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &Client{
		cacheDir: cacheDir,
	}, nil
}

// Close stops the VM if running (but preserves the cached image)
func (c *Client) Close() error {
	if c.vmName == "" {
		return nil
	}

	// Check if VM is running
	cmd := exec.Command("limactl", "list", "--format", "{{.Status}}", c.vmName)
	out, err := cmd.Output()
	if err != nil {
		// VM doesn't exist, nothing to stop
		return nil
	}

	status := strings.TrimSpace(string(out))
	if status == "Running" {
		// Stop the VM
		stopCmd := exec.Command("limactl", "stop", c.vmName)
		if err := stopCmd.Run(); err != nil {
			return fmt.Errorf("failed to stop VM: %w", err)
		}
	}

	return nil
}

// Build creates or reuses a cached Lima VM with all tools installed
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	// Compute cache key hash (same VM for all tools)
	cacheKey, err := c.computeCacheKey(opts)
	if err != nil {
		return "", fmt.Errorf("failed to compute cache key: %w", err)
	}

	// VM name is based on the hash prefix only (not tool-specific)
	vmName := fmt.Sprintf("silo-%s", cacheKey[:12])
	c.vmName = vmName

	// Check if VM already exists
	if c.vmExists(vmName) {
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("Using cached VM: %s\n", vmName))
		}
		return vmName, nil
	}

	// Generate Lima YAML config
	yamlConfig, err := c.generateConfig(opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate Lima config: %w", err)
	}

	// Write config to cache directory
	configPath := filepath.Join(c.cacheDir, vmName+".yaml")
	if err := os.WriteFile(configPath, []byte(yamlConfig), 0644); err != nil {
		return "", fmt.Errorf("failed to write config: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Creating VM: %s (this may take a while on first run)...\n", vmName))
	}

	// Create the VM (--tty=false avoids interactive prompts)
	createCmd := exec.CommandContext(ctx, "limactl", "create", "--tty=false", "--name", vmName, configPath)
	if opts.OnProgress != nil {
		createCmd.Stdout = &progressWriter{onProgress: opts.OnProgress}
		createCmd.Stderr = &progressWriter{onProgress: opts.OnProgress}
	}
	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create VM: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Starting VM: %s...\n", vmName))
	}

	// Start the VM (this runs provisioning scripts)
	startCmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", vmName)
	if opts.OnProgress != nil {
		startCmd.Stdout = &progressWriter{onProgress: opts.OnProgress}
		startCmd.Stderr = &progressWriter{onProgress: opts.OnProgress}
	}
	if err := startCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to start VM: %w", err)
	}

	// Store cache key for verification
	keyPath := filepath.Join(c.cacheDir, vmName+".key")
	if err := os.WriteFile(keyPath, []byte(cacheKey), 0644); err != nil {
		// Non-fatal, just log
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("Warning: failed to write cache key: %v\n", err))
		}
	}

	return vmName, nil
}

// Run executes a command in the Lima VM
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	if c.vmName == "" {
		return fmt.Errorf("no VM created, call Build first")
	}

	// Ensure VM is running
	if err := c.ensureRunning(ctx); err != nil {
		return err
	}

	// Build the shell script
	var scriptParts []string

	// Export environment variables
	for _, e := range opts.Env {
		scriptParts = append(scriptParts, fmt.Sprintf("export %q", e))
	}

	// Add prehooks if any
	for _, hook := range opts.Prehooks {
		scriptParts = append(scriptParts, hook)
	}

	// Add working directory
	if opts.WorkDir != "" {
		scriptParts = append(scriptParts, fmt.Sprintf("cd %q", opts.WorkDir))
	}

	// Add the main command with exec
	fullCmd := append(opts.Command, opts.Args...)
	scriptParts = append(scriptParts, "exec "+strings.Join(fullCmd, " "))

	shellScript := strings.Join(scriptParts, " && ")

	// Build limactl shell command
	// Use bash -l -c to get a login shell that sources .bashrc
	args := []string{"shell", c.vmName, "bash", "-l", "-c", shellScript}

	cmd := exec.CommandContext(ctx, "limactl", args...)

	// Handle TTY mode
	if opts.TTY {
		cmd.Stdin = opts.Stdin
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr

		// Set terminal to raw mode if input is a terminal
		if f, ok := opts.Stdin.(*os.File); ok {
			fd := f.Fd()
			if term.IsTerminal(fd) {
				oldState, err := term.MakeRaw(fd)
				if err != nil {
					return fmt.Errorf("failed to set raw terminal: %w", err)
				}
				defer term.RestoreTerminal(fd, oldState)

				// Handle terminal resize signals
				go c.monitorTTYSize(ctx, fd)
			}
		}
	} else {
		cmd.Stdin = opts.Stdin
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr
	}

	// Run the command
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("command exited with status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("failed to run command: %w", err)
	}

	return nil
}

// computeCacheKey generates a hash from the silo binary, template, and build args
// The cache key is NOT tool-specific since all tools are installed in one VM
func (c *Client) computeCacheKey(opts backend.BuildOptions) (string, error) {
	h := sha256.New()

	// Hash the silo binary itself
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	exeData, err := os.ReadFile(exePath)
	if err != nil {
		return "", fmt.Errorf("failed to read executable: %w", err)
	}
	h.Write(exeData)

	// Hash the template (contains all provisioning logic)
	h.Write([]byte(templateYAML))

	// Hash build args (USER, UID, HOME) in sorted order for consistency
	var keys []string
	for k := range opts.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte(opts.BuildArgs[k]))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// vmExists checks if a VM with the given name exists
func (c *Client) vmExists(name string) bool {
	cmd := exec.Command("limactl", "list", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// ensureRunning ensures the VM is in a running state
func (c *Client) ensureRunning(ctx context.Context) error {
	cmd := exec.Command("limactl", "list", "--format", "{{.Status}}", c.vmName)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check VM status: %w", err)
	}

	status := strings.TrimSpace(string(out))
	if status == "Running" {
		return nil
	}

	// Start the VM
	startCmd := exec.CommandContext(ctx, "limactl", "start", c.vmName)
	startCmd.Stdout = os.Stderr
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	return nil
}

// templateData contains data for the Lima YAML template
type templateData struct {
	CPUs   int
	Memory string // e.g., "8GiB"
	User   string // username
	UID    string // user ID
	Home   string // home directory path (e.g., "/Users/username")
}

// generateConfig generates a Lima YAML config with mounts and settings
func (c *Client) generateConfig(opts backend.BuildOptions) (string, error) {
	// Get host resources: 100% of CPUs
	cpus := runtime.NumCPU()

	// Get total memory and calculate 50%
	memoryGiB := 8 // default fallback
	if totalBytes, err := getSystemMemoryBytes(); err == nil {
		memoryGiB = int(totalBytes / 2 / (1024 * 1024 * 1024)) // 50% in GiB
		if memoryGiB < 4 {
			memoryGiB = 4 // minimum 4GiB
		}
	}

	data := templateData{
		CPUs:   cpus,
		Memory: fmt.Sprintf("%dGiB", memoryGiB),
		User:   opts.BuildArgs["USER"],
		UID:    opts.BuildArgs["UID"],
		Home:   opts.BuildArgs["HOME"],
	}

	// Parse the template
	tmpl, err := template.New("lima").Parse(templateYAML)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute template with data
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// monitorTTYSize monitors for terminal resize signals
func (c *Client) monitorTTYSize(ctx context.Context, fd uintptr) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGWINCH)
	defer signal.Stop(sigchan)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigchan:
			// Terminal resize is handled by limactl/ssh automatically
			// but we keep this for future enhancements
		}
	}
}

// Delete removes a cached VM and its associated files
func (c *Client) Delete(vmName string) error {
	// Stop the VM if running
	stopCmd := exec.Command("limactl", "stop", vmName)
	stopCmd.Run() // Ignore error, VM might not be running

	// Delete the VM
	deleteCmd := exec.Command("limactl", "delete", vmName)
	if err := deleteCmd.Run(); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	// Remove cached files
	os.Remove(filepath.Join(c.cacheDir, vmName+".yaml"))
	os.Remove(filepath.Join(c.cacheDir, vmName+".key"))

	return nil
}

// ListVMs returns a list of silo-managed VMs
func (c *Client) ListVMs() ([]string, error) {
	cmd := exec.Command("limactl", "list", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	var vms []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "silo-") {
			vms = append(vms, name)
		}
	}
	return vms, nil
}

// progressWriter wraps an OnProgress callback as an io.Writer
type progressWriter struct {
	onProgress func(string)
}

func (w *progressWriter) Write(p []byte) (n int, err error) {
	w.onProgress(string(p))
	return len(p), nil
}

// Compile-time interface check
var _ backend.Backend = (*Client)(nil)
