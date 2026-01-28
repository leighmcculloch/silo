package lima

import (
	"bytes"
	"context"
	"crypto/rand"
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
	cacheDir     string
	baseVMName   string
	instanceName string
	fileLinks    map[string]string // map of original path -> VM temp path for file mounts
	mounts       []mountEntry      // mounts prepared during Build, applied to clone in Run
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

// Close stops and deletes the instance VM (clone), leaving the base VM intact
func (c *Client) Close() error {
	if c.instanceName == "" {
		return nil
	}

	// Stop the instance VM if running
	stopCmd := exec.Command("limactl", "stop", c.instanceName)
	stopCmd.Run() // Ignore error, VM might not be running

	// Delete the instance VM
	deleteCmd := exec.Command("limactl", "delete", c.instanceName)
	if err := deleteCmd.Run(); err != nil {
		return fmt.Errorf("failed to delete instance VM: %w", err)
	}

	return nil
}

// Build creates or reuses a cached base Lima VM with all tools installed.
// The base VM is provisioned and stopped, ready to be cloned for each Run.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	// Prepare file mounts (hardlinks on host, populate fileLinks for Run)
	mounts, err := c.prepareMounts(opts)
	if err != nil {
		return "", fmt.Errorf("failed to prepare mounts: %w", err)
	}
	c.mounts = mounts

	// Compute cache key hash (excludes mounts, which are per-instance)
	cacheKey, err := c.computeCacheKey(opts)
	if err != nil {
		return "", fmt.Errorf("failed to compute cache key: %w", err)
	}

	// Base VM name
	baseName := fmt.Sprintf("silo-base-%s", cacheKey[:12])
	c.baseVMName = baseName

	// Check if base VM already exists (cached)
	if c.vmExists(baseName) {
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("Using cached base VM: %s\n", baseName))
		}
		// Ensure the base VM is stopped (it may still be running if a previous run was interrupted)
		_ = exec.CommandContext(ctx, "limactl", "stop", baseName).Run()
		return baseName, nil
	}

	// Generate Lima YAML config (no mounts — mounts are per-instance)
	yamlConfig, err := c.generateConfig(opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate Lima config: %w", err)
	}

	// Write config to cache directory
	configPath := filepath.Join(c.cacheDir, baseName+".yaml")
	if err := os.WriteFile(configPath, []byte(yamlConfig), 0644); err != nil {
		return "", fmt.Errorf("failed to write config: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Creating base VM: %s (this may take a while on first run)...\n", baseName))
	}

	// Create the VM (--tty=false avoids interactive prompts)
	createCmd := exec.CommandContext(ctx, "limactl", "create", "--tty=false", "--name", baseName, configPath)
	if opts.OnProgress != nil {
		createCmd.Stdout = &progressWriter{onProgress: opts.OnProgress}
		createCmd.Stderr = &progressWriter{onProgress: opts.OnProgress}
	}
	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create VM: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Provisioning base VM: %s...\n", baseName))
	}

	// Start the VM (runs provisioning scripts)
	startCmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", baseName)
	if opts.OnProgress != nil {
		startCmd.Stdout = &progressWriter{onProgress: opts.OnProgress}
		startCmd.Stderr = &progressWriter{onProgress: opts.OnProgress}
	}
	if err := startCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to start VM: %w", err)
	}

	// Stop the base VM — it will be cloned for each Run
	if opts.OnProgress != nil {
		opts.OnProgress(fmt.Sprintf("Stopping base VM: %s...\n", baseName))
	}
	stopCmd := exec.CommandContext(ctx, "limactl", "stop", baseName)
	if opts.OnProgress != nil {
		stopCmd.Stdout = &progressWriter{onProgress: opts.OnProgress}
		stopCmd.Stderr = &progressWriter{onProgress: opts.OnProgress}
	}
	if err := stopCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to stop base VM: %w", err)
	}

	// Store cache key for verification
	keyPath := filepath.Join(c.cacheDir, baseName+".key")
	if err := os.WriteFile(keyPath, []byte(cacheKey), 0644); err != nil {
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("Warning: failed to write cache key: %v\n", err))
		}
	}

	return baseName, nil
}

// Run clones the base VM, adds mounts, starts the clone, and executes a command in it.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	if c.baseVMName == "" {
		return fmt.Errorf("no base VM created, call Build first")
	}

	// Create instance by cloning the base VM
	instanceName, err := c.createInstance(ctx)
	if err != nil {
		return err
	}

	// Build the shell script
	var scriptParts []string

	// Create symlinks for file mounts (files are mounted to /silo/mounts/... and need symlinks)
	for origPath, vmTempPath := range c.fileLinks {
		parentDir := filepath.Dir(origPath)
		scriptParts = append(scriptParts, fmt.Sprintf("mkdir -p %q", parentDir))
		scriptParts = append(scriptParts, fmt.Sprintf("ln -sf %q %q", vmTempPath, origPath))
	}

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
	args := []string{"shell", instanceName, "bash", "-l", "-c", shellScript}

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

