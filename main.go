package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/mattn/go-isatty"

	"github.com/leighmcculloch/silo/backend"
	applecontainer "github.com/leighmcculloch/silo/backend/container"
	"github.com/leighmcculloch/silo/backend/docker"
	flybackend "github.com/leighmcculloch/silo/backend/fly"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/configshow"
	"github.com/leighmcculloch/silo/run"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/leighmcculloch/silo/tools"
	"github.com/leighmcculloch/silo/tools/claudecode"
	"github.com/leighmcculloch/silo/tools/cline"
	"github.com/leighmcculloch/silo/tools/codexcli"
	"github.com/leighmcculloch/silo/tools/copilotcli"
	"github.com/leighmcculloch/silo/tools/mistralvibe"
	"github.com/leighmcculloch/silo/tools/opencode"
	"github.com/leighmcculloch/silo/tools/paperclipai"
	"github.com/spf13/cobra"
)

var (
	version = "dev"

	// supportedTools is the single source of truth for which tools silo
	// supports. To add a tool: create tools/<name>/, define its Tool, and
	// add it here. To remove a tool: delete from this slice.
	supportedTools = []tools.Tool{
		claudecode.Tool,
		cline.Tool,
		codexcli.Tool,
		opencode.Tool,
		paperclipai.Tool,
		copilotcli.Tool,
		mistralvibe.Tool,
	}
)

// toolDefaults returns the default ToolConfig map derived from supportedTools.
func toolDefaults() map[string]config.ToolConfig {
	return tools.DefaultToolConfigs(supportedTools)
}

// findTool returns the Tool definition for the given name, or nil if not found.
func findTool(name string) *tools.Tool {
	for i := range supportedTools {
		if supportedTools[i].Name == name {
			return &supportedTools[i]
		}
	}
	return nil
}

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// runMain is the main entry point that can be called by tests
func runMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rootCmd := newRootCmd(stdout, stderr)
	rootCmd.SetArgs(args)
	rootCmd.SetIn(stdin)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	if err := rootCmd.Execute(); err != nil {
		if !hasQuietArg(args) && isStdoutTTY(stderr) {
			cli.LogErrorTo(stderr, "%v", err)
		}
		return 1
	}
	return 0
}

func hasQuietArg(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--quiet" || arg == "-q" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "q") {
			return true
		}
	}
	return false
}

func isStdoutTTY(stdout io.Writer) bool {
	if f, ok := stdout.(*os.File); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
}

