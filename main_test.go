package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"4d63.com/testcli"
)

// mainFunc wraps our run function to match testcli.MainFunc signature
func mainFunc(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return run(args, stdin, stdout, stderr)
}

func TestHelp(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"--help"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Check for ASCII art banner
	if !strings.Contains(stdout, "███████╗██╗██╗") {
		t.Error("expected ASCII art banner in help output")
	}

	// Check for description
	if !strings.Contains(stdout, "Run AI coding assistants") {
		t.Error("expected description in help output")
	}

	// Check for usage
	if !strings.Contains(stdout, "Usage:") {
		t.Error("expected usage section in help output")
	}

	// Check for examples
	if !strings.Contains(stdout, "Examples:") {
		t.Error("expected examples section in help output")
	}

	// Check for available commands
	if !strings.Contains(stdout, "Available Commands:") {
		t.Error("expected available commands section in help output")
	}

	// Check for config command
	if !strings.Contains(stdout, "config") {
		t.Error("expected config command in help output")
	}

	// Check for init command
	if !strings.Contains(stdout, "init") {
		t.Error("expected init command in help output")
	}
}

func TestVersion(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"--version"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(stdout, "silo version") {
		t.Errorf("expected version output, got: %s", stdout)
	}
}

func TestConfigCommand(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"config"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Check for configuration sections
	if !strings.Contains(stdout, "Silo Configuration") {
		t.Error("expected configuration title in output")
	}

	if !strings.Contains(stdout, "Global Settings") {
		t.Error("expected global settings section in output")
	}

	if !strings.Contains(stdout, "Tools") {
		t.Error("expected tools section in output")
	}

	// Check for default tools
	if !strings.Contains(stdout, "claude:") {
		t.Error("expected claude tool in output")
	}

	if !strings.Contains(stdout, "opencode:") {
		t.Error("expected opencode tool in output")
	}

	if !strings.Contains(stdout, "copilot:") {
		t.Error("expected copilot tool in output")
	}
}

func TestConfigHelp(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"config", "--help"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(stdout, "Show the current merged configuration") {
		t.Error("expected config description in help output")
	}
}

func TestInitCommand(t *testing.T) {
	tmpDir := testcli.MkdirTemp(t)
	testcli.Chdir(t, tmpDir)

	exitCode, _, stderr := testcli.Main(t, []string{"init"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d, stderr: %s", exitCode, stderr)
	}

	// Check that config file was created
	configPath := filepath.Join(tmpDir, ".silo.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("expected .silo.json to be created")
	}

	// Check contents
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if !strings.Contains(string(content), "mounts") {
		t.Error("expected mounts field in config")
	}

	if !strings.Contains(string(content), "env_passthrough") {
		t.Error("expected env_passthrough field in config")
	}
}

func TestInitCommandAlreadyExists(t *testing.T) {
	tmpDir := testcli.MkdirTemp(t)
	testcli.Chdir(t, tmpDir)

	// Create existing config
	configPath := filepath.Join(tmpDir, ".silo.json")
	testcli.WriteFile(t, configPath, []byte("{}"))

	exitCode, _, stderr := testcli.Main(t, []string{"init"}, nil, mainFunc)

	if exitCode == 0 {
		t.Error("expected failure when config already exists")
	}

	if !strings.Contains(stderr, "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", stderr)
	}
}

func TestInitHelp(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"init", "--help"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(stdout, "Create a sample .silo.json configuration file") {
		t.Error("expected init description in help output")
	}
}

func TestInvalidTool(t *testing.T) {
	exitCode, _, stderr := testcli.Main(t, []string{"invalid-tool"}, nil, mainFunc)

	if exitCode == 0 {
		t.Error("expected failure for invalid tool")
	}

	if !strings.Contains(stderr, "invalid tool") {
		t.Errorf("expected 'invalid tool' error, got: %s", stderr)
	}
}

func TestCompletionCommand(t *testing.T) {
	shells := []string{"bash", "zsh", "fish", "powershell"}

	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			exitCode, stdout, stderr := testcli.Main(t, []string{"completion", shell}, nil, mainFunc)

			if exitCode != 0 {
				t.Fatalf("expected exit code 0 for %s completion, got %d, stderr: %s", shell, exitCode, stderr)
			}

			if stdout == "" {
				t.Errorf("expected completion output for %s", shell)
			}
		})
	}
}

func TestHelpCommand(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"help"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Should show same as --help
	if !strings.Contains(stdout, "Run AI coding assistants") {
		t.Error("expected description in help output")
	}
}

func TestHelpConfig(t *testing.T) {
	exitCode, stdout, _ := testcli.Main(t, []string{"help", "config"}, nil, mainFunc)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(stdout, "Show the current merged configuration") {
		t.Error("expected config description in help output")
	}
}

func TestRunFunction(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"--help"}, nil, &stdout, &stderr)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !strings.Contains(stdout.String(), "silo") {
		t.Error("expected help output")
	}
}
