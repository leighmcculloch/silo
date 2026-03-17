package mistralvibe

import (
	_ "embed"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the Mistral Vibe tool definition.
var Tool = tools.Tool{
	Name:            "vibe",
	Description:     "Mistral Vibe - Mistral's CLI coding assistant",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{home + "/.local/bin/vibe", "--agent", "auto-approve"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.vibe",
			},
			Env: []string{
				"MISTRAL_API_KEY",
			},
		}
	},
}