func boolFlag(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	if f := cmd.Flags().Lookup(name); f != nil {
		if q, err := cmd.Flags().GetBool(name); err == nil && q {
			return true
		}
	}
	if root := cmd.Root(); root != nil && root != cmd {
		if f := root.Flags().Lookup(name); f != nil {
			if q, err := root.Flags().GetBool(name); err == nil && q {
				return true
			}
		}
	}
	return false
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "silo",
		Short: "Run AI coding tools in isolated Docker containers",
		Long: lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(`
  ███████╗██╗██╗      ██████╗
  ██╔════╝██║██║     ██╔═══██╗
  ███████╗██║██║     ██║   ██║
  ╚════██║██║██║     ██║   ██║
  ███████║██║███████╗╚██████╔╝
  ╚══════╝╚═╝╚══════╝ ╚═════╝
`) + `
Run AI tools (Claude Code, Cline, Codex, OpenCode, Paperclip AI, Copilot, Mistral Vibe) in isolated
Docker containers with proper security sandboxing.

The container is configured with:
  • Your current directory mounted as the working directory
  • Git identity from your host machine
  • Tool-specific configuration directories
  • API keys from configured key files

Configuration is loaded from (in order, merged):
  1. ~/.config/silo/config.json (global)
  2. .silo.json files from root to current directory (local)
`,
		Example: `  # Interactive tool selection
  silo

  # Run a specific tool
  silo claude
  silo cline
  silo codex
  silo opencode
  silo paperclipai
  silo copilot
  silo vibe

  # Pass arguments to the tool
  silo claude -- --help`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSilo(cmd, args, stdout, stderr)
		},
	}

	rootCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly")
	rootCmd.Flags().Bool("force-build", false, "Force rebuild of container image")
	rootCmd.Flags().Bool("no-cache", false, "Disable build cache (implies --force-build)")
	rootCmd.Flags().BoolP("verbose", "v", false, "Show detailed output instead of progress bar")
	rootCmd.Flags().BoolP("quiet", "q", false, "Suppress silo logs and render only tool output")
	rootCmd.Flags().Bool("no-tty", false, "Run without allocating a TTY; suitable for scripts and JSON output")
	rootCmd.Flags().String("tool-version", "", "Pin a specific tool version (forces synchronous build)")
	addMountFlags(rootCmd)

	// Define command groups (order here determines display order in --help)
	rootCmd.AddGroup(
		&cobra.Group{ID: "tools", Title: "Tools:"},
		&cobra.Group{ID: "container", Title: "Container Commands:"},
		&cobra.Group{ID: "config", Title: "Configuration:"},
	)

	// Register each tool as a subcommand
	for _, t := range supportedTools {
		toolDef := t // capture loop variable
		toolCmd := &cobra.Command{
			Use:     toolDef.Name + " [-- args...]",
			Short:   toolDef.Description,
			GroupID: "tools",
			Args:    cobra.ArbitraryArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runTool(cmd, toolDef, args, stdout, stderr)
			},
		}
		toolCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly")
		toolCmd.Flags().Bool("force-build", false, "Force rebuild of container image")
		toolCmd.Flags().Bool("no-cache", false, "Disable build cache (implies --force-build)")
		toolCmd.Flags().BoolP("verbose", "v", false, "Show detailed output instead of progress bar")
		toolCmd.Flags().BoolP("quiet", "q", false, "Suppress silo logs and render only tool output")
		toolCmd.Flags().Bool("no-tty", false, "Run without allocating a TTY; suitable for scripts and JSON output")
		toolCmd.Flags().String("entrypoint", "", "Run a custom command instead of the tool (e.g. /bin/bash)")
		toolCmd.Flags().String("tool-version", "", "Pin a specific tool version (forces synchronous build)")
		addMountFlags(toolCmd)
		rootCmd.AddCommand(toolCmd)
	}

	configCmd := &cobra.Command{
		Use:     "config",
		Short:   "Configuration management commands",
		GroupID: "config",
	}

	configShowCmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current merged configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return configshow.Show(stdout, toolDefaults())
		},
	}

	configPathsCmd := &cobra.Command{
		Use:   "paths",
		Short: "Show all config file paths being merged",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigPaths(cmd, args, stdout)
		},
	}

	configEditCmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit a config file in your editor",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigEdit(cmd, args, stdout, stderr)
		},
	}

	configDefaultCmd := &cobra.Command{
		Use:   "default",
		Short: "Show the default configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return configshow.Default(stdout, toolDefaults())
		},
	}

	configInitCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a sample configuration file",
		Long: `Create a sample silo configuration file.

By default, an interactive prompt lets you choose between local and global config.
Use --local or --global to skip the prompt.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			globalFlag, _ := cmd.Flags().GetBool("global")
			localFlag, _ := cmd.Flags().GetBool("local")
			return runInit(cmd, args, stderr, globalFlag, localFlag)
		},
	}
	configInitCmd.Flags().BoolP("global", "g", false, fmt.Sprintf("Create global config (%s)", tilde.Path(filepath.Join(config.XDGConfigHome(), "silo", "silo.jsonc"))))
	configInitCmd.Flags().BoolP("local", "l", false, "Create local config (silo.jsonc)")
	configInitCmd.MarkFlagsMutuallyExclusive("global", "local")

	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configPathsCmd)
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configDefaultCmd)
	configCmd.AddCommand(configInitCmd)

	rootCmd.AddCommand(configCmd)

	logsCmd := &cobra.Command{
		Use:     "logs",
		Short:   "Browse build logs from previous runs",
		GroupID: "config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(stdout, stderr)
		},
	}
	rootCmd.AddCommand(logsCmd)

	lsCmd := &cobra.Command{
		Use:     "ls",
		Short:   "List all silo-created containers",
		GroupID: "container",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args, stdout, stderr)
		},
	}
	lsCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly (default: all)")
	lsCmd.Flags().BoolP("quiet", "q", false, "Only display container names")
	rootCmd.AddCommand(lsCmd)

	rmCmd := &cobra.Command{
		Use:     "rm [container...]",
		Short:   "Remove silo containers",
		GroupID: "container",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd, args, stderr)
		},
	}
	rmCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly (default: all)")
	rmCmd.Flags().BoolP("force", "f", false, "Force removal of running containers")
	rootCmd.AddCommand(rmCmd)

	execCmd := &cobra.Command{
		Use:     "exec [container] [command] [args...]",
		Short:   "Run a command in a running silo container",
		GroupID: "container",
		Long:    `Execute an arbitrary command inside a running silo container with an interactive TTY.`,
		Example: `  # Run bash in a container
  silo exec silo-myproject-1 /bin/bash

  # Run a specific command
  silo exec silo-myproject-1 ls -la /app`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeContainerNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd, args[0], args[1:], stderr)
		},
	}
	execCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly (default: all)")
	rootCmd.AddCommand(execCmd)

	shellCmd := &cobra.Command{
		Use:               "shell [container]",
		Short:             "Open a shell in a running silo container",
		GroupID:           "container",
		Long:              `Open an interactive /bin/bash shell inside a running silo container.`,
		Example:           `  silo shell silo-myproject-1`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeContainerNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd, args[0], []string{"/bin/bash"}, stderr)
		},
	}
	shellCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly (default: all)")
	rootCmd.AddCommand(shellCmd)

	reconnectCmd := &cobra.Command{
		Use:               "reconnect [container]",
		Short:             "Reconnect to a running silo container's tool session",
		GroupID:           "container",
		Long:              `Reconnect to a running silo container's tool session. Re-syncs files and reattaches to the running tool (e.g., claude, copilot).`,
		Example:           `  silo reconnect silo-1`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeContainerNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReconnect(cmd, args[0], stderr)
		},
	}
	reconnectCmd.Flags().StringP("backend", "b", "", "Backend to use: docker, container, fly (default: all)")
	rootCmd.AddCommand(reconnectCmd)

	// Hidden __build subcommand — used by background build launcher.
	buildCmd := &cobra.Command{
		Use:    "__build",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := cmd.Flags().GetString("dir")
			return runBackgroundBuild(dir, stderr)
		},
	}
	buildCmd.Flags().String("dir", "", "Build state directory")
	rootCmd.AddCommand(buildCmd)

	upgradeCmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade silo to the latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(stderr)
		},
	}
	rootCmd.AddCommand(upgradeCmd)

	rootCmd.Version = version
	rootCmd.SetVersionTemplate("silo version {{.Version}}\n")

	return rootCmd
}

