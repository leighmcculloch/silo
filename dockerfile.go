package main

import (
	_ "embed"
	"fmt"
)

//go:embed Dockerfile
var dockerfile string

//go:embed silo.jsonc.example
var sampleConfig string

// Dockerfile returns the embedded Dockerfile content
func Dockerfile() string {
	return dockerfile
}

// AvailableTools returns the list of available tool targets
func AvailableTools() []string {
	return []string{"opencode", "claude", "copilot"}
}

// ToolDescription returns a description for a tool
func ToolDescription(tool string) string {
	switch tool {
	case "opencode":
		return "OpenCode - AI coding assistant"
	case "claude":
		return "Claude Code - Anthropic's CLI for Claude"
	case "copilot":
		return "GitHub Copilot CLI"
	default:
		return fmt.Sprintf("Unknown tool: %s", tool)
	}
}