// createInstance clones the base VM, injects mounts into the clone config, and starts it.
func (c *Client) createInstance(ctx context.Context) (string, error) {
	// Generate a unique instance name
	instanceName := fmt.Sprintf("silo-run-%s-%s", c.baseVMName[len("silo-base-"):], randomHex(4))
	c.instanceName = instanceName

	// Clone the base VM
	cloneCmd := exec.CommandContext(ctx, "limactl", "clone", "--tty=false", c.baseVMName, instanceName)
	cloneCmd.Stdout = os.Stderr
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to clone base VM: %w", err)
	}

	// Inject mounts into the clone's lima.yaml config
	if len(c.mounts) > 0 {
		if err := c.addMountsToInstance(instanceName); err != nil {
			return "", fmt.Errorf("failed to add mounts to instance: %w", err)
		}
	}

	// Start the cloned instance
	startCmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", instanceName)
	startCmd.Stdout = os.Stderr
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to start instance VM: %w", err)
	}

	return instanceName, nil
}

// addMountsToInstance appends mount configuration to a cloned instance's lima.yaml
func (c *Client) addMountsToInstance(instanceName string) error {
	configPath := filepath.Join(limaHome(), instanceName, "lima.yaml")

	var mountsYAML strings.Builder
	mountsYAML.WriteString("\nmounts:\n")
	for _, m := range c.mounts {
		mountsYAML.WriteString(fmt.Sprintf("  - location: %q\n", m.Source))
		mountsYAML.WriteString(fmt.Sprintf("    mountPoint: %q\n", m.Target))
		mountsYAML.WriteString(fmt.Sprintf("    writable: %v\n", m.Writable))
	}

	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open instance config: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(mountsYAML.String()); err != nil {
		return fmt.Errorf("failed to write mounts to config: %w", err)
	}

	return nil
}

// limaHome returns the Lima home directory
func limaHome() string {
	if h := os.Getenv("LIMA_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lima")
}

// randomHex returns n random bytes as a hex string
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
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

// mountEntry represents a mount configuration for the Lima template
type mountEntry struct {
	Source   string // path on host
	Target   string // path in VM
	Writable bool
}

// templateData contains data for the Lima YAML template
type templateData struct {
	CPUs   int
	Memory string // e.g., "8GiB"
	User   string // username
	UID    string // user ID
	Home   string // home directory path (e.g., "/Users/username")
}

// mountInfo holds information about a mount and any file links needed
type mountInfo struct {
	Source   string            // path on host to mount
	Target   string            // path in VM to mount at
	Writable bool              // whether writable
	Links    map[string]string // map of original path -> VM path (for files needing symlinks)
}

// prepareFileMounts creates mounts for directories and file hardlinks
// For directories: mount directly to the same path
// For files: each file gets its own mount directory (named by hash) to avoid conflicts
func prepareFileMounts(paths []string, writable bool) ([]mountInfo, error) {
	var mounts []mountInfo

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue // skip non-existent paths
		}

		if info.IsDir() {
			// Directory mounts can be used directly
			mounts = append(mounts, mountInfo{
				Source:   p,
				Target:   p,
				Writable: writable,
			})
		} else {
			// Each file gets its own mount directory named by hash of the original path
			pathHash := fmt.Sprintf("%x", sha256.Sum256([]byte(p)))
			basename := filepath.Base(p)

			// Create temp directory on host using XDG state
			tempDir := filepath.Join(xdg.StateHome, "silo", "mounts", pathHash)
			if err := os.MkdirAll(tempDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create temp mount dir: %w", err)
			}

			// Create hardlink in the temp directory
			linkPath := filepath.Join(tempDir, basename)
			os.Remove(linkPath) // Remove existing link if present
			if err := os.Link(p, linkPath); err != nil {
				return nil, fmt.Errorf("failed to hardlink %s: %w", p, err)
			}

			// Track: original path -> path in mounted temp dir
			vmMountDir := filepath.Join("/silo/mounts", pathHash)
			vmFilePath := filepath.Join(vmMountDir, basename)

			mounts = append(mounts, mountInfo{
				Source:   tempDir,
				Target:   vmMountDir,
				Writable: writable,
				Links:    map[string]string{p: vmFilePath},
			})
		}
	}

	return mounts, nil
}

// prepareMounts creates hardlinks for file mounts and populates fileLinks for Run()
// Returns mount entries for the Lima config
func (c *Client) prepareMounts(opts backend.BuildOptions) ([]mountEntry, error) {
	var mounts []mountEntry
	c.fileLinks = make(map[string]string)

	mountInfosRO, err := prepareFileMounts(opts.MountsRO, false)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare read-only mounts: %w", err)
	}
	for _, mi := range mountInfosRO {
		mounts = append(mounts, mountEntry{Source: mi.Source, Target: mi.Target, Writable: mi.Writable})
		for origPath, vmTempPath := range mi.Links {
			c.fileLinks[origPath] = vmTempPath
		}
	}

	mountInfosRW, err := prepareFileMounts(opts.MountsRW, true)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare read-write mounts: %w", err)
	}
	for _, mi := range mountInfosRW {
		mounts = append(mounts, mountEntry{Source: mi.Source, Target: mi.Target, Writable: mi.Writable})
		for origPath, vmTempPath := range mi.Links {
			c.fileLinks[origPath] = vmTempPath
		}
	}

	return mounts, nil
}

// generateConfig generates a Lima YAML config for the base VM (no mounts)
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
