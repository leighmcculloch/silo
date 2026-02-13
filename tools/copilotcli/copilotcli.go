package copilotcli

import (
	_ "embed"
	"path/filepath"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the GitHub Copilot CLI tool definition.
var Tool = tools.Tool{
	Name:            "copilot",
	Description:     "GitHub Copilot CLI",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{"copilot", "--allow-all", "--disable-builtin-mcps"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				tilde.Path(filepath.Join(config.XDGConfigHomeDir(), ".copilot")),
			},
			MountsRO: []string{
				"~/.claude",
			},
			Env: []string{
				"COPILOT_GITHUB_TOKEN",
			},
		}
	},
}
