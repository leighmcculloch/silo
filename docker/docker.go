package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/kballard/go-shellquote"
	"github.com/leighmcculloch/silo/backend"
	"github.com/moby/term"
)

// Backend implements the backend.Backend interface using Docker
type Backend struct {
	cli *client.Client
}

// NewBackend creates a new Docker backend
func NewBackend() (*Backend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	return &Backend{cli: cli}, nil
}

// Name returns the backend name
func (b *Backend) Name() string {
	return "docker"
}

// Close closes the Docker client
func (b *Backend) Close() error {
	return b.cli.Close()
}

// Build builds a Docker image
func (b *Backend) Build(ctx context.Context, opts backend.BuildOptions) error {
	// Create a tar archive with the Dockerfile
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dockerfileContent := []byte(opts.Dockerfile)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfileContent)),
		Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}

	if _, err := tw.Write(dockerfileContent); err != nil {
		return fmt.Errorf("failed to write Dockerfile to tar: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("failed to close tar: %w", err)
	}

	// Convert build args
	buildArgs := make(map[string]*string)
	for k, v := range opts.BuildArgs {
		v := v // capture for pointer
		buildArgs[k] = &v
	}

	// Build the image
	resp, err := b.cli.ImageBuild(ctx, &buf, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Target:     opts.Target,
		BuildArgs:  buildArgs,
		Tags:       []string{opts.Target},
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer resp.Body.Close()

	// Read and parse the build output
	output, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read build output: %w", err)
	}

	if opts.OnProgress != nil {
		opts.OnProgress(string(output))
	}

	return nil
}

// Run runs a container with the given options
func (b *Backend) Run(ctx context.Context, opts backend.RunOptions) error {
	// Convert mounts
	var mounts []mount.Mount
	for _, m := range opts.MountsRO {
		// Check if path exists before mounting
		if _, err := os.Stat(m); err != nil {
			continue // Skip non-existent paths
		}
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m,
			Target:   m,
			ReadOnly: true,
		})
	}
	for _, m := range opts.MountsRW {
		// Check if path exists before mounting
		if _, err := os.Stat(m); err != nil {
			continue // Skip non-existent paths
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: m,
			Target: m,
		})
	}

	// Build the entrypoint script if we have prehooks or a command
	var entrypoint []string
	var cmd []string

	if len(opts.Command) > 0 {
		// Build full command: Command + Args
		fullCmd := append(opts.Command, opts.Args...)

		if len(opts.Prehooks) > 0 {
			// Create a bash script that runs prehooks then execs the command
			var script strings.Builder
			for _, hook := range opts.Prehooks {
				script.WriteString(hook)
				script.WriteString(" && ")
			}
			script.WriteString("exec ")
			script.WriteString(shellquote.Join(fullCmd...))
			entrypoint = []string{"/bin/bash", "-c", script.String()}
			cmd = nil
		} else {
			// No prehooks, just run the command directly
			entrypoint = []string{fullCmd[0]}
			if len(fullCmd) > 1 {
				cmd = fullCmd[1:]
			}
		}
	} else {
		// No command specified, use image's default entrypoint
		// Pass args as Cmd (will be appended to entrypoint)
		cmd = opts.Args
	}

	// Create container configuration
	config := &container.Config{
		Image:        opts.Tool,
		WorkingDir:   opts.WorkDir,
		Env:          opts.Env,
		Entrypoint:   entrypoint,
		Cmd:          cmd,
		Tty:          opts.TTY,
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}

	hostConfig := &container.HostConfig{
		Mounts:      mounts,
		AutoRemove:  opts.RemoveOnExit,
		Privileged:  false,
		SecurityOpt: opts.SecurityOptions,
		CapDrop:     []string{"ALL"},
	}

	// Create the container
	resp, err := b.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Attach to the container
	attachResp, err := b.cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("failed to attach to container: %w", err)
	}
	defer attachResp.Close()

	// Start the container
	if err := b.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Set terminal to raw mode if TTY and handle resizing
	if opts.TTY {
		if f, ok := opts.Stdin.(*os.File); ok {
			fd := f.Fd()
			if term.IsTerminal(fd) {
				oldState, err := term.MakeRaw(fd)
				if err != nil {
					return fmt.Errorf("failed to set raw terminal: %w", err)
				}
				defer term.RestoreTerminal(fd, oldState)

				// Set initial terminal size
				b.resizeContainerTTY(ctx, resp.ID, fd)

				// Handle terminal resize signals
				go b.monitorTTYSize(ctx, resp.ID, fd)
			}
		}
	}

	// Copy stdin to container
	if opts.Stdin != nil {
		go func() {
			io.Copy(attachResp.Conn, opts.Stdin)
			attachResp.CloseWrite()
		}()
	}

	// Copy container output to stdout/stderr
	if opts.TTY {
		_, err = io.Copy(opts.Stdout, attachResp.Reader)
	} else {
		_, err = stdcopy.StdCopy(opts.Stdout, opts.Stderr, attachResp.Reader)
	}

	// Wait for the container to finish
	statusCh, errCh := b.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("error waiting for container: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("container exited with status %d", status.StatusCode)
		}
	}

	return nil
}

