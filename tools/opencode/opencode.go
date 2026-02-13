package opencode

import (
	_ "embed"
	"path/filepath"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the OpenCode tool definition.
var Tool = tools.Tool{
	Name:            "opencode",
	Description:     "OpenCode - AI coding assistant",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{"opencode"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				tilde.Path(filepath.Join(config.XDGConfigHomeDir(), "opencode")),
				tilde.Path(filepath.Join(config.XDGDataHomeDir(), "opencode")),
				tilde.Path(filepath.Join(config.XDGStateHomeDir(), "opencode")),
			},
			MountsRO: []string{
				"~/.claude",
			},
			Env: []string{
				"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
			},
		}
	},
}
