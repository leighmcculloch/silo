package copilotcli

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the GitHub Copilot CLI tool definition.
var Tool = tools.Tool{
	Name:            "copilot",
	Description:     "GitHub Copilot CLI",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{"copilot", "--allow-all", "--disable-builtin-mcps"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				tilde.Path(filepath.Join(config.XDGConfigHomeDir(), ".copilot")),
			},
			MountsRO: []string{
				"~/.claude",
			},
			Env: []string{
				"COPILOT_GITHUB_TOKEN",
			},
		}
	},
	LatestVersion: fetchLatestRelease,
}

// fetchLatestRelease queries the GitHub releases API for the latest copilot-cli
// version tag.
func fetchLatestRelease(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/github/copilot-cli/releases/latest", nil)
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
