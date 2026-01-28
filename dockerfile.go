package main

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed Dockerfile
var dockerfile string

// Dockerfile returns the embedded Dockerfile content
func Dockerfile() string {
	return dockerfile
}

// DockerfileWithHooks returns the Dockerfile with post-build hooks injected
// globalHooks are injected into the base stage, toolHooks are injected into the specific tool stage
func DockerfileWithHooks(globalHooks []string, tool string, toolHooks []string) string {
	result := dockerfile

	// Inject global hooks at base stage marker
	if len(globalHooks) > 0 {
		var runCmds strings.Builder
		for _, hook := range globalHooks {
			runCmds.WriteString("RUN ")
			runCmds.WriteString(hook)
			runCmds.WriteString("\n")
		}
		result = strings.Replace(result, "# SILO_POST_BUILD_HOOKS\n", runCmds.String()+"# SILO_POST_BUILD_HOOKS\n", 1)
	}

	// Inject tool-specific hooks at tool stage marker
	if len(toolHooks) > 0 {
		toolMarker := fmt.Sprintf("# SILO_POST_BUILD_HOOKS_%s\n", strings.ToUpper(tool))
		var runCmds strings.Builder
		for _, hook := range toolHooks {
			runCmds.WriteString("RUN ")
			runCmds.WriteString(hook)
			runCmds.WriteString("\n")
		}
		result = strings.Replace(result, toolMarker, runCmds.String()+toolMarker, 1)
	}

	return result
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