// resizeContainerTTY resizes the container's TTY to match the terminal size
func (b *Backend) resizeContainerTTY(ctx context.Context, containerID string, fd uintptr) {
	winsize, err := term.GetWinsize(fd)
	if err != nil {
		return
	}

	b.cli.ContainerResize(ctx, containerID, container.ResizeOptions{
		Height: uint(winsize.Height),
		Width:  uint(winsize.Width),
	})
}

// monitorTTYSize monitors for terminal resize signals and updates the container
func (b *Backend) monitorTTYSize(ctx context.Context, containerID string, fd uintptr) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGWINCH)
	defer signal.Stop(sigchan)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigchan:
			b.resizeContainerTTY(ctx, containerID, fd)
		}
	}
}

// GetGitWorktreeRoots returns git worktree common directories for the given directory
func GetGitWorktreeRoots(dir string) ([]string, error) {
	var roots []string
	seen := make(map[string]bool)

	// Check current dir and immediate subdirs
	dirs := []string{dir}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(dir, e.Name()))
			}
		}
	}

	for _, d := range dirs {
		// Check if it's a git worktree
		cmd := exec.Command("git", "-C", d, "rev-parse", "--is-inside-work-tree")
		if err := cmd.Run(); err != nil {
			continue
		}

		// Get git dir
		gitDirCmd := exec.Command("git", "-C", d, "rev-parse", "--git-dir")
		gitDirOut, err := gitDirCmd.Output()
		if err != nil {
			continue
		}
		gitDir := strings.TrimSpace(string(gitDirOut))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(d, gitDir)
		}
		gitDir, _ = filepath.Abs(gitDir)

		// Get git common dir
		gitCommonDirCmd := exec.Command("git", "-C", d, "rev-parse", "--git-common-dir")
		gitCommonDirOut, err := gitCommonDirCmd.Output()
		if err != nil {
			continue
		}
		gitCommonDir := strings.TrimSpace(string(gitCommonDirOut))
		if !filepath.IsAbs(gitCommonDir) {
			gitCommonDir = filepath.Join(d, gitCommonDir)
		}
		gitCommonDir, _ = filepath.Abs(gitCommonDir)

		// If git-dir != git-common-dir, it's a worktree
		if gitDir != gitCommonDir {
			commonRoot := filepath.Dir(gitCommonDir)
			if !seen[commonRoot] {
				seen[commonRoot] = true
				roots = append(roots, commonRoot)
			}
		}
	}

	return roots, nil
}

// GetGitIdentity returns the git user.name and user.email from global config
func GetGitIdentity() (name, email string) {
	nameCmd := exec.Command("git", "config", "--global", "user.name")
	if out, err := nameCmd.Output(); err == nil {
		name = strings.TrimSpace(string(out))
	}

	emailCmd := exec.Command("git", "config", "--global", "user.email")
	if out, err := emailCmd.Output(); err == nil {
		email = strings.TrimSpace(string(out))
	}

	return name, email
}
