package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/tidwall/jsonc"
)

// Config represents the silo configuration
type Config struct {
	// MountsRO are read-only directories or files to mount into the container
	MountsRO []string `json:"mounts_ro,omitempty"`

	// MountsRW are read-write directories or files to mount into the container
	MountsRW []string `json:"mounts_rw,omitempty"`

	// Env are environment variables. Values without '=' are passed through from host.
	// Values with '=' are set explicitly (KEY=VALUE format).
	Env []string `json:"env,omitempty"`

	// Prehooks is a list of shell commands to run inside the container before the tool.
	Prehooks []string `json:"prehooks,omitempty"`

	// Tools defines available AI tools with their configurations
	Tools map[string]ToolConfig `json:"tools,omitempty"`
}

// ToolConfig represents configuration for a specific AI tool
type ToolConfig struct {
	// MountsRO are read-only mounts specific to this tool
	MountsRO []string `json:"mounts_ro,omitempty"`

	// MountsRW are read-write mounts specific to this tool
	MountsRW []string `json:"mounts_rw,omitempty"`

	// Env specific to this tool (same format as Config.Env)
	Env []string `json:"env,omitempty"`
}

// xdgConfigHome returns XDG_CONFIG_HOME or the default ~/.config
func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(xdg.Home, ".config")
}

// xdgDataHome returns XDG_DATA_HOME or the default ~/.local/share
func xdgDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(xdg.Home, ".local", "share")
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	// Helper to replace home dir with ~
	tildePath := func(path string) string {
		if rest, ok := strings.CutPrefix(path, xdg.Home); ok {
			return "~" + rest
		}
		return path
	}

	return Config{
		MountsRO: []string{},
		MountsRW: []string{},
		Env:      []string{},
		Prehooks: []string{},
		Tools: map[string]ToolConfig{
			"claude": {
				MountsRW: []string{
					"~/.claude.json",
					"~/.claude",
				},
			},
			"opencode": {
				MountsRW: []string{
					tildePath(filepath.Join(xdgConfigHome(), "opencode")),
					tildePath(filepath.Join(xdgDataHome(), "opencode")),
				},
			},
			"copilot": {
				MountsRW: []string{
					tildePath(filepath.Join(xdgConfigHome(), ".copilot")),
				},
				Env: []string{
					"COPILOT_GITHUB_TOKEN",
				},
			},
		},
	}
}

// XDGConfigHome returns the XDG config home directory
func XDGConfigHome() string {
	return xdg.ConfigHome
}