func runSilo(cmd *cobra.Command, args []string, stdout, stderr io.Writer) error {
	// Load configuration
	cfg := config.LoadAll(toolDefaults())
	noTTY := boolFlag(cmd, "no-tty") || !isStdoutTTY(stdout)

	// Get tool-specific args (everything after --)
	var toolArgs []string
	if cmd.ArgsLenAtDash() > -1 {
		toolArgs = args[cmd.ArgsLenAtDash():]
	}

	// Get cwd for repo matching
	cwd, _ := os.Getwd()

	// Determine tool (priority: repo config > global config > interactive)
	var tool string
	var err error

	// Check repo-specific tool setting (applied in specificity order)
	for _, m := range run.GetMatchingRepos(cfg, cwd) {
		if m.Config.Tool != "" {
			tool = m.Config.Tool
		}
	}
	// Fall back to global config tool
	if tool == "" && cfg.Tool != "" {
		tool = cfg.Tool
	}
	// Interactive selection as last resort
	if tool == "" {
		if noTTY {
			return fmt.Errorf("--no-tty requires an explicit tool or configured default tool")
		}
		tool, err = selectTool()
		if err != nil {
			return err
		}
	}

	// Validate tool
	validTools := AvailableTools(supportedTools)
	if !slices.Contains(validTools, tool) {
		return fmt.Errorf("invalid tool: %s (valid tools: %s)", tool, strings.Join(validTools, ", "))
	}

	// Find tool definition
	toolDef := findTool(tool)
	if toolDef == nil {
		return fmt.Errorf("tool definition not found: %s", tool)
	}

	// Override backend from flag
	if b, _ := cmd.Flags().GetString("backend"); b != "" {
		cfg.Backend = b
	}

	// Get force-build flag
	forceBuild, _ := cmd.Flags().GetBool("force-build")

	// Get no-cache flag (implies force-build)
	noCache, _ := cmd.Flags().GetBool("no-cache")
	if noCache {
		forceBuild = true
	}

	// Get verbose flag
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Get quiet flag
	quiet := boolFlag(cmd, "quiet")

	// Get no-tty flag
	noTTY = boolFlag(cmd, "no-tty")

	// Auto-detect non-interactive environment
	if !noTTY && !isStdoutTTY(stdout) {
		noTTY = true
		quiet = true
	}

	// Get tool-version flag
	toolVersion, _ := cmd.Flags().GetString("tool-version")

	// Get additional mount flags
	extraMountsRW, extraMountsRO, err := extraMountsFromFlags(cmd)
	if err != nil {
		return err
	}

	// Run the tool
	return run.Tool(run.Options{
		ToolDef:       *toolDef,
		ToolArgs:      toolArgs,
		Config:        cfg,
		Dockerfile:    Dockerfile(*toolDef),
		ForceBuild:    forceBuild,
		NoCache:       noCache,
		Verbose:       verbose,
		Quiet:         quiet,
		NoTTY:         noTTY,
		ToolVersion:   toolVersion,
		ExtraMountsRW: extraMountsRW,
		ExtraMountsRO: extraMountsRO,
		Stdin:         cmd.InOrStdin(),
		Stdout:        stdout,
		Stderr:        stderr,
	})
}

