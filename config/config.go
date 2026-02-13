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
	// Backend specifies which backend to use: "docker" (default)
	Backend string `json:"backend,omitempty"`

	// Tool specifies the default tool to run: "claude", "opencode", or "copilot"
	// If not set, an interactive prompt is shown
	Tool string `json:"tool,omitempty"`

	// MountsRO are read-only directories or files to mount into the container
	MountsRO []string `json:"mounts_ro,omitempty"`

	// MountsRW are read-write directories or files to mount into the container
	MountsRW []string `json:"mounts_rw,omitempty"`

	// Env are environment variables. Values without '=' are passed through from host.
	// Values with '=' are set explicitly (KEY=VALUE format).
	Env []string `json:"env,omitempty"`

	// PreRunHooks is a list of shell commands to run inside the container before the tool.
	PreRunHooks []string `json:"pre_run_hooks,omitempty"`

	// PostBuildHooks is a list of shell commands to run inside the container after building the image.
	PostBuildHooks []string `json:"post_build_hooks,omitempty"`

	// Tools defines available AI tools with their configurations
	Tools map[string]ToolConfig `json:"tools,omitempty"`

	// Repos defines repository-specific configurations that are applied when
	// a git remote URL contains the specified key as a substring.
	Repos map[string]RepoConfig `json:"repos,omitempty"`
}

// ToolConfig represents configuration for a specific AI tool
type ToolConfig struct {
	// MountsRO are read-only mounts specific to this tool
	MountsRO []string `json:"mounts_ro,omitempty"`

	// MountsRW are read-write mounts specific to this tool
	MountsRW []string `json:"mounts_rw,omitempty"`

	// Env specific to this tool (same format as Config.Env)
	Env []string `json:"env,omitempty"`

	// PreRunHooks are shell commands to run inside the container before this tool
	PreRunHooks []string `json:"pre_run_hooks,omitempty"`

	// PostBuildHooks are shell commands to run in the Dockerfile for this tool's stage
	PostBuildHooks []string `json:"post_build_hooks,omitempty"`
}

// RepoConfig represents configuration for a specific git repository.
// It is applied when any git remote URL contains the map key as a substring,
// allowing for prefix matching (e.g., "github.com/stellar" matches all stellar repos).
// When multiple patterns match, they are applied in order of specificity (shortest first),
// so more specific patterns override or extend less specific ones.
type RepoConfig struct {
	// Tool specifies which tool to use for this repository
	Tool string `json:"tool,omitempty"`

	// MountsRO are read-only mounts specific to this repository
	MountsRO []string `json:"mounts_ro,omitempty"`

	// MountsRW are read-write mounts specific to this repository
	MountsRW []string `json:"mounts_rw,omitempty"`

	// Env specific to this repository (same format as Config.Env)
	Env []string `json:"env,omitempty"`

	// PreRunHooks are shell commands to run inside the container before the tool
	PreRunHooks []string `json:"pre_run_hooks,omitempty"`

	// PostBuildHooks are shell commands to run in the Dockerfile
	PostBuildHooks []string `json:"post_build_hooks,omitempty"`
}

// SourceInfo tracks the source of configuration values
type SourceInfo struct {
	Backend            string                       // source path for backend setting
	Tool               string                       // source path for tool setting
	MountsRO           map[string]string            // value -> source path
	MountsRW           map[string]string            // value -> source path
	Env                map[string]string            // value -> source path
	PreRunHooks        map[string]string            // value -> source path
	PostBuildHooks     map[string]string            // value -> source path
	ToolMountsRO       map[string]map[string]string // tool -> value -> source
	ToolMountsRW       map[string]map[string]string // tool -> value -> source
	ToolEnv            map[string]map[string]string // tool -> value -> source
	ToolPreRunHooks    map[string]map[string]string // tool -> value -> source
	ToolPostBuildHooks map[string]map[string]string // tool -> value -> source
	RepoTool           map[string]string            // repo -> source path
	RepoMountsRO       map[string]map[string]string // repo -> value -> source
	RepoMountsRW       map[string]map[string]string // repo -> value -> source
	RepoEnv            map[string]map[string]string // repo -> value -> source
	RepoPreRunHooks    map[string]map[string]string // repo -> value -> source
	RepoPostBuildHooks map[string]map[string]string // repo -> value -> source
}