// Load loads configuration from the given path (supports JSONC with comments)
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	// Strip comments from JSONC to get valid JSON
	jsonData := jsonc.ToJSON(data)

	var cfg Config
	if err := json.Unmarshal(jsonData, &cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Merge merges two configs, with the overlay taking precedence for arrays (append) and maps (merge)
func Merge(base, overlay Config) Config {
	result := base

	// Append arrays
	result.MountsRO = append(result.MountsRO, overlay.MountsRO...)
	result.MountsRW = append(result.MountsRW, overlay.MountsRW...)
	result.Env = append(result.Env, overlay.Env...)
	result.Prehooks = append(result.Prehooks, overlay.Prehooks...)

	// Merge tools map
	if result.Tools == nil {
		result.Tools = make(map[string]ToolConfig)
	}
	for name, tool := range overlay.Tools {
		if existing, ok := result.Tools[name]; ok {
			existing.MountsRO = append(existing.MountsRO, tool.MountsRO...)
			existing.MountsRW = append(existing.MountsRW, tool.MountsRW...)
			existing.Env = append(existing.Env, tool.Env...)
			result.Tools[name] = existing
		} else {
			result.Tools[name] = tool
		}
	}

	return result
}

// SourceInfo tracks the source of configuration values
type SourceInfo struct {
	MountsRO     map[string]string            // value -> source path
	MountsRW     map[string]string            // value -> source path
	Env          map[string]string            // value -> source path
	Prehooks     map[string]string            // value -> source path
	ToolMountsRO map[string]map[string]string // tool -> value -> source
	ToolMountsRW map[string]map[string]string // tool -> value -> source
	ToolEnv      map[string]map[string]string // tool -> value -> source
}

// NewSourceInfo creates a new empty SourceInfo
func NewSourceInfo() *SourceInfo {
	return &SourceInfo{
		MountsRO:     make(map[string]string),
		MountsRW:     make(map[string]string),
		Env:          make(map[string]string),
		Prehooks:     make(map[string]string),
		ToolMountsRO: make(map[string]map[string]string),
		ToolMountsRW: make(map[string]map[string]string),
		ToolEnv:      make(map[string]map[string]string),
	}
}

// ConfigPath represents a config file path with its status
type ConfigPath struct {
	Path   string
	Exists bool
}

// GetConfigPaths returns all config paths that would be checked/loaded
func GetConfigPaths() []ConfigPath {
	var paths []ConfigPath

	// Global config
	globalConfigPath := filepath.Join(xdg.ConfigHome, "silo", "silo.jsonc")
	_, err := os.Stat(globalConfigPath)
	paths = append(paths, ConfigPath{Path: globalConfigPath, Exists: err == nil})

	// Find all config files from root to current directory
	cwd, err := os.Getwd()
	if err != nil {
		return paths
	}

	var localPaths []ConfigPath
	dir := cwd
	for {
		configPath := filepath.Join(dir, "silo.jsonc")
		_, err := os.Stat(configPath)
		localPaths = append([]ConfigPath{{Path: configPath, Exists: err == nil}}, localPaths...)

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	paths = append(paths, localPaths...)
	return paths
}

// LoadAll loads and merges all configuration files from XDG config home and current/parent directories
func LoadAll() (Config, error) {
	cfg, _ := LoadAllWithSources()
	return cfg, nil
}

// LoadAllWithSources loads and merges all configs, tracking the source of each value
func LoadAllWithSources() (Config, *SourceInfo) {
	cfg := DefaultConfig()
	sources := NewSourceInfo()

	// Track defaults
	trackConfigSources(cfg, "default", sources)

	// Load from XDG config home
	globalConfigPath := filepath.Join(xdg.ConfigHome, "silo", "silo.jsonc")
	if globalCfg, err := Load(globalConfigPath); err == nil {
		trackConfigSources(globalCfg, globalConfigPath, sources)
		cfg = Merge(cfg, globalCfg)
	}

	// Find all config files from root to current directory
	cwd, err := os.Getwd()
	if err != nil {
		return cfg, sources
	}

	var configPaths []string
	dir := cwd
	for {
		configPath := filepath.Join(dir, "silo.jsonc")
		if _, err := os.Stat(configPath); err == nil {
			configPaths = append([]string{configPath}, configPaths...)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Load and merge configs from parent to child (child overrides parent)
	for _, path := range configPaths {
		if localCfg, err := Load(path); err == nil {
			trackConfigSources(localCfg, path, sources)
			cfg = Merge(cfg, localCfg)
		}
	}

	return cfg, sources
}

// trackConfigSources records the source for each value in the config
func trackConfigSources(cfg Config, source string, info *SourceInfo) {
	for _, v := range cfg.MountsRO {
		info.MountsRO[v] = source
	}
	for _, v := range cfg.MountsRW {
		info.MountsRW[v] = source
	}
	for _, v := range cfg.Env {
		info.Env[v] = source
	}
	for _, v := range cfg.Prehooks {
		info.Prehooks[v] = source
	}
	for toolName, toolCfg := range cfg.Tools {
		if info.ToolMountsRO[toolName] == nil {
			info.ToolMountsRO[toolName] = make(map[string]string)
		}
		if info.ToolMountsRW[toolName] == nil {
			info.ToolMountsRW[toolName] = make(map[string]string)
		}
		if info.ToolEnv[toolName] == nil {
			info.ToolEnv[toolName] = make(map[string]string)
		}
		for _, v := range toolCfg.MountsRO {
			info.ToolMountsRO[toolName][v] = source
		}
		for _, v := range toolCfg.MountsRW {
			info.ToolMountsRW[toolName][v] = source
		}
		for _, v := range toolCfg.Env {
			info.ToolEnv[toolName][v] = source
		}
	}
}
