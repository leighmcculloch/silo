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

// writer handles JSONC output with optional syntax highlighting and source comments.
type writer struct {
	w   io.Writer
	key func(string) string // renders a JSON key (with optional color)
	str func(string) string // renders a JSON string value (with optional color)
	src func(string) string // renders a source comment; nil = no comments
}

// suffix returns the trailing comma and optional source comment for a value.
func (w *writer) suffix(source string, comma bool) string {
	s := ""
	if comma {
		s = ","
	}
	if w.src != nil {
		s += " " + w.src(source)
	}
	return s
}

// stringField writes a JSON string field: "key": "value"[, // source]
func (w *writer) stringField(indent, name, value, source string, comma bool) {
	fmt.Fprintf(w.w, "%s%s: %s%s\n", indent, w.key(name), w.str(value), w.suffix(source, comma))
}

// nullableString writes a JSON string field that may be null.
func (w *writer) nullableString(indent, name, value, source string, comma bool) {
	if value != "" {
		w.stringField(indent, name, value, source, comma)
	} else {
		fmt.Fprintf(w.w, "%s%s: null%s\n", indent, w.key(name), w.suffix(source, comma))
	}
}

// array writes a JSON array field with optional per-element source comments.
func (w *writer) array(indent, name string, values []string, sources map[string]string, comma bool) {
	fmt.Fprintf(w.w, "%s%s: [\n", indent, w.key(name))
	for i, v := range values {
		src := ""
		if sources != nil {
			src = sources[v]
		}
		fmt.Fprintf(w.w, "%s  %s%s\n", indent, w.str(v), w.suffix(src, i < len(values)-1))
	}
	c := ""
	if comma {
		c = ","
	}
	fmt.Fprintf(w.w, "%s]%s\n", indent, c)
}

// openObject writes the opening of a JSON object field.
func (w *writer) openObject(indent, name string) {
	fmt.Fprintf(w.w, "%s%s: {\n", indent, w.key(name))
}

// closeObject writes the closing brace of a JSON object.
func (w *writer) closeObject(indent string, comma bool) {
	c := ""
	if comma {
		c = ","
	}
	fmt.Fprintf(w.w, "%s}%s\n", indent, c)
}

func newShowWriter(stdout io.Writer) *writer {
	isTTY := false
	if f, ok := stdout.(*os.File); ok {
		stat, _ := f.Stat()
		isTTY = (stat.Mode() & os.ModeCharDevice) != 0
	}

	keyStyle := lipgloss.NewStyle()
	stringStyle := lipgloss.NewStyle()
	commentStyle := lipgloss.NewStyle()
	if isTTY {
		keyStyle = keyStyle.Foreground(lipgloss.Color("6"))         // Cyan
		stringStyle = stringStyle.Foreground(lipgloss.Color("2"))   // Green
		commentStyle = commentStyle.Foreground(lipgloss.Color("8")) // Gray
	}

	return &writer{
		w:   stdout,
		key: func(k string) string { return keyStyle.Render(fmt.Sprintf("%q", k)) },
		str: func(s string) string { return stringStyle.Render(fmt.Sprintf("%q", s)) },
		src: func(s string) string { return commentStyle.Render("// " + tilde.Path(s)) },
	}
}

