package claudecode

import (
	_ "embed"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the Claude Code tool definition.
var Tool = tools.Tool{
	Name:            "claude",
	Description:     "Claude Code - Anthropic's CLI for Claude",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{"claude", "--mcp-config=" + home + "/.claude/mcp.json", "--dangerously-skip-permissions"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.claude.json",
				"~/.claude",
			},
		}
	},
	VersionURL: "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/latest",
}
