package codexcli

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

// Tool is the OpenAI Codex CLI tool definition.
var Tool = tools.Tool{
	Name:            "codex",
	Description:     "Codex CLI - OpenAI's CLI coding agent",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{home + "/.local/node/bin/codex", "--dangerously-bypass-approvals-and-sandbox"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.codex",
			},
			Env: []string{
				"OPENAI_API_KEY",
			},
		}
	},
	LatestVersion: fetchLatestVersion,
}

// fetchLatestVersion queries the npm registry for the latest @openai/codex
// version.
func fetchLatestVersion(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/@openai/codex/latest", nil)
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
