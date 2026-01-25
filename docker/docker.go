package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
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

// Client wraps the Docker client with silo-specific functionality
type Client struct {
	cli *client.Client
}

// NewClient creates a new Docker client
func NewClient() (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Close closes the Docker client
func (c *Client) Close() error {
	return c.cli.Close()
}

// Build builds a Docker image and returns the image ID
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	// Create a tar archive with the Dockerfile
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dockerfileContent := []byte(opts.Dockerfile)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfileContent)),
		Mode: 0644,
	}); err != nil {
		return "", fmt.Errorf("failed to write tar header: %w", err)
	}

	if _, err := tw.Write(dockerfileContent); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile to tar: %w", err)
	}

	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("failed to close tar: %w", err)
	}

	// Convert build args
	buildArgs := make(map[string]*string)
	for k, v := range opts.BuildArgs {
		v := v // capture for pointer
		buildArgs[k] = &v
	}

	// Build the image
	resp, err := c.cli.ImageBuild(ctx, &buf, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Target:     opts.Target,
		BuildArgs:  buildArgs,
		Tags:       []string{opts.Target},
		Remove:     true,
	})
	if err != nil {
		return "", fmt.Errorf("failed to build image: %w", err)
	}
	defer resp.Body.Close()

	// Read and parse the build output line by line
	// Docker's build API returns JSON messages with "stream" for output and "error" for errors
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			if msg.Error != "" {
				errMsg := msg.Error
				if msg.ErrorDetail.Message != "" {
					errMsg = msg.ErrorDetail.Message
				}
				return "", fmt.Errorf("build error: %s", errMsg)
			}
			if msg.Stream != "" && opts.OnProgress != nil {
				opts.OnProgress(msg.Stream)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read build output: %w", err)
	}

	return opts.Target, nil
}

// Run runs a container with the given options
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
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
		Image:        opts.Image,
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
	resp, err := c.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Attach to the container
	attachResp, err := c.cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
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
	if err := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
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
				c.resizeContainerTTY(ctx, resp.ID, fd)

				// Handle terminal resize signals
				go c.monitorTTYSize(ctx, resp.ID, fd)
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
	statusCh, errCh := c.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
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
func (c *Client) resizeContainerTTY(ctx context.Context, containerID string, fd uintptr) {
	winsize, err := term.GetWinsize(fd)
	if err != nil {
		return
	}

	c.cli.ContainerResize(ctx, containerID, container.ResizeOptions{
		Height: uint(winsize.Height),
		Width:  uint(winsize.Width),
	})
}

// monitorTTYSize monitors for terminal resize signals and updates the container
func (c *Client) monitorTTYSize(ctx context.Context, containerID string, fd uintptr) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGWINCH)
	defer signal.Stop(sigchan)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigchan:
			c.resizeContainerTTY(ctx, containerID, fd)
		}
	}
}
