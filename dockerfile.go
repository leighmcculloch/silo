package main

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile.base
var dockerfileBase string

//go:embed silo.jsonc.example
var sampleConfig string

// Dockerfile returns the composed Dockerfile: base stage + all tool stages.
func Dockerfile(tt []tools.Tool) string {
	var b strings.Builder
	b.WriteString(dockerfileBase)
	for _, t := range tt {
		b.WriteString("\n")
		b.WriteString(t.DockerfileStage)
	}
	return b.String()
}

// AvailableTools returns the list of available tool names derived from the
// given tool definitions.
func AvailableTools(tt []tools.Tool) []string {
	names := make([]string, len(tt))
	for i, t := range tt {
		names[i] = t.Name
	}
	return names
}

// ToolDescription returns a description for a tool by name. Returns a
// fallback string for unknown tools.
func ToolDescription(tt []tools.Tool, name string) string {
	for _, t := range tt {
		if t.Name == name {
			return t.Description
		}
	}
	return fmt.Sprintf("Unknown tool: %s", name)
}
