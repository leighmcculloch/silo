package claudecode

import (
	_ "embed"
	"fmt"

	"github.com/kballard/go-shellquote"
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
		claudePath := home + "/.local/bin/claude"
		mcpPath := home + "/.claude/mcp.json"
		script := fmt.Sprintf(
			"if [ -f %s ]; then exec %s --mcp-config=%s --dangerously-skip-permissions; else exec %s --dangerously-skip-permissions; fi",
			shellquote.Join(mcpPath),
			shellquote.Join(claudePath),
			shellquote.Join(mcpPath),
			shellquote.Join(claudePath),
		)
		return []string{"bash", "-lc", script}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.claude.json",
				"~/.claude",
			},
		}
	},
	LatestVersion: tools.FetchURLVersion("https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/latest"),
}