func runTool(cmd *cobra.Command, toolDef tools.Tool, args []string, stdout, stderr io.Writer) error {
	// Load configuration
	cfg := config.LoadAll(toolDefaults())

	// Get tool-specific args (everything after --)
	var toolArgs []string
	if cmd.ArgsLenAtDash() > -1 {
		toolArgs = args[cmd.ArgsLenAtDash():]
	}

	// Override backend from flag
	if b, _ := cmd.Flags().GetString("backend"); b != "" {
		cfg.Backend = b
	}

	// Get force-build flag
	forceBuild, _ := cmd.Flags().GetBool("force-build")

	// Get no-cache flag (implies force-build)
	noCache, _ := cmd.Flags().GetBool("no-cache")
	if noCache {
		forceBuild = true
	}

	// Get verbose flag
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Get quiet flag
	quiet := boolFlag(cmd, "quiet")

	// Get no-tty flag
	noTTY := boolFlag(cmd, "no-tty")

	// Auto-detect non-interactive environment
	if !noTTY && !isStdoutTTY(stdout) {
		noTTY = true
		quiet = true
	}

	// Get entrypoint flag
	entrypoint, _ := cmd.Flags().GetString("entrypoint")

	// Get tool-version flag
	toolVersion, _ := cmd.Flags().GetString("tool-version")

	// Get additional mount flags
	extraMountsRW, extraMountsRO, err := extraMountsFromFlags(cmd)
	if err != nil {
		return err
	}

	// Run the tool
	return run.Tool(run.Options{
		ToolDef:       toolDef,
		ToolArgs:      toolArgs,
		Config:        cfg,
		Dockerfile:    Dockerfile(toolDef),
		ForceBuild:    forceBuild,
		NoCache:       noCache,
		Verbose:       verbose,
		Quiet:         quiet,
		NoTTY:         noTTY,
		Entrypoint:    entrypoint,
		ToolVersion:   toolVersion,
		ExtraMountsRW: extraMountsRW,
		ExtraMountsRO: extraMountsRO,
		Stdin:         cmd.InOrStdin(),
		Stdout:        stdout,
		Stderr:        stderr,
	})
}

func addMountFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayP("mount", "m", nil, "Additional read-write path to mount into the container (repeatable)")
	cmd.Flags().StringArray("mountro", nil, "Additional read-only path to mount into the container (repeatable)")
}

func extraMountsFromFlags(cmd *cobra.Command) (mountsRW, mountsRO []string, err error) {
	mountsRW, err = cmd.Flags().GetStringArray("mount")
	if err != nil {
		return nil, nil, err
	}
	mountsRO, err = cmd.Flags().GetStringArray("mountro")
	if err != nil {
		return nil, nil, err
	}
	return mountsRW, mountsRO, nil
}

func selectTool() (string, error) {
	names := AvailableTools(supportedTools)

	var options []huh.Option[string]
	for _, t := range names {
		options = append(options, huh.NewOption(ToolDescription(supportedTools, t), t))
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select AI Tool").
				Description("Choose which AI coding assistant to run").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", fmt.Errorf("selection cancelled")
	}

	return selected, nil
}

func runConfigPaths(_ *cobra.Command, _ []string, stdout io.Writer) error {
	paths := config.GetConfigPaths()

	for _, p := range paths {
		if p.Exists {
			fmt.Fprintln(stdout, p.Path)
		}
	}

	return nil
}

func runConfigEdit(_ *cobra.Command, _ []string, _, stderr io.Writer) error {
	paths := config.GetConfigPaths()

	// Build options for the selector:
	// - Always include global config (first path)
	// - Only include local configs that exist
	var options []huh.Option[string]
	for i, p := range paths {
		isGlobal := i == 0
		if !isGlobal && !p.Exists {
			continue
		}
		label := p.Path
		if !p.Exists {
			label += " (new)"
		}
		options = append(options, huh.NewOption(label, p.Path))
	}

	var selectedPath string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select Config to Edit").
				Description("Configs are merged in order shown (later overrides earlier)").
				Options(options...).
				Value(&selectedPath),
		),
	)

	if err := form.Run(); err != nil {
		return fmt.Errorf("selection cancelled")
	}

	// Get editor from environment
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	// Ensure parent directory exists for new files
	dir := filepath.Dir(selectedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// If file doesn't exist, pre-fill with template
	if _, err := os.Stat(selectedPath); os.IsNotExist(err) {
		if err := os.WriteFile(selectedPath, []byte(sampleConfig), 0644); err != nil {
			return fmt.Errorf("failed to create config: %w", err)
		}
	}

	// Open editor
	cmd := exec.Command(editor, selectedPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	return nil
}

func runInit(_ *cobra.Command, _ []string, stderr io.Writer, globalFlag, localFlag bool) error {
	var configType string

	// Determine config type from flags or interactive prompt
	if globalFlag {
		configType = "global"
	} else if localFlag {
		configType = "local"
	} else {
		// Interactive selection
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Create Configuration").
					Description("Choose which configuration file to create").
					Options(
						huh.NewOption("Local (silo.jsonc in current directory)", "local"),
						huh.NewOption(fmt.Sprintf("Global (%s)", tilde.Path(filepath.Join(config.XDGConfigHome(), "silo", "silo.jsonc"))), "global"),
					).
					Value(&configType),
			),
		)

		if err := form.Run(); err != nil {
			return fmt.Errorf("selection cancelled")
		}
	}

	var configPath string
	if configType == "global" {
		configDir := filepath.Join(config.XDGConfigHome(), "silo")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
		configPath = filepath.Join(configDir, "silo.jsonc")
	} else {
		configPath = "silo.jsonc"
	}

	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists: %s", configPath)
	}

	if err := os.WriteFile(configPath, []byte(sampleConfig), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	cli.LogSuccessTo(stderr, "Created %s", configPath)
	return nil
}

