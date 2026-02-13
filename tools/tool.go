package tools

import "github.com/leighmcculloch/silo/config"

// Tool defines a self-contained tool that can be run inside a silo container.
type Tool struct {
	Name            string                     // build target / config key (e.g. "claude")
	Description     string                     // human-readable (e.g. "Claude Code - Anthropic's CLI")
	DockerfileStage string                     // Dockerfile fragment (FROM base AS <name> ...)
	Command         func(home string) []string // container entrypoint + args
	DefaultConfig   func() config.ToolConfig   // default mounts/env/hooks
	VersionURL      string                     // optional latest-version URL for cache-busting
}

// DefaultToolConfigs builds the map that config.DefaultConfig needs from a
// slice of tool definitions.
func DefaultToolConfigs(tt []Tool) map[string]config.ToolConfig {
	m := make(map[string]config.ToolConfig, len(tt))
	for _, t := range tt {
		if t.DefaultConfig != nil {
			m[t.Name] = t.DefaultConfig()
		}
	}
	return m
}
