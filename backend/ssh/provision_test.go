package ssh

import (
	"strings"
	"testing"
)

// --- parseStages ---

func TestParseStagesSingle(t *testing.T) {
	dockerfile := "FROM ubuntu:22.04\nRUN apt-get update\nRUN apt-get install -y curl"
	stages := parseStages(dockerfile)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].from != "ubuntu:22.04" {
		t.Errorf("expected from ubuntu:22.04, got %s", stages[0].from)
	}
	if len(stages[0].directives) != 2 {
		t.Errorf("expected 2 directives, got %d", len(stages[0].directives))
	}
}

func TestParseStagesMultiStage(t *testing.T) {
	dockerfile := `FROM golang:1.21 AS builder
RUN go build -o /app
FROM ubuntu:22.04 AS runtime
RUN apt-get update`
	stages := parseStages(dockerfile)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(stages))
	}
	if stages[0].name != "builder" {
		t.Errorf("expected stage name 'builder', got %q", stages[0].name)
	}
	if stages[0].from != "golang:1.21" {
		t.Errorf("expected from golang:1.21, got %s", stages[0].from)
	}
	if stages[1].name != "runtime" {
		t.Errorf("expected stage name 'runtime', got %q", stages[1].name)
	}
	if stages[1].from != "ubuntu:22.04" {
		t.Errorf("expected from ubuntu:22.04, got %s", stages[1].from)
	}
}

func TestParseStagesFromAs(t *testing.T) {
	dockerfile := "FROM node:18 AS build\nRUN npm install"
	stages := parseStages(dockerfile)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].name != "build" {
		t.Errorf("expected name 'build', got %q", stages[0].name)
	}
}

func TestParseStagesCommentsAndBlankLines(t *testing.T) {
	dockerfile := `# This is a comment
FROM ubuntu:22.04

# Another comment
RUN echo hello

`
	stages := parseStages(dockerfile)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if len(stages[0].directives) != 1 {
		t.Errorf("expected 1 directive (comments/blanks skipped), got %d", len(stages[0].directives))
	}
}

func TestParseStagesGlobalARGBeforeFROM(t *testing.T) {
	dockerfile := `ARG BASE_IMAGE=ubuntu:22.04
FROM ${BASE_IMAGE}
RUN echo hello`
	stages := parseStages(dockerfile)
	// Should have a synthetic unnamed stage for the global ARG, plus the FROM stage.
	if len(stages) < 2 {
		t.Fatalf("expected at least 2 stages (synthetic + FROM), got %d", len(stages))
	}
	// The synthetic stage should be first and unnamed.
	if stages[0].name != "" {
		t.Errorf("expected synthetic stage to have empty name, got %q", stages[0].name)
	}
	if len(stages[0].directives) != 1 {
		t.Errorf("expected 1 directive in synthetic stage, got %d", len(stages[0].directives))
	}
	if stages[0].directives[0].Command != "ARG" {
		t.Errorf("expected ARG directive, got %s", stages[0].directives[0].Command)
	}
}

func TestParseStagesCaseInsensitiveFROM(t *testing.T) {
	dockerfile := "from ubuntu:22.04 as builder\nRUN echo hi"
	stages := parseStages(dockerfile)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].name != "builder" {
		t.Errorf("expected name 'builder', got %q", stages[0].name)
	}
}

// --- splitDirective ---

