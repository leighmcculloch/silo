package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/leighmcculloch/silo/backend"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
	applecontainer "github.com/leighmcculloch/silo/container"
	"github.com/leighmcculloch/silo/docker"
	"github.com/leighmcculloch/silo/mountwait"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
)

const sampleConfig = `{
  "$schema": "https://raw.githubusercontent.com/leighmcculloch/silo/main/silo.schema.json",
  // Backend to use: "docker" or "container" (default: "container" if installed, else "docker")
  // "backend": "docker",
  // Default tool to run: "claude", "opencode", or "copilot" (prompts if not set)
  // "tool": "claude",
  // Read-only directories or files to mount into the container
  // "mounts_ro": [],
  // Read-write directories or files to mount into the container
  // "mounts_rw": [],
  // Environment variables: names without '=' pass through from host,
  // names with '=' set explicitly (e.g., "FOO=bar")
  // "env": [],
  // Shell commands to run inside the container after building the image
  // "post_build_hooks": [],
  // Shell commands to run inside the container before the tool
  // "pre_run_hooks": [],
  // Tool-specific configuration (merged with global config above)
  // Example: "tools": { "claude": { "env": ["CLAUDE_SPECIFIC_VAR"] } }
  // "tools": {}
}
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the main entry point that can be called by tests
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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
		Use:   "silo [tool] [-- args...]",
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
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTools,
		SilenceUsage:      true,
		SilenceErrors:     true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSilo(cmd, args, stdout, stderr)
		},
	}

	rootCmd.Flags().String("backend", "", "Backend to use: docker, container")

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management commands",
	}

	configShowCmd := &cobra.Command{
		Use:   "show",
		Short: "Show the current merged configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd, args, stdout)
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
			return runConfigDefault(cmd, args, stdout)
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
		Use:   "ls",
		Short: "List all silo-created containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args, stdout, stderr)
		},
	}
	lsCmd.Flags().String("backend", "", "Backend to use: docker, container (default: both)")
	lsCmd.Flags().BoolP("quiet", "q", false, "Only display container names")
	rootCmd.AddCommand(lsCmd)

	rmCmd := &cobra.Command{
		Use:   "rm [container...]",
		Short: "Remove silo containers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd, args, stderr)
		},
	}
	rmCmd.Flags().String("backend", "", "Backend to use: docker, container (default: both)")
	rootCmd.AddCommand(rmCmd)

	rootCmd.Version = version
	rootCmd.SetVersionTemplate("silo version {{.Version}}\n")

	return rootCmd
}

func completeTools(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	tools := AvailableTools()
	var completions []string
	for _, t := range tools {
		if strings.HasPrefix(t, toComplete) {
			completions = append(completions, fmt.Sprintf("%s\t%s", t, ToolDescription(t)))
		}
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func runSilo(cmd *cobra.Command, args []string, stdout, stderr io.Writer) error {
	// Load configuration
	cfg, err := config.LoadAll()
	if err != nil {
		cli.LogWarningTo(stderr, "Failed to load config: %v", err)
		cfg = config.DefaultConfig()
	}

	// Determine tool
	var tool string
	if len(args) > 0 {
		tool = args[0]
	} else if cfg.Tool != "" {
		tool = cfg.Tool
	} else {
		// Interactive selection
		tool, err = selectTool()
		if err != nil {
			return err
		}
	}

	// Validate tool
	validTools := AvailableTools()
	if !slices.Contains(validTools, tool) {
		return fmt.Errorf("invalid tool: %s (valid tools: %s)", tool, strings.Join(validTools, ", "))
	}

	// Get tool-specific args (everything after --)
	var toolArgs []string
	if cmd.ArgsLenAtDash() > -1 {
		toolArgs = args[cmd.ArgsLenAtDash():]
	}

	// Override backend from flag
	if b, _ := cmd.Flags().GetString("backend"); b != "" {
		cfg.Backend = b
	}

	// Run the tool
	return runTool(tool, toolArgs, cfg, stdout, stderr)
}

func selectTool() (string, error) {
	tools := AvailableTools()

	var options []huh.Option[string]
	for _, t := range tools {
		options = append(options, huh.NewOption(ToolDescription(t), t))
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

func runTool(tool string, toolArgs []string, cfg config.Config, _, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Select and create backend
	var backendClient backend.Backend
	var err error

	backendType := cfg.Backend
	if backendType == "" {
		// Default to container if available, otherwise docker
		if _, err := exec.LookPath("container"); err == nil {
			backendType = "container"
		} else {
			backendType = "docker"
		}
	}

	switch backendType {
	case "docker":
		cli.LogTo(stderr, "Using docker backend...")
		backendClient, err = docker.NewClient()
		if err != nil {
			return fmt.Errorf("failed to connect to Docker: %w", err)
		}
	case "container":
		cli.LogTo(stderr, "Using apple container (lightweight vms) backend...")
		backendClient, err = applecontainer.NewClient()
		if err != nil {
			return fmt.Errorf("failed to initialize container backend: %w", err)
		}
	default:
		return fmt.Errorf("unknown backend: %s (valid: docker, container)", backendType)
	}
	defer backendClient.Close()

	// Get current user info
	home := os.Getenv("HOME")
	user := os.Getenv("USER")
	uid := os.Getuid()

	// Collect mounts (needed for Lima VM configuration at build time)
	cwd, _ := os.Getwd()
	mountsRW := []string{cwd}
	var mountsRO []string

	// Add tool-specific mounts
	if toolCfg, ok := cfg.Tools[tool]; ok {
		for _, m := range toolCfg.MountsRO {
			mountsRO = append(mountsRO, expandPath(m))
		}
		for _, m := range toolCfg.MountsRW {
			mountsRW = append(mountsRW, expandPath(m))
		}
	}

	// Add global config mounts
	for _, m := range cfg.MountsRO {
		mountsRO = append(mountsRO, expandPath(m))
	}
	for _, m := range cfg.MountsRW {
		mountsRW = append(mountsRW, expandPath(m))
	}

	// Add git worktree roots (read-write for git operations)
	worktreeRoots, _ := backend.GetGitWorktreeRoots(cwd)
	mountsRW = append(mountsRW, worktreeRoots...)

	// Get tool-specific hooks
	var toolPreRunHooks []string
	var toolPostBuildHooks []string
	if toolCfg, ok := cfg.Tools[tool]; ok {
		toolPreRunHooks = toolCfg.PreRunHooks
		toolPostBuildHooks = toolCfg.PostBuildHooks
	}

	// Compute content-addressed tag for caching
	dockerfile := DockerfileWithHooks(cfg.PostBuildHooks, tool, toolPostBuildHooks)
	buildArgs := map[string]string{
		"HOME": home,
		"USER": user,
		"UID":  fmt.Sprintf("%d", uid),
	}
	imageTag := buildImageTag(tool, dockerfile, buildArgs)

	// Log post-build hooks (before building so user knows what will be run)
	if len(cfg.PostBuildHooks) > 0 {
		cli.LogTo(stderr, "Post-build hooks:")
		for _, hook := range cfg.PostBuildHooks {
			cli.LogBulletTo(stderr, "%s", hook)
		}
	}
	if len(toolPostBuildHooks) > 0 {
		cli.LogTo(stderr, "Post-build hooks (%s):", tool)
		for _, hook := range toolPostBuildHooks {
			cli.LogBulletTo(stderr, "%s", hook)
		}
	}

	// Check if image already exists
	exists, err := backendClient.ImageExists(ctx, imageTag)
	if err != nil {
		exists = false
	}

	cli.LogTo(stderr, "Building environment for %s...", tool)
	if exists {
		cli.LogSuccessBulletTo(stderr, "Environment cached")
	} else {
		_, err = backendClient.Build(ctx, backend.BuildOptions{
			Dockerfile: dockerfile,
			Target:     tool,
			Tag:        imageTag,
			BuildArgs:  buildArgs,
			MountsRO:   mountsRO,
			MountsRW:   mountsRW,
			OnProgress: func(msg string) {
				fmt.Fprint(stderr, msg)
			},
		})
		if err != nil {
			return fmt.Errorf("failed to build environment: %w", err)
		}
		cli.LogSuccessBulletTo(stderr, "Environment ready")
	}

	// Collect environment variables
	var envVars []string

	// Get git identity
	gitName, gitEmail := backend.GetGitIdentity()
	if gitName != "" {
		envVars = append(envVars,
			"GIT_AUTHOR_NAME="+gitName,
			"GIT_COMMITTER_NAME="+gitName,
		)
		cli.LogTo(stderr, "Git identity: %s <%s>", gitName, gitEmail)
	}
	if gitEmail != "" {
		envVars = append(envVars,
			"GIT_AUTHOR_EMAIL="+gitEmail,
			"GIT_COMMITTER_EMAIL="+gitEmail,
		)
	}

	// Track env vars by category for logging
	var envExplicitGlobal []string // explicit from cfg.Env (KEY=VALUE)
	var envExplicitTool []string   // explicit from toolCfg.Env (KEY=VALUE)
	var envFromHost []string       // lifted from host env
	var envNotFound []string       // configured but not in host env

	// Process env vars (passthrough if no '=', explicit if has '=')
	for _, e := range cfg.Env {
		if strings.Contains(e, "=") {
			envVars = append(envVars, e)
			// Extract key name for logging
			envExplicitGlobal = append(envExplicitGlobal, strings.SplitN(e, "=", 2)[0])
		} else if val := os.Getenv(e); val != "" {
			envVars = append(envVars, e+"="+val)
			envFromHost = append(envFromHost, e)
		} else {
			envNotFound = append(envNotFound, e)
		}
	}

	// Tool-specific env vars
	if toolCfg, ok := cfg.Tools[tool]; ok {
		for _, e := range toolCfg.Env {
			if strings.Contains(e, "=") {
				envVars = append(envVars, e)
				envExplicitTool = append(envExplicitTool, strings.SplitN(e, "=", 2)[0])
			} else if val := os.Getenv(e); val != "" {
				envVars = append(envVars, e+"="+val)
				envFromHost = append(envFromHost, e)
			} else {
				envNotFound = append(envNotFound, e)
			}
		}
	}

	// Generate container name
	baseName := filepath.Base(cwd)
	baseName = strings.ReplaceAll(baseName, ".", "")
	containerName := backendClient.NextContainerName(ctx, baseName)

	// Log mounts
	seen := make(map[string]bool)
	if len(mountsRO) > 0 {
		cli.LogTo(stderr, "Mounts (read-only):")
		for _, m := range mountsRO {
			if _, err := os.Lstat(m); err != nil {
				continue
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			cli.LogBulletTo(stderr, "%s", tilde.Path(m))
		}
	}
	cli.LogTo(stderr, "Mounts (read-write):")
	for _, m := range mountsRW {
		if _, err := os.Lstat(m); err != nil {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		cli.LogBulletTo(stderr, "%s", tilde.Path(m))
	}

	// Log environment variables
	if len(envExplicitGlobal) > 0 {
		cli.LogTo(stderr, "Environment (config):")
		for _, name := range envExplicitGlobal {
			cli.LogBulletTo(stderr, "%s", name)
		}
	}
	if len(envExplicitTool) > 0 {
		cli.LogTo(stderr, "Environment (config, %s):", tool)
		for _, name := range envExplicitTool {
			cli.LogBulletTo(stderr, "%s", name)
		}
	}
	if len(envFromHost) > 0 || len(envNotFound) > 0 {
		cli.LogTo(stderr, "Environment (host):")
		for _, name := range envFromHost {
			cli.LogBulletTo(stderr, "%s", name)
		}
		for _, name := range envNotFound {
			cli.LogBulletTo(stderr, "%s (not set)", name)
		}
	}

	// Combine global and tool-specific pre-run hooks
	preRunHooks := append(cfg.PreRunHooks, toolPreRunHooks...)

	// Collect all mount paths that exist for the mount wait script
	var allMountPaths []string
	for _, m := range mountsRO {
		if _, err := os.Lstat(m); err == nil {
			allMountPaths = append(allMountPaths, m)
		}
	}
	for _, m := range mountsRW {
		if _, err := os.Lstat(m); err == nil {
			allMountPaths = append(allMountPaths, m)
		}
	}
	sort.Strings(allMountPaths)

	// Prepend mount wait hook to ensure mounts are ready before other hooks run
	if mountWaitHook := mountwait.GenerateScript(allMountPaths); mountWaitHook != "" {
		preRunHooks = append([]string{mountWaitHook}, preRunHooks...)
	}

	// Log pre-run hooks
	if len(cfg.PreRunHooks) > 0 {
		cli.LogTo(stderr, "Pre-run hooks:")
		for _, hook := range cfg.PreRunHooks {
			cli.LogBulletTo(stderr, "%s", hook)
		}
	}
	if len(toolPreRunHooks) > 0 {
		cli.LogTo(stderr, "Pre-run hooks (%s):", tool)
		for _, hook := range toolPreRunHooks {
			cli.LogBulletTo(stderr, "%s", hook)
		}
	}

	cli.LogTo(stderr, "Container name: %s", containerName)
	cli.LogTo(stderr, "Running %s...", tool)

	// Define tool-specific commands
	toolCommands := map[string][]string{
		"claude":   {"claude", "--mcp-config=" + home + "/.claude/mcp.json", "--dangerously-skip-permissions"},
		"opencode": {"opencode"},
		"copilot":  {"copilot", "--allow-all", "--disable-builtin-mcps"},
	}

	// Run the container/VM
	err = backendClient.Run(ctx, backend.RunOptions{
		Image:       imageTag,
		Name:        containerName,
		WorkDir:     cwd,
		MountsRO:    mountsRO,
		MountsRW:    mountsRW,
		Env:         envVars,
		Command:     toolCommands[tool],
		Args:        toolArgs,
		PreRunHooks: preRunHooks,
	})

	if err != nil {
		return fmt.Errorf("run error: %w", err)
	}

	return nil
}

// buildImageTag returns a content-addressed image tag encoding the build inputs.
func buildImageTag(target, dockerfile string, buildArgs map[string]string) string {
	h := sha256.New()
	h.Write([]byte(dockerfile))
	h.Write([]byte{0})
	h.Write([]byte(target))
	h.Write([]byte{0})

	keys := make([]string, 0, len(buildArgs))
	for k := range buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write([]byte(buildArgs[k]))
		h.Write([]byte{0})
	}

	sum := fmt.Sprintf("%x", h.Sum(nil))
	return fmt.Sprintf("silo-%s-%s", target, sum[:16])
}

func runConfigShow(_ *cobra.Command, _ []string, stdout io.Writer) error {
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
	fmt.Fprintln(stdout, "  }")

	fmt.Fprintln(stdout, "}")
	return nil
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

func runConfigDefault(_ *cobra.Command, _ []string, stdout io.Writer) error {
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
	fmt.Fprintln(stdout, "  }")

	fmt.Fprintln(stdout, "}")
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

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container"}
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
		default:
			return fmt.Errorf("unknown backend: %s", backendType)
		}

		removed, err := backendClient.Remove(ctx, args)
		backendClient.Close()
		if err != nil {
			return fmt.Errorf("failed to remove containers (%s): %w", backendType, err)
		}

		for _, name := range removed {
			cli.LogTo(stderr, "Removed %s (%s)", name, backendType)
		}
	}

	return nil
}

func runList(cmd *cobra.Command, _ []string, stdout, stderr io.Writer) error {
	ctx := context.Background()

	backendFlag, _ := cmd.Flags().GetString("backend")
	quietFlag, _ := cmd.Flags().GetBool("quiet")

	var backends []string
	if backendFlag != "" {
		backends = []string{backendFlag}
	} else {
		backends = []string{"docker", "container"}
	}

	hasContainers := false
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
		default:
			return fmt.Errorf("unknown backend: %s", backendType)
		}

		containers, err := backendClient.List(ctx)
		backendClient.Close()
		if err != nil {
			return fmt.Errorf("failed to list containers (%s): %w", backendType, err)
		}

		for _, ctr := range containers {
			hasContainers = true
			if quietFlag {
				fmt.Fprintln(stdout, ctr.Name)
			} else {
				fmt.Fprintf(stdout, "%-20s  %-40s  %-10s  %s\n", ctr.Name, ctr.Image, backendType, ctr.Status)
			}
		}
	}

	if !hasContainers && !quietFlag {
		cli.LogTo(stderr, "No silo containers found")
	}

	return nil
}

// expandPath expands ~ to the user's home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		return os.Getenv("HOME")
	}
	return path
}