func runLogs(stdout, stderr io.Writer) error {
	logDir := filepath.Join(config.XDGStateHomeDir(), "silo", "logs")

	entries, err := os.ReadDir(logDir)
	if err != nil {
		return fmt.Errorf("no logs found (looked in %s)", tilde.Path(logDir))
	}

	// Filter to .log files and collect in reverse order (most recent first)
	var logs []os.DirEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if filepath.Ext(entries[i].Name()) == ".log" {
			logs = append(logs, entries[i])
		}
	}

	if len(logs) == 0 {
		return fmt.Errorf("no logs found in %s", tilde.Path(logDir))
	}

	// Build options for the selector
	var options []huh.Option[string]
	for _, entry := range logs {
		name := entry.Name()
		path := filepath.Join(logDir, name)
		info, _ := entry.Info()
		label := name
		if info != nil {
			label = fmt.Sprintf("%s (%s)", name, humanize.Bytes(uint64(info.Size())))
		}
		options = append(options, huh.NewOption(label, path))
	}

	var selected string
	for {
		km := huh.NewDefaultKeyMap()
		km.Quit = key.NewBinding(key.WithKeys("q", "ctrl+c"))
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Select Log").
					Description(tilde.Path(logDir)).
					Options(options...).
					Value(&selected),
			),
		).WithKeyMap(km)

		if err := form.Run(); err != nil {
			return nil
		}

		// Open in less
		cmd := exec.Command("less", selected)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
}

func runRemove(cmd *cobra.Command, args []string, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	force, _ := cmd.Flags().GetBool("force")
	cfg := config.LoadAll(toolDefaults())
	backends := removeBackends(backendFlag)

	targetsByBackend, err := resolveRemoveTargets(ctx, cfg, backends, args, stderr)
	if err != nil {
		return err
	}

	for _, backendType := range backends {
		toRemove := targetsByBackend[backendType]
		if len(toRemove) == 0 {
			continue
		}

		backendClient, err := createBackendByType(backendType, cfg)
		if err != nil {
			cli.LogWarningTo(stderr, "%s not available: %v", backendType, err)
			continue
		}

		// Unless -f is passed, refuse to remove running containers.
		if !force {
			containers, listErr := backendClient.List(ctx)
			if listErr == nil {
				toRemove = filterRunningContainers(toRemove, containers, stderr)
			}
		}

		if len(toRemove) == 0 {
			backendClient.Close()
			continue
		}

		removed, err := backendClient.Remove(ctx, toRemove)
		backendClient.Close()
		if err != nil {
			cli.LogWarningTo(stderr, "failed to remove containers (%s): %v", backendType, err)
			continue
		}

		for _, name := range removed {
			cli.LogTo(stderr, "Removed %s (%s)", name, backendType)
		}
	}

	return nil
}

func runExec(cmd *cobra.Command, name string, command []string, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	cfg := config.LoadAll(toolDefaults())

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container", "fly"}
	}

	for _, backendType := range backends {
		backendClient, err := createBackendByType(backendType, cfg)
		if err != nil {
			continue
		}

		err = backendClient.Exec(ctx, name, command)
		backendClient.Close()

		if err == nil {
			return nil
		}

		// If the error is "not found", try the next backend.
		// If the error is something else (not running, exec failure), return it.
		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}

	return fmt.Errorf("container %s not found", name)
}