func TestSplitDirectiveRUN(t *testing.T) {
	cmd, args := splitDirective("RUN apt-get update && apt-get install -y curl")
	if cmd != "RUN" {
		t.Errorf("expected RUN, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "apt-get update && apt-get install -y curl" {
		t.Errorf("expected single arg with full command, got %v", args)
	}
}

func TestSplitDirectiveENV(t *testing.T) {
	cmd, args := splitDirective("ENV FOO=bar BAZ=qux")
	if cmd != "ENV" {
		t.Errorf("expected ENV, got %s", cmd)
	}
	if len(args) != 2 || args[0] != "FOO=bar" || args[1] != "BAZ=qux" {
		t.Errorf("expected [FOO=bar BAZ=qux], got %v", args)
	}
}

func TestSplitDirectiveWORKDIR(t *testing.T) {
	cmd, args := splitDirective("WORKDIR /app")
	if cmd != "WORKDIR" {
		t.Errorf("expected WORKDIR, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "/app" {
		t.Errorf("expected [/app], got %v", args)
	}
}

func TestSplitDirectiveCOPY(t *testing.T) {
	cmd, args := splitDirective("COPY src/ /app/src/")
	if cmd != "COPY" {
		t.Errorf("expected COPY, got %s", cmd)
	}
	if len(args) != 2 || args[0] != "src/" || args[1] != "/app/src/" {
		t.Errorf("expected [src/ /app/src/], got %v", args)
	}
}

func TestSplitDirectiveContinuationBackslash(t *testing.T) {
	cmd, args := splitDirective("RUN apt-get update \\")
	if cmd != "RUN" {
		t.Errorf("expected RUN, got %s", cmd)
	}
	if len(args) != 1 || args[0] != "apt-get update" {
		t.Errorf("expected [apt-get update], got %v", args)
	}
}

func TestSplitDirectiveCommandOnly(t *testing.T) {
	cmd, args := splitDirective("EXPOSE")
	if cmd != "EXPOSE" {
		t.Errorf("expected EXPOSE, got %s", cmd)
	}
	if args != nil {
		t.Errorf("expected nil args, got %v", args)
	}
}

// --- splitArgs ---

func TestSplitArgsRUN(t *testing.T) {
	args := splitArgs("RUN", "echo hello world")
	if len(args) != 1 || args[0] != "echo hello world" {
		t.Errorf("expected single arg, got %v", args)
	}
}

func TestSplitArgsENV(t *testing.T) {
	args := splitArgs("ENV", "FOO=bar BAZ=qux")
	if len(args) != 2 {
		t.Errorf("expected 2 args, got %d: %v", len(args), args)
	}
}

func TestSplitArgsCOPY(t *testing.T) {
	args := splitArgs("COPY", "src/ /app/")
	if len(args) != 2 || args[0] != "src/" || args[1] != "/app/" {
		t.Errorf("expected [src/ /app/], got %v", args)
	}
}

// --- tokenize ---

func TestTokenizeSimple(t *testing.T) {
	tokens := tokenize("foo bar baz")
	if len(tokens) != 3 || tokens[0] != "foo" || tokens[1] != "bar" || tokens[2] != "baz" {
		t.Errorf("expected [foo bar baz], got %v", tokens)
	}
}

func TestTokenizeQuotedStrings(t *testing.T) {
	tokens := tokenize(`"hello world" foo "bar baz"`)
	if len(tokens) != 3 || tokens[0] != "hello world" || tokens[1] != "foo" || tokens[2] != "bar baz" {
		t.Errorf("expected [hello world, foo, bar baz], got %v", tokens)
	}
}

func TestTokenizeEmpty(t *testing.T) {
	tokens := tokenize("")
	if len(tokens) != 0 {
		t.Errorf("expected empty slice, got %v", tokens)
	}
}

func TestTokenizeMultipleSpaces(t *testing.T) {
	tokens := tokenize("foo   bar")
	if len(tokens) != 2 || tokens[0] != "foo" || tokens[1] != "bar" {
		t.Errorf("expected [foo bar], got %v", tokens)
	}
}

// --- DockerfileToShell ---

func TestDockerfileToShellSingleStage(t *testing.T) {
	dockerfile := `FROM ubuntu:22.04
RUN apt-get update
ENV FOO=bar`
	script, err := DockerfileToShell(dockerfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(script, "#!/bin/bash\n") {
		t.Error("expected script to start with shebang")
	}
	if !strings.Contains(script, "apt-get update") {
		t.Error("expected script to contain apt-get update")
	}
	if !strings.Contains(script, "export FOO=") {
		t.Error("expected script to contain export FOO")
	}
}

func TestDockerfileToShellMultiStageWithTarget(t *testing.T) {
	dockerfile := `FROM golang:1.21 AS builder
RUN go build
FROM ubuntu:22.04 AS runtime
RUN apt-get update`
	script, err := DockerfileToShell(dockerfile, "runtime")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "apt-get update") {
		t.Error("expected script to contain runtime stage commands")
	}
	if strings.Contains(script, "go build") {
		t.Error("expected script to NOT contain builder stage commands")
	}
}

func TestDockerfileToShellMissingTargetSingleStageFallback(t *testing.T) {
	dockerfile := `FROM ubuntu:22.04
RUN echo hello`
	// Single stage: should fallback even if target doesn't match.
	script, err := DockerfileToShell(dockerfile, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "echo hello") {
		t.Error("expected single stage fallback to include commands")
	}
}

func TestDockerfileToShellMissingTargetMultiStageError(t *testing.T) {
	dockerfile := `FROM golang:1.21 AS builder
RUN go build
FROM ubuntu:22.04 AS runtime
RUN apt-get update`
	_, err := DockerfileToShell(dockerfile, "nonexistent")
	if err == nil {
		t.Error("expected error for missing target in multi-stage Dockerfile")
	}
	if !strings.Contains(err.Error(), "stage not found") {
		t.Errorf("expected 'stage not found' error, got: %v", err)
	}
}

func TestDockerfileToShellEmpty(t *testing.T) {
	_, err := DockerfileToShell("", "")
	if err == nil {
		t.Error("expected error for empty Dockerfile")
	}
}

func TestDockerfileToShellFROMChainResolution(t *testing.T) {
	dockerfile := `FROM ubuntu:22.04 AS base
RUN apt-get update
FROM base AS app
RUN echo app`
	script, err := DockerfileToShell(dockerfile, "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should include base stage directives via FROM chain.
	if !strings.Contains(script, "apt-get update") {
		t.Error("expected script to include base stage commands via FROM chain")
	}
	if !strings.Contains(script, "echo app") {
		t.Error("expected script to include app stage commands")
	}
}

func TestDockerfileToShellUserAndWorkdirTracking(t *testing.T) {
	dockerfile := `FROM ubuntu:22.04
WORKDIR /app
RUN echo in-app-dir
USER developer
RUN echo as-developer`
	script, err := DockerfileToShell(dockerfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "mkdir -p /app") {
		t.Error("expected WORKDIR to create directory")
	}
	if !strings.Contains(script, "cd /app && echo in-app-dir") {
		t.Error("expected RUN after WORKDIR to have cd prefix")
	}
	if !strings.Contains(script, "su - ") {
		t.Error("expected RUN after USER to use su")
	}
	if !strings.Contains(script, "developer") {
		t.Error("expected su to reference the developer user")
	}
}

// --- ProvisionScript ---

func TestProvisionScriptContainsHash(t *testing.T) {
	script := ProvisionScript("echo hello")
	hash := sha256Hash("echo hello")
	if !strings.Contains(script, hash) {
		t.Error("expected provision script to contain the SHA-256 hash of the input")
	}
}

func TestProvisionScriptIdempotencyStructure(t *testing.T) {
	script := ProvisionScript("echo hello")
	if !strings.Contains(script, "MARKER_FILE") {
		t.Error("expected provision script to reference MARKER_FILE")
	}
	if !strings.Contains(script, "EXPECTED_HASH") {
		t.Error("expected provision script to reference EXPECTED_HASH")
	}
	if !strings.Contains(script, "already provisioned") {
		t.Error("expected provision script to have early exit message")
	}
	if !strings.Contains(script, "echo hello") {
		t.Error("expected provision script to contain the original script")
	}
	if !strings.HasPrefix(script, "#!/bin/bash\n") {
		t.Error("expected provision script to start with shebang")
	}
}

func TestProvisionScriptDifferentInputsDifferentHashes(t *testing.T) {
	s1 := ProvisionScript("echo hello")
	s2 := ProvisionScript("echo world")
	h1 := sha256Hash("echo hello")
	h2 := sha256Hash("echo world")
	if h1 == h2 {
		t.Fatal("hashes should differ for different inputs")
	}
	if !strings.Contains(s1, h1) {
		t.Error("s1 should contain h1")
	}
	if !strings.Contains(s2, h2) {
		t.Error("s2 should contain h2")
	}
}

// --- directiveToShell ---

func TestDirectiveToShellRUN(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "RUN", Args: []string{"echo hello"}}, &user, &dir)
	if result != "echo hello" {
		t.Errorf("expected 'echo hello', got %q", result)
	}
}

func TestDirectiveToShellRUNWithWorkdir(t *testing.T) {
	user := ""
	dir := "/app"
	result := directiveToShell(Directive{Command: "RUN", Args: []string{"make build"}}, &user, &dir)
	if !strings.HasPrefix(result, "cd /app && ") {
		t.Errorf("expected cd prefix, got %q", result)
	}
	if !strings.Contains(result, "make build") {
		t.Errorf("expected 'make build' in result, got %q", result)
	}
}

func TestDirectiveToShellRUNWithUser(t *testing.T) {
	user := "appuser"
	dir := ""
	result := directiveToShell(Directive{Command: "RUN", Args: []string{"whoami"}}, &user, &dir)
	if !strings.HasPrefix(result, "su - ") {
		t.Errorf("expected su prefix, got %q", result)
	}
	if !strings.Contains(result, "appuser") {
		t.Errorf("expected user in result, got %q", result)
	}
}

func TestDirectiveToShellRUNWithRootUser(t *testing.T) {
	user := "root"
	dir := ""
	result := directiveToShell(Directive{Command: "RUN", Args: []string{"apt-get update"}}, &user, &dir)
	// root should NOT use su.
	if strings.HasPrefix(result, "su") {
		t.Errorf("expected no su for root user, got %q", result)
	}
	if result != "apt-get update" {
		t.Errorf("expected 'apt-get update', got %q", result)
	}
}

func TestDirectiveToShellRUNEmptyArgs(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "RUN", Args: nil}, &user, &dir)
	if result != "" {
		t.Errorf("expected empty string for RUN with no args, got %q", result)
	}
}

