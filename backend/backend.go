package backend

import (
	"context"
)

// Backend defines the interface for container/VM backends
type Backend interface {
	// Build prepares an environment for running tools (builds an image or creates a VM)
	Build(ctx context.Context, opts BuildOptions) (string, error)

	// ImageExists returns true if an image with the given name exists locally.
	ImageExists(ctx context.Context, name string) (bool, error)

	// NextContainerName returns the next sequential container name for the given
	// base name. It lists existing containers with the same prefix and returns
	// baseName-N where N is one more than the highest existing suffix.
	NextContainerName(ctx context.Context, baseName string) string

	// Run executes a command in the prepared environment
	Run(ctx context.Context, opts RunOptions) error

	// Destroy removes all silo-created containers
	Destroy(ctx context.Context) ([]string, error)

	// Close releases any resources held by the backend
	Close() error
}

// BuildOptions contains options for building/preparing an environment
type BuildOptions struct {
	// Dockerfile content for building the environment
	Dockerfile string

	// Target specifies which tool to build (e.g., "claude", "opencode", "copilot")
	Target string

	// Tag is the image tag to apply. If empty, Target is used as the tag.
	Tag string

	// BuildArgs are variables passed to the build process
	BuildArgs map[string]string

	// MountsRO are read-only mount paths
	MountsRO []string

	// MountsRW are read-write mount paths
	MountsRW []string

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
}