func runReconnect(cmd *cobra.Command, name string, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	cfg := config.LoadAll(toolDefaults())
	cwd, _ := os.Getwd()

	// Collect mount paths for re-sync using the same logic as a normal run.
	// We use an empty tool name — global and repo mounts are still collected.
	repoMatches := run.GetMatchingRepos(cfg, cwd)
	mountsRO, mountsRW := run.CollectMounts("", cfg, cwd, repoMatches, nil, nil, nil)

	// Collect clean mount paths (same logic as run.Tool)
	var cleanMountPaths []string
	for _, m := range mountsRO {
		if _, err := os.Lstat(m); err == nil {
			cleanMountPaths = append(cleanMountPaths, m)
		}
	}
	for _, m := range mountsRW {
		if _, err := os.Lstat(m); err == nil {
			cleanMountPaths = append(cleanMountPaths, m)
		}
	}

	opts := backend.RunOptions{
		MountsRO:        mountsRO,
		MountsRW:        mountsRW,
		CleanMountPaths: cleanMountPaths,
	}

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container", "fly"}
	}

	for _, backendType := range backends {
		backendClient, err := createBackendByType(backendType, cfg)
		if err != nil {
			continue
		}

		err = backendClient.Reconnect(ctx, name, opts)
		backendClient.Close()

		if err == nil {
			return nil
		}

		if !strings.Contains(err.Error(), "not found") {
			return err
		}
	}

	return fmt.Errorf("container %s not found", name)
}

func completeContainerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Only complete the first arg (container name)
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := context.Background()
	cfg := config.LoadAll(toolDefaults())
	var names []string

	for _, backendType := range []string{"docker", "container", "fly"} {
		bc, err := createBackendByType(backendType, cfg)
		if err != nil {
			continue
		}
		if containers, err := bc.List(ctx); err == nil {
			for _, ctr := range containers {
				if ctr.IsRunning && strings.HasPrefix(ctr.Name, toComplete) {
					names = append(names, ctr.Name)
				}
			}
		}
		bc.Close()
	}

	return names, cobra.ShellCompDirectiveNoFileComp
}

func runList(cmd *cobra.Command, _ []string, stdout, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	quietFlag, _ := cmd.Flags().GetBool("quiet")
	cfg := config.LoadAll(toolDefaults())

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container", "fly"}
	}

	hasContainers := false

	// Collect all container info first to calculate column widths
	type containerRow struct {
		name        string
		image       string
		backendType string
		memory      string
		status      string
	}
	var rows []containerRow

	for _, backendType := range backends {
		backendClient, err := createBackendByType(backendType, cfg)
		if err != nil {
			if !quietFlag {
				cli.LogWarningTo(stderr, "%s not available: %v", backendType, err)
			}
			continue
		}

		containers, err := backendClient.List(ctx)
		backendClient.Close()
		if err != nil {
			if !quietFlag {
				cli.LogWarningTo(stderr, "Failed to list containers (%s): %v", backendType, err)
			}
			continue
		}

		for _, ctr := range containers {
			hasContainers = true
			if quietFlag {
				fmt.Fprintln(stdout, ctr.Name)
			} else {
				rows = append(rows, containerRow{
					name:        ctr.Name,
					image:       ctr.Image,
					backendType: backendType,
					memory:      formatMemoryUsage(ctr.MemoryUsage, ctr.IsRunning),
					status:      ctr.Status,
				})
			}
		}
	}

	// Print table with dynamic column widths
	if len(rows) > 0 {
		// Calculate max widths for each column
		nameWidth := len("NAME")
		imageWidth := len("IMAGE")
		backendWidth := len("BACKEND")
		memoryWidth := len("MEMORY")

		for _, r := range rows {
			if len(r.name) > nameWidth {
				nameWidth = len(r.name)
			}
			if len(r.image) > imageWidth {
				imageWidth = len(r.image)
			}
			if len(r.backendType) > backendWidth {
				backendWidth = len(r.backendType)
			}
			if len(r.memory) > memoryWidth {
				memoryWidth = len(r.memory)
			}
		}

		// Print header
		format := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n",
			nameWidth, imageWidth, backendWidth, memoryWidth)
		fmt.Fprintf(stdout, format, "NAME", "IMAGE", "BACKEND", "MEMORY", "STATUS")

		// Print rows
		for _, r := range rows {
			fmt.Fprintf(stdout, format, r.name, r.image, r.backendType, r.memory, r.status)
		}
	}

	if !hasContainers && !quietFlag {
		cli.LogTo(stderr, "No silo containers found")
	}

	return nil
}