func TestDirectiveToShellENV(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "ENV", Args: []string{"FOO=bar"}}, &user, &dir)
	if !strings.Contains(result, "export FOO=") {
		t.Errorf("expected export, got %q", result)
	}
}

func TestDirectiveToShellWORKDIR(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "WORKDIR", Args: []string{"/app"}}, &user, &dir)
	if result != "mkdir -p /app" {
		t.Errorf("expected 'mkdir -p /app', got %q", result)
	}
	if dir != "/app" {
		t.Errorf("expected currentDir to be set to /app, got %q", dir)
	}
}

func TestDirectiveToShellUSER(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "USER", Args: []string{"developer"}}, &user, &dir)
	if result != "# USER developer" {
		t.Errorf("expected comment, got %q", result)
	}
	if user != "developer" {
		t.Errorf("expected currentUser to be 'developer', got %q", user)
	}
}

func TestDirectiveToShellCOPY(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "COPY", Args: []string{".", "/app"}}, &user, &dir)
	if result != "# COPY skipped (files come from sync)" {
		t.Errorf("expected COPY skip comment, got %q", result)
	}
}

func TestDirectiveToShellEXPOSE(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "EXPOSE", Args: []string{"8080"}}, &user, &dir)
	if result != "# EXPOSE 8080" {
		t.Errorf("expected EXPOSE comment, got %q", result)
	}
}

