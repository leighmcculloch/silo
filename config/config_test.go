package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
)

func TestDefaultConfig(t *testing.T) {
	toolDefaults := map[string]ToolConfig{
		"claude":   {MountsRW: []string{"~/.claude.json", "~/.claude"}},
		"opencode": {MountsRW: []string{"~/.config/opencode"}},
		"copilot":  {MountsRW: []string{"~/.config/.copilot"}},
	}
	cfg := DefaultConfig(toolDefaults)

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
		"mounts_ro": ["/test/mount/ro"],
		"mounts_rw": ["/test/mount/rw"],
		"env": ["TEST_VAR", "FOO=bar"],
		"pre_run_hooks": ["echo hello"],
		"tools": {
			"test-tool": {
				"mounts_ro": ["/tool/mount/ro"],
				"mounts_rw": ["/tool/mount/rw"],
				"env": ["TOOL_VAR", "TOOL_FOO=bar"]
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

	if len(cfg.MountsRO) != 1 || cfg.MountsRO[0] != "/test/mount/ro" {
		t.Errorf("expected mounts_ro [/test/mount/ro], got %v", cfg.MountsRO)
	}

	if len(cfg.MountsRW) != 1 || cfg.MountsRW[0] != "/test/mount/rw" {
		t.Errorf("expected mounts_rw [/test/mount/rw], got %v", cfg.MountsRW)
	}

	if len(cfg.Env) != 2 || cfg.Env[0] != "TEST_VAR" || cfg.Env[1] != "FOO=bar" {
		t.Errorf("expected env [TEST_VAR, FOO=bar], got %v", cfg.Env)
	}

	if len(cfg.PreRunHooks) != 1 || cfg.PreRunHooks[0] != "echo hello" {
		t.Errorf("expected pre_run_hooks [echo hello], got %v", cfg.PreRunHooks)
	}

	toolCfg, ok := cfg.Tools["test-tool"]
	if !ok {
		t.Fatal("expected test-tool config to exist")
	}

	if len(toolCfg.MountsRO) != 1 || toolCfg.MountsRO[0] != "/tool/mount/ro" {
		t.Errorf("expected tool mounts_ro [/tool/mount/ro], got %v", toolCfg.MountsRO)
	}

	if len(toolCfg.MountsRW) != 1 || toolCfg.MountsRW[0] != "/tool/mount/rw" {
		t.Errorf("expected tool mounts_rw [/tool/mount/rw], got %v", toolCfg.MountsRW)
	}

	if len(toolCfg.Env) != 2 || toolCfg.Env[0] != "TOOL_VAR" || toolCfg.Env[1] != "TOOL_FOO=bar" {
		t.Errorf("expected tool env [TOOL_VAR, TOOL_FOO=bar], got %v", toolCfg.Env)
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
		"mounts_rw": ["/test/mount"],
		/* Multi-line
		   comment */
		"env": ["TEST_VAR"]
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("failed to load JSONC config: %v", err)
	}

	if len(cfg.MountsRW) != 1 || cfg.MountsRW[0] != "/test/mount" {
		t.Errorf("expected mounts_rw [/test/mount], got %v", cfg.MountsRW)
	}

	if len(cfg.Env) != 1 || cfg.Env[0] != "TEST_VAR" {
		t.Errorf("expected env [TEST_VAR], got %v", cfg.Env)
	}
}

func TestMerge(t *testing.T) {
	base := Config{
		MountsRO:    []string{"/base/mount/ro"},
		MountsRW:    []string{"/base/mount/rw"},
		Env:         []string{"BASE_VAR", "BASE=1"},
		PreRunHooks: []string{"echo base"},
		Tools: map[string]ToolConfig{
			"tool1": {
				MountsRW: []string{"/tool1/base"},
			},
		},
	}

	overlay := Config{
		MountsRO:    []string{"/overlay/mount/ro"},
		MountsRW:    []string{"/overlay/mount/rw"},
		Env:         []string{"OVERLAY_VAR", "OVERLAY=1"},
		PreRunHooks: []string{"echo overlay"},
		Tools: map[string]ToolConfig{
			"tool1": {
				MountsRW: []string{"/tool1/overlay"},
			},
			"tool2": {
				MountsRW: []string{"/tool2"},
			},
		},
	}

	result := Merge(base, overlay)

	// Check arrays are appended
	if len(result.MountsRO) != 2 {
		t.Errorf("expected 2 mounts_ro, got %d", len(result.MountsRO))
	}
	if result.MountsRO[0] != "/base/mount/ro" || result.MountsRO[1] != "/overlay/mount/ro" {
		t.Errorf("unexpected mounts_ro: %v", result.MountsRO)
	}

	if len(result.MountsRW) != 2 {
		t.Errorf("expected 2 mounts_rw, got %d", len(result.MountsRW))
	}
	if result.MountsRW[0] != "/base/mount/rw" || result.MountsRW[1] != "/overlay/mount/rw" {
		t.Errorf("unexpected mounts_rw: %v", result.MountsRW)
	}

	if len(result.Env) != 4 {
		t.Errorf("expected 4 env, got %d", len(result.Env))
	}

	// Check pre_run_hooks arrays are appended
	if len(result.PreRunHooks) != 2 {
		t.Errorf("expected 2 pre_run_hooks commands, got %d", len(result.PreRunHooks))
	}
	if result.PreRunHooks[0] != "echo base" || result.PreRunHooks[1] != "echo overlay" {
		t.Errorf("unexpected pre_run_hooks: %v", result.PreRunHooks)
	}

	// Check tools are merged
	tool1, ok := result.Tools["tool1"]
	if !ok {
		t.Fatal("expected tool1 to exist")
	}
	if len(tool1.MountsRW) != 2 {
		t.Errorf("expected tool1 to have 2 mounts_rw, got %d", len(tool1.MountsRW))
	}

	tool2, ok := result.Tools["tool2"]
	if !ok {
		t.Fatal("expected tool2 to exist")
	}
	if len(tool2.MountsRW) != 1 || tool2.MountsRW[0] != "/tool2" {
		t.Errorf("unexpected tool2 mounts_rw: %v", tool2.MountsRW)
	}
}

func TestMergePreRunHooksAppend(t *testing.T) {
	// Test that pre_run_hooks arrays are appended
	base := Config{
		PreRunHooks: []string{"echo base"},
	}
	overlay := Config{
		PreRunHooks: []string{"echo overlay"},
	}

	result := Merge(base, overlay)
	if len(result.PreRunHooks) != 2 {
		t.Errorf("expected 2 pre_run_hooks commands, got %d", len(result.PreRunHooks))
	}
	if result.PreRunHooks[0] != "echo base" || result.PreRunHooks[1] != "echo overlay" {
		t.Errorf("expected [echo base, echo overlay], got %v", result.PreRunHooks)
	}

	// Test that empty overlay doesn't add anything
	base2 := Config{
		PreRunHooks: []string{"echo base"},
	}
	overlay2 := Config{
		PreRunHooks: []string{},
	}

	result2 := Merge(base2, overlay2)
	if len(result2.PreRunHooks) != 1 || result2.PreRunHooks[0] != "echo base" {
		t.Errorf("expected [echo base], got %v", result2.PreRunHooks)
	}
}

func TestMergeWithNilTools(t *testing.T) {
	base := Config{
		MountsRW: []string{"/base"},
		Tools:    nil,
	}

	overlay := Config{
		MountsRW: []string{"/overlay"},
		Tools: map[string]ToolConfig{
			"tool1": {MountsRW: []string{"/tool1"}},
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
	globalConfig := `{"mounts_rw": ["/global"]}`
	if err := os.WriteFile(filepath.Join(xdgConfigDir, "silo.jsonc"), []byte(globalConfig), 0644); err != nil {
		t.Fatalf("failed to write global config: %v", err)
	}

	// Create project directory
	projectDir := filepath.Join(tmpDir, "projects", "myproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Create local config in project
	localConfig := `{"mounts_rw": ["/local"]}`
	if err := os.WriteFile(filepath.Join(projectDir, "silo.jsonc"), []byte(localConfig), 0644); err != nil {
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

	cfg := LoadAll(nil)

	// Check that both global and local mounts are present
	hasGlobal := false
	hasLocal := false
	for _, m := range cfg.MountsRW {
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