// ConfigPath represents a config file path with its status
type ConfigPath struct {
	Path   string
	Exists bool
}

// DefaultConfig returns the default configuration. toolDefaults supplies
// per-tool default configs (mounts, env, hooks) so the config package does
// not need to know about individual tools.
func DefaultConfig(toolDefaults map[string]ToolConfig) Config {
	tools := make(map[string]ToolConfig, len(toolDefaults))
	for k, v := range toolDefaults {
		tools[k] = v
	}
	return Config{
		MountsRO:       []string{},
		MountsRW:       []string{},
		Env:            []string{},
		PreRunHooks:    []string{},
		PostBuildHooks: []string{},
		Tools:          tools,
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

	// Backend: overlay takes precedence if set
	if overlay.Backend != "" {
		result.Backend = overlay.Backend
	}

	// Tool: overlay takes precedence if set
	if overlay.Tool != "" {
		result.Tool = overlay.Tool
	}

	// Append arrays
	result.MountsRO = append(result.MountsRO, overlay.MountsRO...)
	result.MountsRW = append(result.MountsRW, overlay.MountsRW...)
	result.Env = append(result.Env, overlay.Env...)
	result.PreRunHooks = append(result.PreRunHooks, overlay.PreRunHooks...)
	result.PostBuildHooks = append(result.PostBuildHooks, overlay.PostBuildHooks...)

	// Merge tools map
	if result.Tools == nil {
		result.Tools = make(map[string]ToolConfig)
	}
	for name, tool := range overlay.Tools {
		if existing, ok := result.Tools[name]; ok {
			existing.MountsRO = append(existing.MountsRO, tool.MountsRO...)
			existing.MountsRW = append(existing.MountsRW, tool.MountsRW...)
			existing.Env = append(existing.Env, tool.Env...)
			existing.PreRunHooks = append(existing.PreRunHooks, tool.PreRunHooks...)
			existing.PostBuildHooks = append(existing.PostBuildHooks, tool.PostBuildHooks...)
			result.Tools[name] = existing
		} else {
			result.Tools[name] = tool
		}
	}

	// Merge repos map
	if result.Repos == nil {
		result.Repos = make(map[string]RepoConfig)
	}
	for name, repo := range overlay.Repos {
		if existing, ok := result.Repos[name]; ok {
			existing.MountsRO = append(existing.MountsRO, repo.MountsRO...)
			existing.MountsRW = append(existing.MountsRW, repo.MountsRW...)
			existing.Env = append(existing.Env, repo.Env...)
			existing.PreRunHooks = append(existing.PreRunHooks, repo.PreRunHooks...)
			existing.PostBuildHooks = append(existing.PostBuildHooks, repo.PostBuildHooks...)
			result.Repos[name] = existing
		} else {
			result.Repos[name] = repo
		}
	}

	return result
}

// NewSourceInfo creates a new empty SourceInfo
func NewSourceInfo() *SourceInfo {
	return &SourceInfo{
		MountsRO:           make(map[string]string),
		MountsRW:           make(map[string]string),
		Env:                make(map[string]string),
		PreRunHooks:        make(map[string]string),
		PostBuildHooks:     make(map[string]string),
		ToolMountsRO:       make(map[string]map[string]string),
		ToolMountsRW:       make(map[string]map[string]string),
		ToolEnv:            make(map[string]map[string]string),
		ToolPreRunHooks:    make(map[string]map[string]string),
		ToolPostBuildHooks: make(map[string]map[string]string),
		RepoTool:           make(map[string]string),
		RepoMountsRO:       make(map[string]map[string]string),
		RepoMountsRW:       make(map[string]map[string]string),
		RepoEnv:            make(map[string]map[string]string),
		RepoPreRunHooks:    make(map[string]map[string]string),
		RepoPostBuildHooks: make(map[string]map[string]string),
	}
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

// LoadAll loads and merges all configuration files from XDG config home and current/parent directories.
// Missing or invalid config files are silently ignored - only defaults and valid configs are merged.
func LoadAll(toolDefaults map[string]ToolConfig) Config {
	cfg, _ := LoadAllWithSources(toolDefaults)
	return cfg
}

// LoadAllWithSources loads and merges all configs, tracking the source of each value
func LoadAllWithSources(toolDefaults map[string]ToolConfig) (Config, *SourceInfo) {
	cfg := DefaultConfig(toolDefaults)
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
	if cfg.Backend != "" {
		info.Backend = source
	}
	if cfg.Tool != "" {
		info.Tool = source
	}
	for _, v := range cfg.MountsRO {
		info.MountsRO[v] = source
	}
	for _, v := range cfg.MountsRW {
		info.MountsRW[v] = source
	}
	for _, v := range cfg.Env {
		info.Env[v] = source
	}
	for _, v := range cfg.PreRunHooks {
		info.PreRunHooks[v] = source
	}
	for _, v := range cfg.PostBuildHooks {
		info.PostBuildHooks[v] = source
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
		if info.ToolPreRunHooks[toolName] == nil {
			info.ToolPreRunHooks[toolName] = make(map[string]string)
		}
		if info.ToolPostBuildHooks[toolName] == nil {
			info.ToolPostBuildHooks[toolName] = make(map[string]string)
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
		for _, v := range toolCfg.PreRunHooks {
			info.ToolPreRunHooks[toolName][v] = source
		}
		for _, v := range toolCfg.PostBuildHooks {
			info.ToolPostBuildHooks[toolName][v] = source
		}
	}
	for repoName, repoCfg := range cfg.Repos {
		if repoCfg.Tool != "" {
			info.RepoTool[repoName] = source
		}
		if info.RepoMountsRO[repoName] == nil {
			info.RepoMountsRO[repoName] = make(map[string]string)
		}
		if info.RepoMountsRW[repoName] == nil {
			info.RepoMountsRW[repoName] = make(map[string]string)
		}
		if info.RepoEnv[repoName] == nil {
			info.RepoEnv[repoName] = make(map[string]string)
		}
		if info.RepoPreRunHooks[repoName] == nil {
			info.RepoPreRunHooks[repoName] = make(map[string]string)
		}
		if info.RepoPostBuildHooks[repoName] == nil {
			info.RepoPostBuildHooks[repoName] = make(map[string]string)
		}
		for _, v := range repoCfg.MountsRO {
			info.RepoMountsRO[repoName][v] = source
		}
		for _, v := range repoCfg.MountsRW {
			info.RepoMountsRW[repoName][v] = source
		}
		for _, v := range repoCfg.Env {
			info.RepoEnv[repoName][v] = source
		}
		for _, v := range repoCfg.PreRunHooks {
			info.RepoPreRunHooks[repoName][v] = source
		}
		for _, v := range repoCfg.PostBuildHooks {
			info.RepoPostBuildHooks[repoName][v] = source
		}
	}
}

// XDGConfigHomeDir returns XDG_CONFIG_HOME or the default ~/.config
func XDGConfigHomeDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(xdg.Home, ".config")
}

// XDGDataHomeDir returns XDG_DATA_HOME or the default ~/.local/share
func XDGDataHomeDir() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(xdg.Home, ".local", "share")
}

// XDGStateHomeDir returns XDG_STATE_HOME or the default ~/.local/state
func XDGStateHomeDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(xdg.Home, ".local", "state")
}