func TestDirectiveToShellEXPOSEEmpty(t *testing.T) {
	user := ""
	dir := ""
	result := directiveToShell(Directive{Command: "EXPOSE", Args: nil}, &user, &dir)
	if result != "" {
		t.Errorf("expected empty for EXPOSE with no args, got %q", result)
	}
}

func TestDirectiveToShellUnsupported(t *testing.T) {
	user := ""
	dir := ""
	for _, cmd := range []string{"LABEL", "CMD", "ENTRYPOINT", "VOLUME", "HEALTHCHECK"} {
		result := directiveToShell(Directive{Command: cmd, Args: []string{"something"}}, &user, &dir)
		if result != "" {
			t.Errorf("expected empty for %s, got %q", cmd, result)
		}
	}
}

// --- envToShell ---

func TestEnvToShellKeyValueForm(t *testing.T) {
	result := envToShell([]string{"FOO=bar", "BAZ=qux"})
	if !strings.Contains(result, "export FOO=bar") {
		t.Errorf("expected 'export FOO=bar', got %q", result)
	}
	if !strings.Contains(result, "export BAZ=qux") {
		t.Errorf("expected 'export BAZ=qux', got %q", result)
	}
}

func TestEnvToShellKeySpaceValueForm(t *testing.T) {
	// ENV KEY VALUE form: args are [KEY, VALUE]
	result := envToShell([]string{"MY_VAR", "hello world"})
	if !strings.Contains(result, "export MY_VAR=") {
		t.Errorf("expected 'export MY_VAR=', got %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected value 'hello world', got %q", result)
	}
}

