package backend

import (
	"context"
	"io"
)

// Backend defines the interface for running silo tools in an isolated environment
type Backend interface {
	// Name returns the name of the backend (e.g., "docker", "avf")
	Name() string

	// Build builds the environment (image, VM, etc.) for the given tool
	Build(ctx context.Context, opts BuildOptions) error

	// Run runs the tool in the isolated environment
	Run(ctx context.Context, opts RunOptions) error

	// Close cleans up the backend resources
	Close() error
}

// BuildOptions contains options for building the environment
type BuildOptions struct {
	// Dockerfile is the Dockerfile content to build from
	Dockerfile string

	// Target is the build target (e.g., "claude", "opencode", "copilot")
	Target string

	// BuildArgs are build-time arguments
	BuildArgs map[string]string

	// OnProgress is called with progress messages during the build
	OnProgress func(string)
}

// RunOptions contains options for running a tool
type RunOptions struct {
	// Tool is the name of the tool to run (e.g., "claude")
	Tool string

	// Name is the name for this run instance
	Name string

	// WorkDir is the working directory inside the container/VM
	WorkDir string

	// MountsRO are read-only bind mounts (host paths mounted at the same path)
	MountsRO []string

	// MountsRW are read-write bind mounts (host paths mounted at the same path)
	MountsRW []string

	// Env are environment variables (KEY=VALUE format)
	Env []string

	// Command is the base command to run (e.g., ["claude", "--flag"])
	Command []string

	// Args are additional arguments appended to Command
	Args []string

	// Prehooks are shell commands to run before the main command
	Prehooks []string

	// Stdin is the input stream
	Stdin io.Reader

	// Stdout is the output stream
	Stdout io.Writer

	// Stderr is the error output stream
	Stderr io.Writer

	// TTY enables terminal emulation
	TTY bool

	// RemoveOnExit removes the container/VM after it exits
	RemoveOnExit bool

	// SecurityOptions are backend-specific security options
	SecurityOptions []string
}
