package tools

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/leighmcculloch/silo/config"
)

// Tool defines a self-contained tool that can be run inside a silo container.
type Tool struct {
	Name            string                     // build target / config key (e.g. "claude")
	Description     string                     // human-readable (e.g. "Claude Code - Anthropic's CLI")
	DockerfileStage string                     // Dockerfile fragment (FROM base AS <name> ...)
	Command         func(home string) []string // container entrypoint + args
	DefaultConfig   func() config.ToolConfig   // default mounts/env/hooks
	VersionURL      string                     // optional latest-version URL for cache-busting
}

// FetchVersion fetches the latest version from VersionURL and writes it to the
// cache. Intended to be called from a goroutine. Errors are silently ignored.
// If VersionURL is empty the call is a no-op.
func (t Tool) FetchVersion(ctx context.Context) {
	if t.VersionURL == "" {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.VersionURL, nil)
	if err != nil {
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	version := strings.TrimSpace(string(body))
	if version == "" {
		return
	}

	p := versionCachePath(t.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(version), 0o644)
}

// CachedVersion reads the cached version for this tool. Returns "" if no cache
// exists.
func (t Tool) CachedVersion() string {
	data, err := os.ReadFile(versionCachePath(t.Name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

var versionCachePath = func(tool string) string {
	return filepath.Join(xdg.CacheHome, "silo", "tool-versions", tool)
}

// DefaultToolConfigs builds the map that config.DefaultConfig needs from a
// slice of tool definitions.
func DefaultToolConfigs(tt []Tool) map[string]config.ToolConfig {
	m := make(map[string]config.ToolConfig, len(tt))
	for _, t := range tt {
		if t.DefaultConfig != nil {
			m[t.Name] = t.DefaultConfig()
		}
	}
	return m
}
