package dockerfile

import (
	"strings"
	"testing"
)

func TestDockerfile(t *testing.T) {
	df := Dockerfile()

	if df == "" {
		t.Error("expected dockerfile to not be empty")
	}

	// Check for base stage
	if !strings.Contains(df, "FROM ubuntu:24.04 AS base") {
		t.Error("expected dockerfile to contain base stage")
	}

	// Check for opencode stage
	if !strings.Contains(df, "FROM base AS opencode") {
		t.Error("expected dockerfile to contain opencode stage")
	}

	// Check for claude stage
	if !strings.Contains(df, "FROM base AS claude") {
		t.Error("expected dockerfile to contain claude stage")
	}

	// Check for copilot stage
	if !strings.Contains(df, "FROM base AS copilot") {
		t.Error("expected dockerfile to contain copilot stage")
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
}

func TestAvailableTools(t *testing.T) {
	tools := AvailableTools()

	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	expected := map[string]bool{
		"opencode": true,
		"claude":   true,
		"copilot":  true,
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
		{"opencode", "OpenCode"},
		{"claude", "Claude"},
		{"copilot", "Copilot"},
		{"unknown", "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			desc := ToolDescription(tt.tool)
			if !strings.Contains(desc, tt.contains) {
				t.Errorf("expected description for %s to contain %q, got %q", tt.tool, tt.contains, desc)
			}
		})
	}
}
