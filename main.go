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

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/leighmcculloch/silo/backend"
	applecontainer "github.com/leighmcculloch/silo/backend/container"
	"github.com/leighmcculloch/silo/backend/docker"
	sshbackend "github.com/leighmcculloch/silo/backend/ssh"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/configshow"
	"github.com/leighmcculloch/silo/run"
	"github.com/leighmcculloch/silo/tools"
	"github.com/leighmcculloch/silo/tools/claudecode"
	"github.com/leighmcculloch/silo/tools/copilotcli"
	"github.com/leighmcculloch/silo/tools/opencode"
	"github.com/spf13/cobra"
)

var (
	version = "dev"

	// supportedTools is the single source of truth for which tools silo
	// supports. To add a tool: create tools/<name>/, define its Tool, and
	// add it here. To remove a tool: delete from this slice.
	supportedTools = []tools.Tool{
		claudecode.Tool,
		opencode.Tool,
		copilotcli.Tool,
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
		cli.LogErrorTo(stderr, "%v", err)
		return 1
	}
	return 0
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
Run AI coding assistants (Claude Code, OpenCode, Copilot) in isolated
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
  silo opencode
  silo copilot

  # Pass arguments to the tool
  silo claude -- --help`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSilo(cmd, args, stdout, stderr)
		},
	}

	rootCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh")
	rootCmd.Flags().Bool("force-build", false, "Force rebuild of container image, ignoring cache")
	rootCmd.Flags().BoolP("verbose", "v", false, "Show detailed output instead of progress bar")

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
		toolCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh")
		toolCmd.Flags().Bool("force-build", false, "Force rebuild of container image, ignoring cache")
		toolCmd.Flags().BoolP("verbose", "v", false, "Show detailed output instead of progress bar")
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
	configInitCmd.Flags().BoolP("global", "g", false, "Create global config (~/.config/silo/silo.jsonc)")
	configInitCmd.Flags().BoolP("local", "l", false, "Create local config (silo.jsonc)")
	configInitCmd.MarkFlagsMutuallyExclusive("global", "local")

	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configPathsCmd)
	configCmd.AddCommand(configEditCmd)
	configCmd.AddCommand(configDefaultCmd)
	configCmd.AddCommand(configInitCmd)

	rootCmd.AddCommand(configCmd)

	lsCmd := &cobra.Command{
		Use:     "ls",
		Short:   "List all silo-created containers",
		GroupID: "container",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args, stdout, stderr)
		},
	}
	lsCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh (default: all)")
	lsCmd.Flags().BoolP("quiet", "q", false, "Only display container names")
	rootCmd.AddCommand(lsCmd)

	rmCmd := &cobra.Command{
		Use:     "rm [container...]",
		Short:   "Remove silo containers",
		GroupID: "container",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd, args, stderr)
		},
	}
	rmCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh (default: all)")
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
	execCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh (default: all)")
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
	shellCmd.Flags().String("backend", "", "Backend to use: docker, container, ssh (default: all)")
	rootCmd.AddCommand(shellCmd)

	rootCmd.Version = version
	rootCmd.SetVersionTemplate("silo version {{.Version}}\n")

	return rootCmd
}

func runSilo(cmd *cobra.Command, args []string, stdout, stderr io.Writer) error {
	// Load configuration
	cfg := config.LoadAll(toolDefaults())

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

	// Get verbose flag
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Run the tool
	return run.Tool(run.Options{
		ToolDef:    *toolDef,
		Config:     cfg,
		Dockerfile: Dockerfile(supportedTools),
		ForceBuild: forceBuild,
		Verbose:    verbose,
		Stdout:     stdout,
		Stderr:     stderr,
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

	// Get verbose flag
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Run the tool
	return run.Tool(run.Options{
		ToolDef:    toolDef,
		ToolArgs:   toolArgs,
		Config:     cfg,
		Dockerfile: Dockerfile(supportedTools),
		ForceBuild: forceBuild,
		Verbose:    verbose,
		Stdout:     stdout,
		Stderr:     stderr,
	})
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
						huh.NewOption("Global (~/.config/silo/silo.jsonc)", "global"),
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

func runRemove(cmd *cobra.Command, args []string, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	cfg := config.LoadAll(toolDefaults())

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container", "ssh"}
	}

	for _, backendType := range backends {
		var backendClient backend.Backend
		var err error

		switch backendType {
		case "docker":
			backendClient, err = docker.NewClient()
			if err != nil {
				cli.LogWarningTo(stderr, "Docker not available: %v", err)
				continue
			}
		case "container":
			backendClient, err = applecontainer.NewClient()
			if err != nil {
				cli.LogWarningTo(stderr, "Container backend not available: %v", err)
				continue
			}
		case "ssh":
			backendClient, err = sshbackend.NewClient(cfg.Backends.SSH)
			if err != nil {
				cli.LogWarningTo(stderr, "SSH backend not available: %v", err)
				continue
			}
		default:
			return fmt.Errorf("unknown backend: %s", backendType)
		}

		removed, err := backendClient.Remove(ctx, args)
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
		backends = []string{"docker", "container", "ssh"}
	}

	for _, backendType := range backends {
		var backendClient backend.Backend
		var err error

		switch backendType {
		case "docker":
			backendClient, err = docker.NewClient()
			if err != nil {
				continue
			}
		case "container":
			backendClient, err = applecontainer.NewClient()
			if err != nil {
				continue
			}
		case "ssh":
			backendClient, err = sshbackend.NewClient(cfg.Backends.SSH)
			if err != nil {
				continue
			}
		default:
			return fmt.Errorf("unknown backend: %s", backendType)
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

func completeContainerNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Only complete the first arg (container name)
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx := context.Background()
	var names []string

	// Try docker backend
	if dc, err := docker.NewClient(); err == nil {
		if containers, err := dc.List(ctx); err == nil {
			for _, ctr := range containers {
				if ctr.IsRunning && strings.HasPrefix(ctr.Name, toComplete) {
					names = append(names, ctr.Name)
				}
			}
		}
		dc.Close()
	}

	// Try container backend
	if cc, err := applecontainer.NewClient(); err == nil {
		if containers, err := cc.List(ctx); err == nil {
			for _, ctr := range containers {
				if ctr.IsRunning && strings.HasPrefix(ctr.Name, toComplete) {
					names = append(names, ctr.Name)
				}
			}
		}
		cc.Close()
	}

	// Try SSH backend
	cfg := config.LoadAll(toolDefaults())
	if cfg.Backends.SSH.Host != "" {
		if sc, err := sshbackend.NewClient(cfg.Backends.SSH); err == nil {
			if containers, err := sc.List(ctx); err == nil {
				for _, ctr := range containers {
					if ctr.IsRunning && strings.HasPrefix(ctr.Name, toComplete) {
						names = append(names, ctr.Name)
					}
				}
			}
			sc.Close()
		}
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
		backends = []string{"docker", "container", "ssh"}
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
		var backendClient backend.Backend
		var err error

		switch backendType {
		case "docker":
			backendClient, err = docker.NewClient()
			if err != nil {
				if !quietFlag {
					cli.LogWarningTo(stderr, "Docker not available: %v", err)
				}
				continue
			}
		case "container":
			backendClient, err = applecontainer.NewClient()
			if err != nil {
				if !quietFlag {
					cli.LogWarningTo(stderr, "Container backend not available: %v", err)
				}
				continue
			}
		case "ssh":
			backendClient, err = sshbackend.NewClient(cfg.Backends.SSH)
			if err != nil {
				if !quietFlag {
					cli.LogWarningTo(stderr, "SSH backend not available: %v", err)
				}
				continue
			}
		default:
			return fmt.Errorf("unknown backend: %s", backendType)
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
