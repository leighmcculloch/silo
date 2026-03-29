package cline

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

// Tool is the Cline CLI tool definition.
var Tool = tools.Tool{
	Name:            "cline",
	Description:     "Cline CLI - Cline's terminal coding agent",
	DockerfileStage: dockerfileStage,
	Command: func(home string) []string {
		return []string{home + "/.local/node/bin/cline", "--auto-approve-all"}
	},
	DefaultConfig: func() config.ToolConfig {
		return config.ToolConfig{
			MountsRW: []string{
				"~/.cline",
				"~/.claude.json",
				"~/.claude",
				"~/.codex",
			},
			Ports: []string{
				"3484:3485",
			},
			PreRunHooks: []string{
				"(socat TCP-LISTEN:3485,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:3484 &)",
			},
		}
	},
	LatestVersion: fetchLatestVersion,
}

// fetchLatestVersion queries the npm registry for the latest cline version.
func fetchLatestVersion(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/cline/latest", nil)
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
