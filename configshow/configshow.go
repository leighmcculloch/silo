package configshow

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/charmbracelet/lipgloss"
	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/tilde"
)

// Show outputs the current merged configuration as JSONC with source comments.
func Show(stdout io.Writer) error {
	cfg, sources := config.LoadAllWithSources()

	// Check if stdout is a TTY for color output
	isTTY := false
	if f, ok := stdout.(*os.File); ok {
		stat, _ := f.Stat()
		isTTY = (stat.Mode() & os.ModeCharDevice) != 0
	}

	// Color styles for syntax highlighting
	keyStyle := lipgloss.NewStyle()
	stringStyle := lipgloss.NewStyle()
	commentStyle := lipgloss.NewStyle()
	if isTTY {
		keyStyle = keyStyle.Foreground(lipgloss.Color("6"))         // Cyan
		stringStyle = stringStyle.Foreground(lipgloss.Color("2"))   // Green
		commentStyle = commentStyle.Foreground(lipgloss.Color("8")) // Gray
	}

	// Helper functions for colored output
	key := func(k string) string {
		return keyStyle.Render(fmt.Sprintf("%q", k))
	}
	str := func(s string) string {
		return stringStyle.Render(fmt.Sprintf("%q", s))
	}
	comment := func(c string) string {
		return commentStyle.Render("// " + tilde.Path(c))
	}

	// Output JSONC with source comments
	fmt.Fprintln(stdout, "{")

	// Backend
	backendValue := cfg.Backend
	if backendValue == "" {
		backendValue = "docker"
	}
	backendSource := sources.Backend
	if backendSource == "" {
		backendSource = "default"
	}
	fmt.Fprintf(stdout, "  %s: %s, %s\n", key("backend"), str(backendValue), comment(backendSource))

	// Tool
	toolValue := cfg.Tool
	if toolValue == "" {
		toolValue = ""
	}
	toolSource := sources.Tool
	if toolSource == "" {
		toolSource = "default"
	}
	if toolValue != "" {
		fmt.Fprintf(stdout, "  %s: %s, %s\n", key("tool"), str(toolValue), comment(toolSource))
	} else {
		fmt.Fprintf(stdout, "  %s: null, %s\n", key("tool"), comment(toolSource))
	}

	// MountsRO
	fmt.Fprintf(stdout, "  %s: [\n", key("mounts_ro"))
	for i, v := range cfg.MountsRO {
		comma := ","
		if i == len(cfg.MountsRO)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %s%s %s\n", str(v), comma, comment(sources.MountsRO[v]))
	}
	fmt.Fprintln(stdout, "  ],")

	// MountsRW
	fmt.Fprintf(stdout, "  %s: [\n", key("mounts_rw"))
	for i, v := range cfg.MountsRW {
		comma := ","
		if i == len(cfg.MountsRW)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %s%s %s\n", str(v), comma, comment(sources.MountsRW[v]))
	}
	fmt.Fprintln(stdout, "  ],")

	// Env
	fmt.Fprintf(stdout, "  %s: [\n", key("env"))
	for i, v := range cfg.Env {
		comma := ","
		if i == len(cfg.Env)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %s%s %s\n", str(v), comma, comment(sources.Env[v]))
	}
	fmt.Fprintln(stdout, "  ],")

	// PostBuildHooks
	fmt.Fprintf(stdout, "  %s: [\n", key("post_build_hooks"))
	for i, v := range cfg.PostBuildHooks {
		comma := ","
		if i == len(cfg.PostBuildHooks)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %s%s %s\n", str(v), comma, comment(sources.PostBuildHooks[v]))
	}
	fmt.Fprintln(stdout, "  ],")

	// PreRunHooks
	fmt.Fprintf(stdout, "  %s: [\n", key("pre_run_hooks"))
	for i, v := range cfg.PreRunHooks {
		comma := ","
		if i == len(cfg.PreRunHooks)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %s%s %s\n", str(v), comma, comment(sources.PreRunHooks[v]))
	}
	fmt.Fprintln(stdout, "  ],")

	// Tools
	fmt.Fprintf(stdout, "  %s: {\n", key("tools"))
	toolNames := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		toolNames = append(toolNames, name)
	}
	slices.Sort(toolNames)

	for ti, toolName := range toolNames {
		toolCfg := cfg.Tools[toolName]
		fmt.Fprintf(stdout, "    %s: {\n", key(toolName))

		// Tool mounts_ro
		fmt.Fprintf(stdout, "      %s: [\n", key("mounts_ro"))
		for i, v := range toolCfg.MountsRO {
			comma := ","
			if i == len(toolCfg.MountsRO)-1 {
				comma = ""
			}
			source := sources.ToolMountsRO[toolName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Tool mounts_rw
		fmt.Fprintf(stdout, "      %s: [\n", key("mounts_rw"))
		for i, v := range toolCfg.MountsRW {
			comma := ","
			if i == len(toolCfg.MountsRW)-1 {
				comma = ""
			}
			source := sources.ToolMountsRW[toolName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Tool env
		fmt.Fprintf(stdout, "      %s: [\n", key("env"))
		for i, v := range toolCfg.Env {
			comma := ","
			if i == len(toolCfg.Env)-1 {
				comma = ""
			}
			source := sources.ToolEnv[toolName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Tool pre_run_hooks
		fmt.Fprintf(stdout, "      %s: [\n", key("pre_run_hooks"))
		for i, v := range toolCfg.PreRunHooks {
			comma := ","
			if i == len(toolCfg.PreRunHooks)-1 {
				comma = ""
			}
			source := sources.ToolPreRunHooks[toolName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Tool post_build_hooks
		fmt.Fprintf(stdout, "      %s: [\n", key("post_build_hooks"))
		for i, v := range toolCfg.PostBuildHooks {
			comma := ","
			if i == len(toolCfg.PostBuildHooks)-1 {
				comma = ""
			}
			source := sources.ToolPostBuildHooks[toolName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ]")

		toolComma := ","
		if ti == len(toolNames)-1 {
			toolComma = ""
		}
		fmt.Fprintf(stdout, "    }%s\n", toolComma)
	}
	fmt.Fprintln(stdout, "  },")

	// Repos
	fmt.Fprintf(stdout, "  %s: {\n", key("repos"))
	repoNames := make([]string, 0, len(cfg.Repos))
	for name := range cfg.Repos {
		repoNames = append(repoNames, name)
	}
	slices.Sort(repoNames)

	for ri, repoName := range repoNames {
		repoCfg := cfg.Repos[repoName]
		fmt.Fprintf(stdout, "    %s: {\n", key(repoName))

		// Repo tool
		toolSource := sources.RepoTool[repoName]
		if toolSource == "" {
			toolSource = "default"
		}
		if repoCfg.Tool != "" {
			fmt.Fprintf(stdout, "      %s: %s, %s\n", key("tool"), str(repoCfg.Tool), comment(toolSource))
		} else {
			fmt.Fprintf(stdout, "      %s: null, %s\n", key("tool"), comment(toolSource))
		}

		// Repo mounts_ro
		fmt.Fprintf(stdout, "      %s: [\n", key("mounts_ro"))
		for i, v := range repoCfg.MountsRO {
			comma := ","
			if i == len(repoCfg.MountsRO)-1 {
				comma = ""
			}
			source := sources.RepoMountsRO[repoName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Repo mounts_rw
		fmt.Fprintf(stdout, "      %s: [\n", key("mounts_rw"))
		for i, v := range repoCfg.MountsRW {
			comma := ","
			if i == len(repoCfg.MountsRW)-1 {
				comma = ""
			}
			source := sources.RepoMountsRW[repoName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Repo env
		fmt.Fprintf(stdout, "      %s: [\n", key("env"))
		for i, v := range repoCfg.Env {
			comma := ","
			if i == len(repoCfg.Env)-1 {
				comma = ""
			}
			source := sources.RepoEnv[repoName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Repo pre_run_hooks
		fmt.Fprintf(stdout, "      %s: [\n", key("pre_run_hooks"))
		for i, v := range repoCfg.PreRunHooks {
			comma := ","
			if i == len(repoCfg.PreRunHooks)-1 {
				comma = ""
			}
			source := sources.RepoPreRunHooks[repoName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ],")

		// Repo post_build_hooks
		fmt.Fprintf(stdout, "      %s: [\n", key("post_build_hooks"))
		for i, v := range repoCfg.PostBuildHooks {
			comma := ","
			if i == len(repoCfg.PostBuildHooks)-1 {
				comma = ""
			}
			source := sources.RepoPostBuildHooks[repoName][v]
			fmt.Fprintf(stdout, "        %s%s %s\n", str(v), comma, comment(source))
		}
		fmt.Fprintln(stdout, "      ]")

		repoComma := ","
		if ri == len(repoNames)-1 {
			repoComma = ""
		}
		fmt.Fprintf(stdout, "    }%s\n", repoComma)
	}
	fmt.Fprintln(stdout, "  }")

	fmt.Fprintln(stdout, "}")
	return nil
}

// Default outputs the default configuration as JSON.
func Default(stdout io.Writer) error {
	cfg := config.DefaultConfig()

	// Output as JSON
	fmt.Fprintln(stdout, "{")

	// MountsRO
	fmt.Fprintln(stdout, "  \"mounts_ro\": [")
	for i, v := range cfg.MountsRO {
		comma := ","
		if i == len(cfg.MountsRO)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %q%s\n", v, comma)
	}
	fmt.Fprintln(stdout, "  ],")

	// MountsRW
	fmt.Fprintln(stdout, "  \"mounts_rw\": [")
	for i, v := range cfg.MountsRW {
		comma := ","
		if i == len(cfg.MountsRW)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %q%s\n", v, comma)
	}
	fmt.Fprintln(stdout, "  ],")

	// Env
	fmt.Fprintln(stdout, "  \"env\": [")
	for i, v := range cfg.Env {
		comma := ","
		if i == len(cfg.Env)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %q%s\n", v, comma)
	}
	fmt.Fprintln(stdout, "  ],")

	// PostBuildHooks
	fmt.Fprintln(stdout, "  \"post_build_hooks\": [")
	for i, v := range cfg.PostBuildHooks {
		comma := ","
		if i == len(cfg.PostBuildHooks)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %q%s\n", v, comma)
	}
	fmt.Fprintln(stdout, "  ],")

	// PreRunHooks
	fmt.Fprintln(stdout, "  \"pre_run_hooks\": [")
	for i, v := range cfg.PreRunHooks {
		comma := ","
		if i == len(cfg.PreRunHooks)-1 {
			comma = ""
		}
		fmt.Fprintf(stdout, "    %q%s\n", v, comma)
	}
	fmt.Fprintln(stdout, "  ],")

	// Tools
	fmt.Fprintln(stdout, "  \"tools\": {")
	toolNames := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		toolNames = append(toolNames, name)
	}
	slices.Sort(toolNames)

	for ti, toolName := range toolNames {
		toolCfg := cfg.Tools[toolName]
		fmt.Fprintf(stdout, "    %q: {\n", toolName)

		fmt.Fprintln(stdout, "      \"mounts_ro\": [")
		for i, v := range toolCfg.MountsRO {
			comma := ","
			if i == len(toolCfg.MountsRO)-1 {
				comma = ""
			}
			fmt.Fprintf(stdout, "        %q%s\n", v, comma)
		}
		fmt.Fprintln(stdout, "      ],")

		fmt.Fprintln(stdout, "      \"mounts_rw\": [")
		for i, v := range toolCfg.MountsRW {
			comma := ","
			if i == len(toolCfg.MountsRW)-1 {
				comma = ""
			}
			fmt.Fprintf(stdout, "        %q%s\n", v, comma)
		}
		fmt.Fprintln(stdout, "      ],")

		fmt.Fprintln(stdout, "      \"env\": [")
		for i, v := range toolCfg.Env {
			comma := ","
			if i == len(toolCfg.Env)-1 {
				comma = ""
			}
			fmt.Fprintf(stdout, "        %q%s\n", v, comma)
		}
		fmt.Fprintln(stdout, "      ],")

		fmt.Fprintln(stdout, "      \"pre_run_hooks\": [")
		for i, v := range toolCfg.PreRunHooks {
			comma := ","
			if i == len(toolCfg.PreRunHooks)-1 {
				comma = ""
			}
			fmt.Fprintf(stdout, "        %q%s\n", v, comma)
		}
		fmt.Fprintln(stdout, "      ],")

		fmt.Fprintln(stdout, "      \"post_build_hooks\": [")
		for i, v := range toolCfg.PostBuildHooks {
			comma := ","
			if i == len(toolCfg.PostBuildHooks)-1 {
				comma = ""
			}
			fmt.Fprintf(stdout, "        %q%s\n", v, comma)
		}
		fmt.Fprintln(stdout, "      ]")

		toolComma := ","
		if ti == len(toolNames)-1 {
			toolComma = ""
		}
		fmt.Fprintf(stdout, "    }%s\n", toolComma)
	}
	fmt.Fprintln(stdout, "  },")

	// Repos (empty by default)
	fmt.Fprintln(stdout, "  \"repos\": {}")

	fmt.Fprintln(stdout, "}")
	return nil
}
