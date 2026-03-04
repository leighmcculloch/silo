package tools

import (
	_ "embed"

	"github.com/leighmcculloch/silo/config"
)

//go:embed claudecode/Dockerfile
var claudeDockerfile string

//go:embed opencode/Dockerfile
var opencodeDockerfile string

//go:embed copilotcli/Dockerfile
var copilotDockerfile string

// DefaultToolDefs returns the built-in default tool definitions. These are
// the starting point for the tools map in config — users can override any
// field or add entirely new tools via their silo.jsonc config files.
func DefaultToolDefs() map[string]config.ToolConfig {
	return map[string]config.ToolConfig{
		"claude": {
			Description:      "Claude Code - Anthropic's CLI for Claude",
			Dockerfile:       claudeDockerfile,
			Command:          []string{"$HOME/.local/bin/claude", "--mcp-config=$HOME/.claude/mcp.json", "--dangerously-skip-permissions"},
			LatestVersionURL: "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/latest",
			MountsRW: []string{
				"~/.claude.json",
				"~/.claude",
			},
		},
		"opencode": {
			Description: "OpenCode - AI coding assistant",
			Dockerfile:  opencodeDockerfile,
			Command:     []string{"$HOME/.opencode/bin/opencode"},
			MountsRW: []string{
				"~/.config/opencode",
				"~/.local/share/opencode",
				"~/.local/state/opencode",
			},
			MountsRO: []string{
				"~/.claude",
			},
			Env: []string{
				"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
			},
		},
		"copilot": {
			Description:                "GitHub Copilot CLI",
			Dockerfile:                 copilotDockerfile,
			Command:                    []string{"$HOME/.local/bin/copilot", "--allow-all", "--disable-builtin-mcps"},
			LatestVersionGitHubRelease: "github/copilot-cli",
			MountsRW: []string{
				"~/.config/.copilot",
			},
			MountsRO: []string{
				"~/.claude",
			},
			Env: []string{
				"COPILOT_GITHUB_TOKEN",
			},
		},
	}
}
