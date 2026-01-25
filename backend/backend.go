package backend

import (
	"context"
	"io"
)

// Backend defines the interface for container/VM backends
type Backend interface {
	// Build prepares an environment for running tools (builds an image or creates a VM)
	Build(ctx context.Context, opts BuildOptions) (string, error)

	// Run executes a command in the prepared environment
	Run(ctx context.Context, opts RunOptions) error

	// Close releases any resources held by the backend
	Close() error
}

// BuildOptions contains options for building/preparing an environment
type BuildOptions struct {
	// Dockerfile content (for Docker) or provisioning script (for Lima)
	Dockerfile string

	// Target specifies which tool to build (e.g., "claude", "opencode", "copilot")
	Target string

	// BuildArgs are variables passed to the build process
	BuildArgs map[string]string

	// OnProgress is called with build progress messages
	OnProgress func(string)
}

// RunOptions contains options for running a command
type RunOptions struct {
	// Image is the built image/VM name to run
	Image string

	// Name is the container/instance name
	Name string

	// WorkDir is the working directory inside the container/VM
	WorkDir string

	// MountsRO are read-only mount paths
	MountsRO []string

	// MountsRW are read-write mount paths
	MountsRW []string

	// Env are environment variables in KEY=VALUE format
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

	// Stderr is the error stream
	Stderr io.Writer

	// TTY enables terminal mode
	TTY bool

	// RemoveOnExit removes the container/instance after exit
	RemoveOnExit bool

	// SecurityOptions are security-related options (Docker-specific)
	SecurityOptions []string
}
