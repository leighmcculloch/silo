package tools

import (
	"context"
	"encoding/json"
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
	Name            string                           // build target / config key (e.g. "claude")
	Description     string                           // human-readable (e.g. "Claude Code - Anthropic's CLI")
	DockerfileStage string                           // Dockerfile fragment (FROM base AS <name> ...)
	Command         func(home string) []string       // container entrypoint + args
	LatestVersion   func(ctx context.Context) string // optional: returns latest version string for cache-busting
}

// NewTool constructs a Tool from a name and ToolConfig. This is the bridge
// between the config-driven tool definitions and the runtime Tool type.
func NewTool(name string, tc config.ToolConfig) Tool {
	// Capture command slice so the closure doesn't share mutable state.
	cmdSlice := make([]string, len(tc.Command))
	copy(cmdSlice, tc.Command)

	t := Tool{
		Name:            name,
		Description:     tc.Description,
		DockerfileStage: tc.Dockerfile,
		Command: func(home string) []string {
			cmd := make([]string, len(cmdSlice))
			for i, c := range cmdSlice {
				cmd[i] = strings.ReplaceAll(c, "$HOME", home)
			}
			return cmd
		},
	}
	if tc.LatestVersionURL != "" {
		t.LatestVersion = FetchURLVersion(tc.LatestVersionURL)
	} else if tc.LatestVersionGitHubRelease != "" {
		t.LatestVersion = FetchGitHubReleaseVersion(tc.LatestVersionGitHubRelease)
	}
	return t
}

// ToolsFromConfig builds a slice of Tool from the tools map in config.
// Only tools with a Dockerfile defined are included (these are runnable tools).
func ToolsFromConfig(cfg config.Config) []Tool {
	var tt []Tool
	for name, tc := range cfg.Tools {
		if tc.Dockerfile == "" {
			continue
		}
		tt = append(tt, NewTool(name, tc))
	}
	return tt
}

// FetchVersion fetches the latest version and writes it to the cache. Intended
// to be called from a goroutine. Errors are silently ignored. If LatestVersion
// is nil the call is a no-op.
func (t Tool) FetchVersion(ctx context.Context) {
	if t.LatestVersion == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	version := t.LatestVersion(ctx)
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

// FetchURLVersion returns a LatestVersion function that fetches a URL and
// returns the trimmed response body as the version string.
func FetchURLVersion(url string) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return ""
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ""
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(body))
	}
}

// FetchGitHubReleaseVersion returns a LatestVersion function that queries
// the GitHub releases API for a repo (e.g. "github/copilot-cli") and returns
// the tag_name of the latest release.
func FetchGitHubReleaseVersion(repo string) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		url := "https://api.github.com/repos/" + repo + "/releases/latest"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return ""
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return ""
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return ""
		}

		var release struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &release); err != nil {
			return ""
		}
		return release.TagName
	}
}

var versionCachePath = func(tool string) string {
	return filepath.Join(xdg.CacheHome, "silo", "tool-versions", tool)
}