func newDefaultWriter(stdout io.Writer) *writer {
	return &writer{
		w:   stdout,
		key: func(k string) string { return fmt.Sprintf("%q", k) },
		str: func(s string) string { return fmt.Sprintf("%q", s) },
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func def(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Show outputs the current merged configuration as JSONC with source comments.
func Show(stdout io.Writer) error {
	cfg, src := config.LoadAllWithSources()
	w := newShowWriter(stdout)

	fmt.Fprintln(stdout, "{")

	w.stringField("  ", "backend", def(cfg.Backend, "docker"), def(src.Backend, "default"), true)
	w.nullableString("  ", "tool", cfg.Tool, def(src.Tool, "default"), true)
	w.array("  ", "mounts_ro", cfg.MountsRO, src.MountsRO, true)
	w.array("  ", "mounts_rw", cfg.MountsRW, src.MountsRW, true)
	w.array("  ", "env", cfg.Env, src.Env, true)
	w.array("  ", "post_build_hooks", cfg.PostBuildHooks, src.PostBuildHooks, true)
	w.array("  ", "pre_run_hooks", cfg.PreRunHooks, src.PreRunHooks, true)

	// Tools
	toolNames := sortedKeys(cfg.Tools)
	w.openObject("  ", "tools")
	for ti, tn := range toolNames {
		tc := cfg.Tools[tn]
		w.openObject("    ", tn)
		w.array("      ", "mounts_ro", tc.MountsRO, src.ToolMountsRO[tn], true)
		w.array("      ", "mounts_rw", tc.MountsRW, src.ToolMountsRW[tn], true)
		w.array("      ", "env", tc.Env, src.ToolEnv[tn], true)
		w.array("      ", "pre_run_hooks", tc.PreRunHooks, src.ToolPreRunHooks[tn], true)
		w.array("      ", "post_build_hooks", tc.PostBuildHooks, src.ToolPostBuildHooks[tn], false)
		w.closeObject("    ", ti < len(toolNames)-1)
	}
	w.closeObject("  ", true)

	// Repos
	repoNames := sortedKeys(cfg.Repos)
	w.openObject("  ", "repos")
	for ri, rn := range repoNames {
		rc := cfg.Repos[rn]
		w.openObject("    ", rn)
		w.nullableString("      ", "tool", rc.Tool, def(src.RepoTool[rn], "default"), true)
		w.array("      ", "mounts_ro", rc.MountsRO, src.RepoMountsRO[rn], true)
		w.array("      ", "mounts_rw", rc.MountsRW, src.RepoMountsRW[rn], true)
		w.array("      ", "env", rc.Env, src.RepoEnv[rn], true)
		w.array("      ", "pre_run_hooks", rc.PreRunHooks, src.RepoPreRunHooks[rn], true)
		w.array("      ", "post_build_hooks", rc.PostBuildHooks, src.RepoPostBuildHooks[rn], false)
		w.closeObject("    ", ri < len(repoNames)-1)
	}
	w.closeObject("  ", false)

	fmt.Fprintln(stdout, "}")
	return nil
}

// Default outputs the default configuration as JSON.
func Default(stdout io.Writer) error {
	cfg := config.DefaultConfig()
	w := newDefaultWriter(stdout)

	fmt.Fprintln(stdout, "{")

	w.array("  ", "mounts_ro", cfg.MountsRO, nil, true)
	w.array("  ", "mounts_rw", cfg.MountsRW, nil, true)
	w.array("  ", "env", cfg.Env, nil, true)
	w.array("  ", "post_build_hooks", cfg.PostBuildHooks, nil, true)
	w.array("  ", "pre_run_hooks", cfg.PreRunHooks, nil, true)

	// Tools
	toolNames := sortedKeys(cfg.Tools)
	w.openObject("  ", "tools")
	for ti, tn := range toolNames {
		tc := cfg.Tools[tn]
		w.openObject("    ", tn)
		w.array("      ", "mounts_ro", tc.MountsRO, nil, true)
		w.array("      ", "mounts_rw", tc.MountsRW, nil, true)
		w.array("      ", "env", tc.Env, nil, true)
		w.array("      ", "pre_run_hooks", tc.PreRunHooks, nil, true)
		w.array("      ", "post_build_hooks", tc.PostBuildHooks, nil, false)
		w.closeObject("    ", ti < len(toolNames)-1)
	}
	w.closeObject("  ", true)

	// Repos (empty by default)
	fmt.Fprintf(stdout, "  %s: {}\n", w.key("repos"))

	fmt.Fprintln(stdout, "}")
	return nil
}
