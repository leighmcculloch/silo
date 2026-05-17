package shell

import (
	_ "embed"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the shell "tool" definition. Unlike other tools, this installs
// nothing on top of the base image and execs the user's login shell
// directly. Used by the `silo shell` subcommand when invoked without a
// container name.
var Tool = tools.Tool{
	Name:            "shell",
	Description:     "Shell - The user's login shell in a base container with no tool installed",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return DefaultShellCommand()
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{}
	},
}

// DefaultShellCommand returns a command that execs the user's login shell
// as recorded in /etc/passwd. The Dockerfile.base useradd line sets this
// to /bin/bash, but a post-build hook (e.g. `chsh -s /bin/zsh`) can
// change it and `silo shell` will pick up the new shell automatically.
func DefaultShellCommand() []string {
	return []string{"sh", "-c", `exec "$(getent passwd "$(id -u)" | cut -d: -f7)"`}
}
