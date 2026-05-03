package claudecode

import (
	_ "embed"
	"os"

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
		mcpConfig := home + "/.claude/mcp.json"
		cmd := []string{home + "/.local/bin/claude"}
		if _, err := os.Stat(mcpConfig); err == nil {
			cmd = append(cmd, "--mcp-config="+mcpConfig)
		}
		return append(cmd, "--dangerously-skip-permissions")
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.claude.json",
				"~/.claude",
			},
			Env: []string{
				"ENABLE_CLAUDEAI_MCP_SERVERS=false",
			},
		}
	},
	LatestVersion: tools.FetchURLVersion("https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/latest"),
}
