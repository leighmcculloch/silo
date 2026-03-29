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
