package main

import (
	"strings"
	"testing"
)

func TestDockerfile(t *testing.T) {
	for _, tool := range supportedTools {
		t.Run(tool.Name, func(t *testing.T) {
			df := Dockerfile(tool)

			if df == "" {
				t.Error("expected dockerfile to not be empty")
			}

			// Check for base stage
			if !strings.Contains(df, "FROM ubuntu:24.04 AS base") {
				t.Error("expected dockerfile to contain base stage")
			}

			// Check that it contains only this tool's stage
			if !strings.Contains(df, "FROM base AS "+tool.Name) {
				t.Errorf("expected dockerfile to contain %s stage", tool.Name)
			}

			// Check for build args
			if !strings.Contains(df, "ARG USER") {
				t.Error("expected dockerfile to contain USER build arg")
			}
			if !strings.Contains(df, "ARG UID") {
				t.Error("expected dockerfile to contain UID build arg")
			}
			if !strings.Contains(df, "ARG HOME") {
				t.Error("expected dockerfile to contain HOME build arg")
			}
		})
	}
}

func TestAvailableTools(t *testing.T) {
	tools := AvailableTools(supportedTools)

	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	expected := map[string]bool{
		"claude":      true,
		"cline":       true,
		"codex":       true,
		"opencode":    true,
		"paperclipai": true,
		"copilot":     true,
		"vibe":        true,
		"kilo":        true,
	}

	for _, tool := range tools {
		if !expected[tool] {
			t.Errorf("unexpected tool: %s", tool)
		}
		delete(expected, tool)
	}

	for tool := range expected {
		t.Errorf("missing expected tool: %s", tool)
	}
}

func TestToolDescription(t *testing.T) {
	tests := []struct {
		tool     string
		contains string
	}{
		{"claude", "Claude"},
		{"cline", "Cline"},
		{"codex", "Codex"},
		{"opencode", "OpenCode"},
		{"paperclipai", "Paperclip"},
		{"copilot", "Copilot"},
		{"vibe", "Vibe"},
		{"kilo", "Kilo"},
		{"unknown", "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			desc := ToolDescription(supportedTools, tt.tool)
			if !strings.Contains(desc, tt.contains) {
				t.Errorf("expected description for %s to contain %q, got %q", tt.tool, tt.contains, desc)
			}
		})
	}
}
