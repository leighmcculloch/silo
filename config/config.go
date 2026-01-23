package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
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

// Load loads configuration from the given path
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
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

// LoadAll loads and merges all configuration files from XDG config home and current/parent directories
func LoadAll() (Config, error) {
	cfg := DefaultConfig()

	// Load from XDG config home
	globalConfigPath := filepath.Join(xdg.ConfigHome, "silo", "config.json")
	if globalCfg, err := Load(globalConfigPath); err == nil {
		cfg = Merge(cfg, globalCfg)
	}

	// Find all config files from root to current directory
	cwd, err := os.Getwd()
	if err != nil {
		return cfg, nil
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
			cfg = Merge(cfg, localCfg)
		}
	}

	return cfg, nil
}
