package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/docker"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
)

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

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Show the current merged configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfig(cmd, args, stdout)
		},
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a sample .silo.json configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, args, stderr)
		},
	}

	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(initCmd)

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

	// Create Docker client
	cli.LogTo(stderr, "Connecting to Docker...")
	dockerClient, err := docker.NewClient()
	if err != nil {
		return fmt.Errorf("failed to connect to Docker: %w", err)
	}
	defer dockerClient.Close()

	// Get current user info
	home := os.Getenv("HOME")
	user := os.Getenv("USER")
	uid := os.Getuid()

	// Build the image
	cli.LogTo(stderr, "Preparing image for %s...", tool)
	_, err = dockerClient.Build(ctx, docker.BuildOptions{
		Dockerfile: Dockerfile(),
		Target:     tool,
		BuildArgs: map[string]string{
			"HOME": home,
			"USER": user,
			"UID":  fmt.Sprintf("%d", uid),
		},
		OnProgress: func(msg string) {
			// Could parse and display build progress here
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	cli.LogSuccessTo(stderr, "Image ready")

	// Collect mounts
	cwd, _ := os.Getwd()
	mounts := []string{cwd}

	// Add tool-specific mounts
	if toolCfg, ok := cfg.Tools[tool]; ok {
		mounts = append(mounts, toolCfg.Mounts...)
	}

	// Add global config mounts
	mounts = append(mounts, cfg.Mounts...)

	// Add git worktree roots
	worktreeRoots, _ := docker.GetGitWorktreeRoots(cwd)
	mounts = append(mounts, worktreeRoots...)

	// Add key files
	mounts = append(mounts, cfg.KeyFiles...)

	// Collect environment variables
	var envVars []string

	// Get git identity
	gitName, gitEmail := docker.GetGitIdentity()
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

	// Source files and add their exports
	for _, sf := range cfg.SourceFiles {
		if data, err := os.ReadFile(sf); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if rest, found := strings.CutPrefix(line, "export "); found {
					if parts := strings.SplitN(rest, "=", 2); len(parts) == 2 {
						envVars = append(envVars, parts[0]+"="+strings.Trim(parts[1], "\"'"))
					}
				}
			}
		}
	}

	// Pass through environment variables
	for _, varName := range cfg.EnvPassthrough {
		if val := os.Getenv(varName); val != "" {
			envVars = append(envVars, varName+"="+val)
		}
	}

	// Tool-specific passthrough
	if toolCfg, ok := cfg.Tools[tool]; ok {
		for _, varName := range toolCfg.EnvPassthrough {
			if val := os.Getenv(varName); val != "" {
				envVars = append(envVars, varName+"="+val)
			}
		}
		envVars = append(envVars, toolCfg.EnvSet...)
	}

	// Explicit env vars
	envVars = append(envVars, cfg.EnvSet...)

	// Generate container name
	baseName := filepath.Base(cwd)
	baseName = strings.ReplaceAll(baseName, ".", "")
	containerName := fmt.Sprintf("%s-%02d", baseName, rand.Intn(100))

	// Log mounts
	cli.LogTo(stderr, "Mounts:")
	seen := make(map[string]bool)
	for _, m := range mounts {
		if _, err := os.Stat(m); err != nil {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		cli.LogBulletTo(stderr, "%s", m)
	}

	cli.LogTo(stderr, "Container name: %s", containerName)
	cli.LogTo(stderr, "Running %s...", tool)

	// Run the container
	err = dockerClient.Run(ctx, docker.RunOptions{
		Image:        tool,
		Name:         containerName,
		WorkDir:      cwd,
		Mounts:       mounts,
		Env:          envVars,
		Args:         toolArgs,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		TTY:          true,
		RemoveOnExit: true,
		SecurityOptions: []string{
			"no-new-privileges:true",
		},
	})

	if err != nil {
		return fmt.Errorf("container error: %w", err)
	}

	return nil
}

func runConfig(_ *cobra.Command, _ []string, stdout io.Writer) error {
	cfg, err := config.LoadAll()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Fprintln(stdout, cli.Title("Silo Configuration"))
	fmt.Fprintln(stdout)

	fmt.Fprintln(stdout, cli.Subtitle("Global Settings"))
	fmt.Fprintf(stdout, "  Mounts:          %v\n", cfg.Mounts)
	fmt.Fprintf(stdout, "  Env Passthrough: %v\n", cfg.EnvPassthrough)
	fmt.Fprintf(stdout, "  Env Set:         %v\n", cfg.EnvSet)
	fmt.Fprintf(stdout, "  Key Files:       %v\n", cfg.KeyFiles)
	fmt.Fprintf(stdout, "  Source Files:    %v\n", cfg.SourceFiles)
	fmt.Fprintln(stdout)

	fmt.Fprintln(stdout, cli.Subtitle("Tools"))
	for name, tool := range cfg.Tools {
		fmt.Fprintf(stdout, "  %s:\n", name)
		fmt.Fprintf(stdout, "    Mounts:          %v\n", tool.Mounts)
		fmt.Fprintf(stdout, "    Env Passthrough: %v\n", tool.EnvPassthrough)
		fmt.Fprintf(stdout, "    Env Set:         %v\n", tool.EnvSet)
	}

	return nil
}

func runInit(_ *cobra.Command, _ []string, stderr io.Writer) error {
	configPath := ".silo.json"

	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists: %s", configPath)
	}

	sampleConfig := `{
  "mounts": [],
  "env_passthrough": [],
  "env_set": [],
  "key_files": [],
  "source_files": [],
  "tools": {
    "claude": {
      "mounts": [],
      "env_passthrough": [],
      "env_set": []
    }
  }
}
`

	if err := os.WriteFile(configPath, []byte(sampleConfig), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	cli.LogSuccessTo(stderr, "Created %s", configPath)
	return nil
}
