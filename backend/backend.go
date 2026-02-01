package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
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

	// List returns all silo-created containers
	List(ctx context.Context) ([]ContainerInfo, error)

	// Remove removes specific containers by name
	Remove(ctx context.Context, names []string) ([]string, error)

	// Close releases any resources held by the backend
	Close() error
}

// ContainerInfo holds information about a container
type ContainerInfo struct {
	Name   string
	Image  string
	Status string
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

	// PreRunHooks are shell commands to run before the main command
	PreRunHooks []string
}

// GenerateMountWaitScript generates a bash script that waits for all mount paths to exist.
// It polls each path at 1ms intervals for up to 10s total timeout, with logging.
// This should be prepended to pre-run hooks to ensure mounts are ready before other commands run.
func GenerateMountWaitScript(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var quotedPaths []string
	for _, p := range paths {
		quotedPaths = append(quotedPaths, shellquote.Join(p))
	}
	// ANSI color codes matching cli/cli.go styles:
	// - Info (==>) color 86, Success (✓) color 82, Error (✗) color 196
	return fmt.Sprintf(`__silo_tilde() { case "$1" in "$HOME"*) echo "~${1#$HOME}";; *) echo "$1";; esac; }
__silo_wait_for_mount() {
  local p=$1 timeout=10000 i=0
  local c_success=$'\033[38;5;82m' c_error=$'\033[38;5;196m' c_reset=$'\033[0m'
  local display=$(__silo_tilde "$p")
  if [ -e "$p" ]; then
    printf "  ${c_success}✓${c_reset} %%s\n" "$display" >&2
    return 0
  fi
  while [ ! -e "$p" ] && [ $i -lt $timeout ]; do
    sleep 0.001
    i=$((i+1))
  done
  if [ -e "$p" ]; then
    printf "  ${c_success}✓${c_reset} %%s (${i}ms)\n" "$display" >&2
    return 0
  fi
  printf "  ${c_error}✗${c_reset} %%s (timed out)\n" "$display" >&2
  return 1
}
__silo_wait_for_mounts() {
  local paths=(%s)
  local pids=() p
  local c_info=$'\033[38;5;86m' c_success=$'\033[38;5;82m' c_reset=$'\033[0m'
  printf "${c_info}==> Waiting for ${#paths[@]} mount(s)...${c_reset}\n" >&2
  for p in "${paths[@]}"; do
    __silo_wait_for_mount "$p" &
    pids+=($!)
  done
  local failed=0
  for pid in "${pids[@]}"; do
    wait $pid || failed=1
  done
  if [ $failed -eq 1 ]; then
    exit 1
  fi
  printf "  ${c_success}✓ All mounts ready${c_reset}\n" >&2
}; __silo_wait_for_mounts`, strings.Join(quotedPaths, " "))
}