// formatMemoryUsage returns a human-readable memory string.
// For stopped containers, returns "-".
// For running containers with 0 bytes (stats unavailable), returns "N/A".
func formatMemoryUsage(bytes uint64, isRunning bool) string {
	if !isRunning {
		return "-"
	}
	if bytes == 0 {
		return "N/A"
	}
	return humanize.IBytes(bytes)
}

// runBackgroundBuild is the entry point for the hidden `silo __build` command.
// It reads all build parameters from the manifest in the build state directory,
// acquires a build lock, builds the image, and updates state.
func runBackgroundBuild(dir string, stderr io.Writer) error {
	if dir == "" {
		return fmt.Errorf("__build: --dir is required")
	}

	// Read build manifest.
	imageTag, buildCfg, tool, dockerfile, buildArgs, err := run.ReadBuildManifest(dir)
	if err != nil {
		return fmt.Errorf("__build: %w", err)
	}

	// Acquire build lock — exit cleanly if another build is already running.
	lock, err := run.TryLock(imageTag)
	if err != nil {
		return fmt.Errorf("__build: lock: %w", err)
	}
	if lock == nil {
		// Another process is already building this image.
		return nil
	}
	defer lock.Unlock()

	// Create backend.
	backendClient, err := run.CreateBackend(buildCfg, stderr, false)
	if err != nil {
		lock.WriteStatus("failed")
		return fmt.Errorf("__build: backend: %w", err)
	}
	defer backendClient.Close()

	// Build image.
	fmt.Fprintf(stderr, "==> Background build: %s (%s)\n", tool, imageTag)
	ctx := context.Background()
	_, err = backendClient.Build(ctx, backend.BuildOptions{
		Dockerfile: dockerfile,
		Target:     tool,
		Tag:        imageTag,
		BuildArgs:  buildArgs,
		OnProgress: func(msg string) {
			fmt.Fprint(stderr, msg)
		},
	})
	if err != nil {
		lock.WriteStatus("failed")
		return fmt.Errorf("__build: build: %w", err)
	}

	lock.WriteStatus("done")
	fmt.Fprintf(stderr, "==> Background build complete: %s\n", imageTag)

	// Update last-image so next run picks up the new image.
	run.SaveLastImage(tool, imageTag)

	return nil
}

// createBackendByType creates a backend client for the given type name.
func createBackendByType(backendType string, cfg config.Config) (backend.Backend, error) {
	switch backendType {
	case "docker":
		return docker.NewClient()
	case "container":
		return applecontainer.NewClient()
	case "fly":
		return flybackend.NewClient(cfg.Backends.Fly.App, cfg.Backends.Fly.Region, os.Stderr)
	default:
		return nil, fmt.Errorf("unknown backend: %s", backendType)
	}
}

func runUpgrade(stderr io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	// Detect Homebrew: the resolved binary lives under a Homebrew Cellar directory.
	if strings.Contains(self, "/Cellar/") || strings.Contains(self, "/homebrew/") {
		args := []string{"brew", "upgrade", "--fetch-HEAD", "leighmcculloch/silo/silo"}
		cli.LogTo(stderr, "Running: %s", strings.Join(args, " "))
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Detect go install: the resolved binary lives under a GOPATH or GOBIN directory.
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, _ := os.UserHomeDir()
		gopath = filepath.Join(home, "go")
	}
	gobin := os.Getenv("GOBIN")
	if gobin == "" {
		gobin = filepath.Join(gopath, "bin")
	}
	if strings.HasPrefix(self, gopath) || strings.HasPrefix(self, gobin) {
		args := []string{"go", "install", "github.com/leighmcculloch/silo@latest"}
		cli.LogTo(stderr, "Running: %s", strings.Join(args, " "))
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("cannot determine install method for %s (not Homebrew or go install)", self)
}
