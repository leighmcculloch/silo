package paperclipai

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"

	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tools"
)

//go:embed Dockerfile
var dockerfileStage string

// Tool is the Paperclip CLI tool definition.
var Tool = tools.Tool{
	Name:            "paperclipai",
	Description:     "Paperclip AI - Multi-agent orchestration CLI",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{home + "/.local/node/bin/paperclipai", "run"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.paperclip",
				"~/.claude.json",
				"~/.claude",
				"~/.codex",
			},
			Ports: []string{
				"3100:3101",
			},
			PreRunHooks: []string{
				"(socat TCP-LISTEN:3101,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:3100 &)",
			},
		}
	},
	LatestVersion: fetchLatestVersion,
}

// fetchLatestVersion queries the npm registry for the latest paperclipai
// version.
func fetchLatestVersion(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/paperclipai/latest", nil)
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

	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return ""
	}
	return pkg.Version
}
