package daytona

import (
	"strings"
	"testing"
)

func TestPrepareDockerfileForDaytona(t *testing.T) {
	dockerfile := strings.Join([]string{
		"FROM ubuntu:24.04 AS base",
		"ARG USER",
		"RUN apt-get update && apt-get install -y tzdata",
		"FROM base AS tool",
		"ARG TOOL_VERSION",
		"RUN echo ok",
	}, "\n")

	got := prepareDockerfileForDaytona(dockerfile, map[string]string{
		"USER":         "leigh",
		"TOOL_VERSION": "1.2.3",
	})

	if strings.Count(got, "ENV DEBIAN_FRONTEND=noninteractive") != 2 {
		t.Fatalf("expected noninteractive env after each FROM, got:\n%s", got)
	}
	if !strings.Contains(got, "ARG USER=leigh") {
		t.Fatalf("expected USER build arg to be inlined, got:\n%s", got)
	}
	if !strings.Contains(got, "ARG TOOL_VERSION=1.2.3") {
		t.Fatalf("expected TOOL_VERSION build arg to be inlined, got:\n%s", got)
	}
}

func TestTrimBootstrapOutput(t *testing.T) {
	t.Run("strips launch script echo", func(t *testing.T) {
		input := strings.Join([]string{
			"abc123% stty -echo",
			"abc123% exec bash /tmp/.silo-start-tool-session.sh",
			"\x1b[?1049hWelcome to Claude Code",
		}, "\r")

		got := string(trimBootstrapOutput([]byte(input), "exec bash /tmp/.silo-start-tool-session.sh"))

		if strings.Contains(got, "stty -echo") {
			t.Fatalf("expected stty command to be removed, got %q", got)
		}
		if strings.Contains(got, "exec bash /tmp/.silo-start-tool-session.sh") {
			t.Fatalf("expected launch command to be removed, got %q", got)
		}
		if !strings.Contains(got, "Welcome to Claude Code") {
			t.Fatalf("expected tool output to remain, got %q", got)
		}
	})

	t.Run("strips reconnect attach echo", func(t *testing.T) {
		input := strings.Join([]string{
			"xyz789% stty -echo",
			"xyz789% exec bash /tmp/.silo-attach-session.sh",
			"\x1b[?1049hreconnected",
		}, "\r")

		got := string(trimBootstrapOutput([]byte(input), "exec bash /tmp/.silo-attach-session.sh"))

		if strings.Contains(got, "/tmp/.silo-attach-session.sh") {
			t.Fatalf("expected attach command to be removed, got %q", got)
		}
		if !strings.Contains(got, "reconnected") {
			t.Fatalf("expected reconnect output to remain, got %q", got)
		}
	})
}

func TestStripANSIControl(t *testing.T) {
	input := "\x1b[?25hError:\r\n\x1b]11;?\x1b\\still here\x1b[0m"
	got := stripANSIControl(input)

	if strings.Contains(got, "\x1b") {
		t.Fatalf("expected ANSI escapes to be removed, got %q", got)
	}
	if got != "Error:\nstill here" {
		t.Fatalf("unexpected sanitized output: %q", got)
	}
}
