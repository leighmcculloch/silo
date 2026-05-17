package shell

import (
	_ "embed"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the shell "tool" definition. Unlike other tools, this installs
// nothing on top of the base image and runs bash directly. Used by the
// `silo shell` subcommand when invoked without a container name.
var Tool = tools.Tool{
	Name:            "shell",
	Description:     "Shell - A bash shell in a base container with no tool installed",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{"/bin/bash"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{}
	},
}
