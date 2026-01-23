package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.EnvPassthrough) == 0 {
		t.Error("expected default env passthrough to not be empty")
	}

	if _, ok := cfg.Tools["claude"]; !ok {
		t.Error("expected claude tool config to exist")
	}

	if _, ok := cfg.Tools["opencode"]; !ok {
		t.Error("expected opencode tool config to exist")
	}

	if _, ok := cfg.Tools["copilot"]; !ok {
		t.Error("expected copilot tool config to exist")
	}
}

func TestLoad(t *testing.T) {
	// Create temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"mounts": ["/test/mount"],
		"env_passthrough": ["TEST_VAR"],
		"env_set": ["FOO=bar"],
		"source_files": ["/test/source"],
		"tools": {
			"test-tool": {
				"mounts": ["/tool/mount"],
				"env_passthrough": ["TOOL_VAR"],
				"env_set": ["TOOL_FOO=bar"]
			}
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if len(cfg.Mounts) != 1 || cfg.Mounts[0] != "/test/mount" {
		t.Errorf("expected mounts [/test/mount], got %v", cfg.Mounts)
	}

	if len(cfg.EnvPassthrough) != 1 || cfg.EnvPassthrough[0] != "TEST_VAR" {
		t.Errorf("expected env passthrough [TEST_VAR], got %v", cfg.EnvPassthrough)
	}

	if len(cfg.EnvSet) != 1 || cfg.EnvSet[0] != "FOO=bar" {
		t.Errorf("expected env set [FOO=bar], got %v", cfg.EnvSet)
	}

	if len(cfg.SourceFiles) != 1 || cfg.SourceFiles[0] != "/test/source" {
		t.Errorf("expected source files [/test/source], got %v", cfg.SourceFiles)
	}

	toolCfg, ok := cfg.Tools["test-tool"]
	if !ok {
		t.Fatal("expected test-tool config to exist")
	}

	if len(toolCfg.Mounts) != 1 || toolCfg.Mounts[0] != "/tool/mount" {
		t.Errorf("expected tool mounts [/tool/mount], got %v", toolCfg.Mounts)
	}
}

func TestLoadNonExistent(t *testing.T) {
	_, err := Load("/nonexistent/config.json")
	if err == nil {
		t.Error("expected error loading non-existent config")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.json")

	if err := os.WriteFile(configPath, []byte("not json"), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("expected error loading invalid JSON")
	}
}

func TestLoadJSONC(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// JSONC with comments
	configContent := `{
		// This is a comment
		"mounts": ["/test/mount"],
		/* Multi-line
		   comment */
		"env_passthrough": ["TEST_VAR"]
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load JSONC config: %v", err)
	}

	if len(cfg.Mounts) != 1 || cfg.Mounts[0] != "/test/mount" {
		t.Errorf("expected mounts [/test/mount], got %v", cfg.Mounts)
	}

	if len(cfg.EnvPassthrough) != 1 || cfg.EnvPassthrough[0] != "TEST_VAR" {
		t.Errorf("expected env passthrough [TEST_VAR], got %v", cfg.EnvPassthrough)
	}
}

func TestMerge(t *testing.T) {
	base := Config{
		Mounts:         []string{"/base/mount"},
		EnvPassthrough: []string{"BASE_VAR"},
		EnvSet:         []string{"BASE=1"},
		SourceFiles:    []string{"/base/source"},
		Tools: map[string]ToolConfig{
			"tool1": {
				Mounts: []string{"/tool1/base"},
			},
		},
	}

	overlay := Config{
		Mounts:         []string{"/overlay/mount"},
		EnvPassthrough: []string{"OVERLAY_VAR"},
		EnvSet:         []string{"OVERLAY=1"},
		SourceFiles:    []string{"/overlay/source"},
		Tools: map[string]ToolConfig{
			"tool1": {
				Mounts: []string{"/tool1/overlay"},
			},
			"tool2": {
				Mounts: []string{"/tool2"},
			},
		},
	}

	result := Merge(base, overlay)

	// Check arrays are appended
	if len(result.Mounts) != 2 {
		t.Errorf("expected 2 mounts, got %d", len(result.Mounts))
	}
	if result.Mounts[0] != "/base/mount" || result.Mounts[1] != "/overlay/mount" {
		t.Errorf("unexpected mounts: %v", result.Mounts)
	}

	if len(result.EnvPassthrough) != 2 {
		t.Errorf("expected 2 env passthrough, got %d", len(result.EnvPassthrough))
	}

	if len(result.EnvSet) != 2 {
		t.Errorf("expected 2 env set, got %d", len(result.EnvSet))
	}

	// Check tools are merged
	tool1, ok := result.Tools["tool1"]
	if !ok {
		t.Fatal("expected tool1 to exist")
	}
	if len(tool1.Mounts) != 2 {
		t.Errorf("expected tool1 to have 2 mounts, got %d", len(tool1.Mounts))
	}

	tool2, ok := result.Tools["tool2"]
	if !ok {
		t.Fatal("expected tool2 to exist")
	}
	if len(tool2.Mounts) != 1 || tool2.Mounts[0] != "/tool2" {
		t.Errorf("unexpected tool2 mounts: %v", tool2.Mounts)
	}
}

func TestMergeWithNilTools(t *testing.T) {
	base := Config{
		Mounts: []string{"/base"},
		Tools:  nil,
	}

	overlay := Config{
		Mounts: []string{"/overlay"},
		Tools: map[string]ToolConfig{
			"tool1": {Mounts: []string{"/tool1"}},
		},
	}

	result := Merge(base, overlay)

	if result.Tools == nil {
		t.Fatal("expected tools to not be nil")
	}

	if _, ok := result.Tools["tool1"]; !ok {
		t.Error("expected tool1 to exist")
	}
}

func TestLoadAll(t *testing.T) {
	// Create a temporary directory structure
	tmpDir := t.TempDir()

	// Create XDG config dir
	xdgConfigDir := filepath.Join(tmpDir, ".config", "silo")
	if err := os.MkdirAll(xdgConfigDir, 0755); err != nil {
		t.Fatalf("failed to create xdg config dir: %v", err)
	}

	// Create global config
	globalConfig := `{"mounts": ["/global"]}`
	if err := os.WriteFile(filepath.Join(xdgConfigDir, "config.json"), []byte(globalConfig), 0644); err != nil {
		t.Fatalf("failed to write global config: %v", err)
	}

	// Create project directory
	projectDir := filepath.Join(tmpDir, "projects", "myproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Create local config in project
	localConfig := `{"mounts": ["/local"]}`
	if err := os.WriteFile(filepath.Join(projectDir, ".silo.json"), []byte(localConfig), 0644); err != nil {
		t.Fatalf("failed to write local config: %v", err)
	}

	// Change to project directory and set XDG_CONFIG_HOME
	oldWd, _ := os.Getwd()
	oldXdg := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		os.Chdir(oldWd)
		os.Setenv("XDG_CONFIG_HOME", oldXdg)
		xdg.Reload()
	}()

	os.Chdir(projectDir)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	xdg.Reload()

	cfg, err := LoadAll()
	if err != nil {
		t.Fatalf("failed to load all configs: %v", err)
	}

	// Check that both global and local mounts are present
	hasGlobal := false
	hasLocal := false
	for _, m := range cfg.Mounts {
		if m == "/global" {
			hasGlobal = true
		}
		if m == "/local" {
			hasLocal = true
		}
	}

	if !hasGlobal {
		t.Error("expected global mount /global to be present")
	}
	if !hasLocal {
		t.Error("expected local mount /local to be present")
	}
}
