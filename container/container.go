package container

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/adrg/xdg"
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

	cmd := exec.CommandContext(ctx, "container", args...)
	cmd.Stderr = os.Stderr

	// Stream stdout for progress
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start build: %w", err)
	}

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

// Run runs a container using the container CLI.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	// Build full command: Command + Args
	fullCmd := append(opts.Command, opts.Args...)

	// Determine the entrypoint override and arguments
	var entrypoint string
	var runArgs []string

	if len(fullCmd) > 0 {
		if len(opts.Prehooks) > 0 {
			// Create a bash script that runs prehooks then execs the command
			var script strings.Builder
			for _, hook := range opts.Prehooks {
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
	for _, m := range opts.MountsRO {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.IsDir() {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", m, m))
		} else {
			stagingDir, containerDir, err := stageFileMount(m)
			if err != nil {
				return fmt.Errorf("staging file mount %s: %w", m, err)
			}
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s,readonly", stagingDir, containerDir))
			symlinkCmds = append(symlinkCmds, fmt.Sprintf("mkdir -p %s && ln -sf %s %s",
				shellquote.Join(filepath.Dir(m)),
				shellquote.Join(filepath.Join(containerDir, filepath.Base(m))),
				shellquote.Join(m),
			))
		}
	}
	for _, m := range opts.MountsRW {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.IsDir() {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", m, m))
		} else {
			stagingDir, containerDir, err := stageFileMount(m)
			if err != nil {
				return fmt.Errorf("staging file mount %s: %w", m, err)
			}
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", stagingDir, containerDir))
			symlinkCmds = append(symlinkCmds, fmt.Sprintf("mkdir -p %s && ln -sf %s %s",
				shellquote.Join(filepath.Dir(m)),
				shellquote.Join(filepath.Join(containerDir, filepath.Base(m))),
				shellquote.Join(m),
			))
		}
	}

	// Prepend symlink commands into prehooks so they run before the main command.
	if len(symlinkCmds) > 0 {
		allPrehooks := append(symlinkCmds, opts.Prehooks...)
		// Rebuild entrypoint to include symlink setup.
		if len(fullCmd) > 0 {
			var script strings.Builder
			for _, hook := range allPrehooks {
				script.WriteString(hook)
				script.WriteString(" && ")
			}
			script.WriteString("exec ")
			script.WriteString(shellquote.Join(fullCmd...))
			entrypoint = "/bin/bash"
			runArgs = []string{"-c", script.String()}
		} else {
			// No command — just run the symlink setup.
			var script strings.Builder
			for i, hook := range symlinkCmds {
				if i > 0 {
					script.WriteString(" && ")
				}
				script.WriteString(hook)
			}
			entrypoint = "/bin/bash"
			runArgs = []string{"-c", script.String()}
		}
	}

	if entrypoint != "" {
		args = append(args, "--entrypoint", entrypoint)
	}

	// Image
	args = append(args, opts.Image)

	// Command arguments
	args = append(args, runArgs...)

	cmd := exec.CommandContext(ctx, "container", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Forward signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	go func() {
		select {
		case sig := <-sigCh:
			cmd.Process.Signal(sig)
		case <-ctx.Done():
		}
	}()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("container exited with status %d", exitErr.ExitCode())
		}
		return fmt.Errorf("container error: %w", err)
	}

	return nil
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
