package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/tidwall/jsonc"
)

// Config represents the silo configuration
type Config struct {
	// Mounts are additional directories or files to mount into the container
	Mounts []string `json:"mounts,omitempty"`

	// EnvPassthrough are environment variable names to pass from host to container
	EnvPassthrough []string `json:"env_passthrough,omitempty"`

	// EnvSet are environment variables to set explicitly (KEY=VALUE format)
	EnvSet []string `json:"env_set,omitempty"`

	// SourceFiles are files to source before running (to load environment variables)
	SourceFiles []string `json:"source_files,omitempty"`

	// Tools defines available AI tools with their configurations
	Tools map[string]ToolConfig `json:"tools,omitempty"`
}

// ToolConfig represents configuration for a specific AI tool
type ToolConfig struct {
	// Mounts specific to this tool
	Mounts []string `json:"mounts,omitempty"`

	// EnvPassthrough specific to this tool
	EnvPassthrough []string `json:"env_passthrough,omitempty"`

	// EnvSet specific to this tool
	EnvSet []string `json:"env_set,omitempty"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() Config {
	home := xdg.Home

	return Config{
		Mounts: []string{},
		EnvPassthrough: []string{
			"XDG_CONFIG_HOME",
		},
		EnvSet:      []string{},
		SourceFiles: []string{},
		Tools: map[string]ToolConfig{
			"claude": {
				Mounts: []string{
					filepath.Join(home, ".claude.json"),
					filepath.Join(home, ".claude"),
				},
			},
			"opencode": {
				Mounts: []string{
					filepath.Join(xdg.ConfigHome, "opencode"),
					filepath.Join(xdg.DataHome, "opencode"),
				},
			},
			"copilot": {
				Mounts: []string{
					filepath.Join(xdg.ConfigHome, ".copilot"),
				},
				EnvPassthrough: []string{
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
	result.Mounts = append(result.Mounts, overlay.Mounts...)
	result.EnvPassthrough = append(result.EnvPassthrough, overlay.EnvPassthrough...)
	result.EnvSet = append(result.EnvSet, overlay.EnvSet...)
	result.SourceFiles = append(result.SourceFiles, overlay.SourceFiles...)

	// Merge tools map
	if result.Tools == nil {
		result.Tools = make(map[string]ToolConfig)
	}
	for name, tool := range overlay.Tools {
		if existing, ok := result.Tools[name]; ok {
			existing.Mounts = append(existing.Mounts, tool.Mounts...)
			existing.EnvPassthrough = append(existing.EnvPassthrough, tool.EnvPassthrough...)
			existing.EnvSet = append(existing.EnvSet, tool.EnvSet...)
			result.Tools[name] = existing
		} else {
			result.Tools[name] = tool
		}
	}

	return result
}

// SourceInfo tracks the source of configuration values
type SourceInfo struct {
	Mounts         map[string]string // value -> source path
	EnvPassthrough map[string]string
	EnvSet         map[string]string
	SourceFiles    map[string]string
	ToolMounts     map[string]map[string]string // tool -> value -> source
	ToolEnvPass    map[string]map[string]string
	ToolEnvSet     map[string]map[string]string
}

// NewSourceInfo creates a new empty SourceInfo
func NewSourceInfo() *SourceInfo {
	return &SourceInfo{
		Mounts:         make(map[string]string),
		EnvPassthrough: make(map[string]string),
		EnvSet:         make(map[string]string),
		SourceFiles:    make(map[string]string),
		ToolMounts:     make(map[string]map[string]string),
		ToolEnvPass:    make(map[string]map[string]string),
		ToolEnvSet:     make(map[string]map[string]string),
	}
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
	globalConfigPath := filepath.Join(xdg.ConfigHome, "silo", "config.json")
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
		configPath := filepath.Join(dir, ".silo.json")
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
	for _, v := range cfg.Mounts {
		info.Mounts[v] = source
	}
	for _, v := range cfg.EnvPassthrough {
		info.EnvPassthrough[v] = source
	}
	for _, v := range cfg.EnvSet {
		info.EnvSet[v] = source
	}
	for _, v := range cfg.SourceFiles {
		info.SourceFiles[v] = source
	}
	for toolName, toolCfg := range cfg.Tools {
		if info.ToolMounts[toolName] == nil {
			info.ToolMounts[toolName] = make(map[string]string)
		}
		if info.ToolEnvPass[toolName] == nil {
			info.ToolEnvPass[toolName] = make(map[string]string)
		}
		if info.ToolEnvSet[toolName] == nil {
			info.ToolEnvSet[toolName] = make(map[string]string)
		}
		for _, v := range toolCfg.Mounts {
			info.ToolMounts[toolName][v] = source
		}
		for _, v := range toolCfg.EnvPassthrough {
			info.ToolEnvPass[toolName][v] = source
		}
		for _, v := range toolCfg.EnvSet {
			info.ToolEnvSet[toolName][v] = source
		}
	}
}