func TestEnvToShellEmpty(t *testing.T) {
	result := envToShell(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// --- argToShell ---

func TestArgToShellWithDefault(t *testing.T) {
	result := argToShell([]string{"VERSION=1.0"})
	if !strings.Contains(result, "VERSION=${VERSION:-1.0}") {
		t.Errorf("expected default value syntax, got %q", result)
	}
}

func TestArgToShellWithoutDefault(t *testing.T) {
	result := argToShell([]string{"VERSION"})
	if !strings.Contains(result, "${VERSION?") {
		t.Errorf("expected required arg syntax, got %q", result)
	}
}

func TestArgToShellEmpty(t *testing.T) {
	result := argToShell(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// --- addToShell ---

func TestAddToShellURLSource(t *testing.T) {
	result := addToShell([]string{"https://example.com/file.tar.gz", "/tmp/file.tar.gz"})
	if !strings.HasPrefix(result, "curl -fsSL") {
		t.Errorf("expected curl command, got %q", result)
	}
	if !strings.Contains(result, "https://example.com/file.tar.gz") {
		t.Errorf("expected URL in result, got %q", result)
	}
	if !strings.Contains(result, "/tmp/file.tar.gz") {
		t.Errorf("expected destination in result, got %q", result)
	}
}

func TestAddToShellLocalSource(t *testing.T) {
	result := addToShell([]string{"./local-file", "/app/local-file"})
	if !strings.Contains(result, "# ADD") {
		t.Errorf("expected comment for local source, got %q", result)
	}
	if !strings.Contains(result, "local source skipped") {
		t.Errorf("expected skip message, got %q", result)
	}
}

func TestAddToShellWithChownFlag(t *testing.T) {
	result := addToShell([]string{"--chown=1000:1000", "https://example.com/file", "/app/file"})
	if !strings.HasPrefix(result, "curl -fsSL") {
		t.Errorf("expected curl command (--chown skipped), got %q", result)
	}
	if !strings.Contains(result, "https://example.com/file") {
		t.Errorf("expected URL in result, got %q", result)
	}
}

func TestAddToShellTooFewArgs(t *testing.T) {
	result := addToShell([]string{"/single"})
	if result != "" {
		t.Errorf("expected empty for too few args, got %q", result)
	}
}

// --- sha256Hash ---

func TestSha256Hash(t *testing.T) {
	hash := sha256Hash("hello")
	// Known SHA-256 of "hello".
	expected := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if hash != expected {
		t.Errorf("expected %s, got %s", expected, hash)
	}
}

func TestSha256HashDifferentInputs(t *testing.T) {
	h1 := sha256Hash("a")
	h2 := sha256Hash("b")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestSha256HashEmpty(t *testing.T) {
	hash := sha256Hash("")
	// Known SHA-256 of empty string.
	expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hash != expected {
		t.Errorf("expected %s, got %s", expected, hash)
	}
}
