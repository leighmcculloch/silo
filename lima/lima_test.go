package lima

import (
	"strings"
	"testing"

	"github.com/leighmcculloch/silo/backend"
)

func TestComputeCacheKey(t *testing.T) {
	c := &Client{cacheDir: t.TempDir()}

	opts1 := backend.BuildOptions{
		Dockerfile: "FROM ubuntu",
		Target:     "claude",
		BuildArgs: map[string]string{
			"USER": "testuser",
		},
	}

	key1, err := c.computeCacheKey(opts1)
	if err != nil {
		t.Fatalf("computeCacheKey failed: %v", err)
	}

	// Same options should produce same key
	key2, err := c.computeCacheKey(opts1)
	if err != nil {
		t.Fatalf("computeCacheKey failed: %v", err)
	}

	if key1 != key2 {
		t.Errorf("same options produced different keys: %s vs %s", key1, key2)
	}

	// Different target should produce SAME key (one VM for all tools)
	opts2 := backend.BuildOptions{
		Dockerfile: "FROM ubuntu",
		Target:     "opencode",
		BuildArgs: map[string]string{
			"USER": "testuser",
		},
	}
	key3, err := c.computeCacheKey(opts2)
	if err != nil {
		t.Fatalf("computeCacheKey failed: %v", err)
	}

	if key1 != key3 {
		t.Errorf("different targets should produce same key (one VM for all tools): %s vs %s", key1, key3)
	}
}

func TestVMExists(t *testing.T) {
	c := &Client{cacheDir: t.TempDir()}

	// Non-existent VM should return false
	if c.vmExists("nonexistent-vm-12345") {
		t.Error("vmExists returned true for non-existent VM")
	}
}

func TestGenerateConfig(t *testing.T) {
	c := &Client{cacheDir: t.TempDir()}

	opts := backend.BuildOptions{
		Dockerfile: "FROM ubuntu",
		Target:     "claude",
	}

	config, err := c.generateConfig(opts)
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	// Check that config contains expected content
	if config == "" {
		t.Error("generateConfig returned empty config")
	}

	// Check for key configuration values
	expectedStrings := []string{
		"vmType: vz",
		"arch: aarch64",
		"images:",
		"cpus:",   // dynamically set
		"memory:", // dynamically set
		"vzNAT: true",
		"claude.ai/install.sh",
		"opencode.ai/install",
		"gh.io/copilot-install",
	}

	for _, s := range expectedStrings {
		if !strings.Contains(config, s) {
			t.Errorf("config missing expected string: %s", s)
		}
	}

	// Ensure Rosetta is NOT in the config
	if strings.Contains(config, "rosetta") {
		t.Error("config should not contain rosetta configuration")
	}
}

func TestTemplateContainsAllTools(t *testing.T) {
	// Verify that the template installs all three tools
	tools := []string{
		"claude.ai/install.sh",
		"opencode.ai/install",
		"gh.io/copilot-install",
	}

	for _, tool := range tools {
		if !strings.Contains(templateYAML, tool) {
			t.Errorf("template missing tool installation: %s", tool)
		}
	}
}

func TestTemplateContainsBashrcSetup(t *testing.T) {
	// Verify that the template sets up .bashrc for persistent PATH
	expectedStrings := []string{
		".bashrc",
		"GOROOT",
		"GOPATH",
		"PATH=",
	}

	for _, s := range expectedStrings {
		if !strings.Contains(templateYAML, s) {
			t.Errorf("template missing .bashrc setup: %s", s)
		}
	}
}
