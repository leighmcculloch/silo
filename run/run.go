package run

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/leighmcculloch/silo/backend"
	applecontainer "github.com/leighmcculloch/silo/backend/container"
	"github.com/leighmcculloch/silo/backend/docker"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
	"github.com/leighmcculloch/silo/git"
	"github.com/leighmcculloch/silo/mountwait"
	"github.com/leighmcculloch/silo/tilde"
	"github.com/leighmcculloch/silo/tools"
)

// Options configures a tool run.
type Options struct {
	ToolDef    tools.Tool
	ToolArgs   []string
	Config     config.Config
	Dockerfile string // raw Dockerfile template (before hook injection)
	ForceBuild bool
	Verbose    bool
	Stdout     io.Writer
	Stderr     io.Writer
}

// Tool runs a tool inside a container.
func Tool(opts Options) error {
	tool := opts.ToolDef.Name
	cfg := opts.Config
	stderr := opts.Stderr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Define progress sections
	progressSections := []string{
		"Backend",
		"Post-build hooks",
		"Building environment",
		"Git identity",
		"Mounts",
		"Environment",
		"Pre-run hooks",
		"Container",
		"Running",
	}

	// Create progress bar (only used when not verbose)
	var progress *cli.Progress
	if !opts.Verbose {
		progress = cli.NewProgress(stderr, progressSections)
		progress.Start()
	}

	// Handle signals - clean up progress bar on interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		if progress != nil {
			progress.Complete()
		}
		cancel()
	}()

	// Helper to log only in verbose mode
	logSection := func(format string, args ...any) {
		if opts.Verbose {
			cli.LogTo(stderr, format, args...)
		}
	}

	// Select and create backend
	if progress != nil {
		progress.SetSection("Backend")
	}
	backendClient, err := createBackend(cfg.Backend, stderr, opts.Verbose)
	if err != nil {
		if progress != nil {
			progress.Complete()
		}
		return err
	}
	defer backendClient.Close()

	// Start async version fetch (updates cache for this or next run)
	go opts.ToolDef.FetchVersion(ctx)

	// Get current user info
	home := os.Getenv("HOME")
	user := os.Getenv("USER")
	uid := os.Getuid()
	cwd, _ := os.Getwd()

	// Collect mounts from config
	mountsRO, mountsRW := collectMounts(tool, cfg, cwd)

	// Get tool-specific hooks
	var toolPreRunHooks, toolPostBuildHooks []string
	if toolCfg, ok := cfg.Tools[tool]; ok {
		toolPreRunHooks = toolCfg.PreRunHooks
		toolPostBuildHooks = toolCfg.PostBuildHooks
	}

	// Get repo-specific hooks
	var repoPreRunHooks, repoPostBuildHooks []string
	var matchedRepoNames []string
	for _, m := range GetMatchingRepos(cfg, cwd) {
		matchedRepoNames = append(matchedRepoNames, m.Name)
		repoPreRunHooks = append(repoPreRunHooks, m.Config.PreRunHooks...)
		repoPostBuildHooks = append(repoPostBuildHooks, m.Config.PostBuildHooks...)
	}

	// Prepare build configuration
	dockerfile := dockerfileWithHooks(opts.Dockerfile, cfg.PostBuildHooks, tool, toolPostBuildHooks, repoPostBuildHooks)
	buildArgs := map[string]string{
		"HOME": home,
		"USER": user,
		"UID":  fmt.Sprintf("%d", uid),
	}

	// Read cached tool version for cache-busting
	toolVersion := opts.ToolDef.CachedVersion()
	if toolVersion != "" {
		logSection("Tool version (cached): %s", toolVersion)
		buildArgs["CACHE_BUST"] = toolVersion
	}

	imageTag := buildImageTag(tool, dockerfile, buildArgs)

	// Build or use cached image
	if progress != nil {
		progress.SetSection("Post-build hooks")
	}
	if err := buildEnvironment(ctx, backendClient, buildEnvOptions{
		tool:               tool,
		dockerfile:         dockerfile,
		imageTag:           imageTag,
		buildArgs:          buildArgs,
		mountsRO:           mountsRO,
		mountsRW:           mountsRW,
		forceBuild:         opts.ForceBuild,
		globalPostBuild:    cfg.PostBuildHooks,
		toolPostBuildHooks: toolPostBuildHooks,
		repoPostBuildHooks: repoPostBuildHooks,
		matchedRepoNames:   matchedRepoNames,
		stderr:             stderr,
		verbose:            opts.Verbose,
		progress:           progress,
	}); err != nil {
		if progress != nil {
			progress.Complete()
		}
		return err
	}

	// Collect environment variables
	envVars, envLog := collectEnvVars(tool, cfg, cwd)

	// Generate container name
	baseName := filepath.Base(cwd)
	baseName = strings.ReplaceAll(baseName, ".", "")
	containerName := backendClient.NextContainerName(ctx, baseName)

	// Log configuration
	if progress != nil {
		progress.SetSection("Git identity")
	}
	logRunConfig(logRunConfigOptions{
		stderr:           stderr,
		tool:             tool,
		mountsRO:         mountsRO,
		mountsRW:         mountsRW,
		envLog:           envLog,
		globalPreRun:     cfg.PreRunHooks,
		toolPreRun:       toolPreRunHooks,
		repoPreRun:       repoPreRunHooks,
		matchedRepoNames: matchedRepoNames,
		containerName:    containerName,
		verbose:          opts.Verbose,
		progress:         progress,
	})

	// Prepare pre-run hooks
	preRunHooks := preparePreRunHooks(cfg.PreRunHooks, toolPreRunHooks, repoPreRunHooks, mountsRO, mountsRW, opts.Verbose)

	if progress != nil {
		progress.SetSection("Running")
	}
	logSection("Running %s...", tool)

	// Complete the progress bar before running the tool
	if progress != nil {
		progress.Complete()
	}

	// Run the container/VM
	err = backendClient.Run(ctx, backend.RunOptions{
		Image:       imageTag,
		Name:        containerName,
		WorkDir:     cwd,
		MountsRO:    mountsRO,
		MountsRW:    mountsRW,
		Env:         envVars,
		Command:     opts.ToolDef.Command(home),
		Args:        opts.ToolArgs,
		PreRunHooks: preRunHooks,
	})

	if err != nil {
		return fmt.Errorf("run error: %w", err)
	}

	return nil
}

// RepoMatch holds a matched repo pattern name and its associated config.
type RepoMatch struct {
	Name   string
	Config config.RepoConfig
}

// GetMatchingRepos returns repo matches (name + config) for repos whose pattern
// matches any of the git remote URLs, sorted by pattern length (shortest first)
// so more specific configs are applied last.
func GetMatchingRepos(cfg config.Config, cwd string) []RepoMatch {
	remoteURLs := git.GetGitRemoteURLs(cwd)
	if len(remoteURLs) == 0 {
		return nil
	}

	var matches []RepoMatch
	for pattern, repoCfg := range cfg.Repos {
		for _, url := range remoteURLs {
			if repoURLMatches(url, pattern) {
				matches = append(matches, RepoMatch{Name: pattern, Config: repoCfg})
				break // Only add each repo config once
			}
		}
	}

	// Sort by pattern length (shortest first = less specific first)
	sort.Slice(matches, func(i, j int) bool {
		return len(matches[i].Name) < len(matches[j].Name)
	})

	return matches
}

// repoURLMatches checks if a git remote URL matches a pattern.
// Both the URL and pattern have .git suffix stripped before comparison.
// The pattern matches if it is a substring of the URL, allowing for prefix matching
// (e.g., "github.com/stellar" matches "github.com/stellar/stellar-core").
// SSH URLs (git@host:path) are normalized to host/path format for matching.
func repoURLMatches(url, pattern string) bool {
	url = strings.TrimSuffix(url, ".git")
	pattern = strings.TrimSuffix(pattern, ".git")

	// Normalize SSH URL format (git@github.com:org/repo -> github.com/org/repo)
	if strings.HasPrefix(url, "git@") {
		url = strings.TrimPrefix(url, "git@")
		url = strings.Replace(url, ":", "/", 1)
	}

	return strings.Contains(url, pattern)
}

// createBackend creates the appropriate backend based on configuration.
func createBackend(backendType string, stderr io.Writer, verbose bool) (backend.Backend, error) {
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
		if verbose {
			cli.LogTo(stderr, "Using docker backend...")
		}
		client, err := docker.NewClient()
		if err != nil {
			return nil, fmt.Errorf("failed to connect to Docker: %w", err)
		}
		return client, nil
	case "container":
		if verbose {
			cli.LogTo(stderr, "Using apple container (lightweight vms) backend...")
		}
		client, err := applecontainer.NewClient()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize container backend: %w", err)
		}
		return client, nil
	default:
		return nil, fmt.Errorf("unknown backend: %s (valid: docker, container)", backendType)
	}
}

// collectMounts gathers all mount paths from config for a specific tool.
func collectMounts(tool string, cfg config.Config, cwd string) (mountsRO, mountsRW []string) {
	mountsRW = []string{cwd}

	// Add tool-specific mounts
	if toolCfg, ok := cfg.Tools[tool]; ok {
		for _, m := range toolCfg.MountsRO {
			mountsRO = append(mountsRO, expandPath(m))
		}
		for _, m := range toolCfg.MountsRW {
			mountsRW = append(mountsRW, expandPath(m))
		}
	}

	// Add repo-specific mounts (match git remote URLs)
	for _, rm := range GetMatchingRepos(cfg, cwd) {
		for _, m := range rm.Config.MountsRO {
			mountsRO = append(mountsRO, expandPath(m))
		}
		for _, m := range rm.Config.MountsRW {
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
	worktreeRoots, _ := git.GetGitWorktreeRoots(cwd)
	mountsRW = append(mountsRW, worktreeRoots...)

	return mountsRO, mountsRW
}

// buildEnvOptions contains options for building the container environment.
type buildEnvOptions struct {
	tool               string
	dockerfile         string
	imageTag           string
	buildArgs          map[string]string
	mountsRO           []string
	mountsRW           []string
	forceBuild         bool
	globalPostBuild    []string
	toolPostBuildHooks []string
	repoPostBuildHooks []string
	matchedRepoNames   []string
	stderr             io.Writer
	verbose            bool
	progress           *cli.Progress
}

// buildEnvironment builds or uses cached container image.
func buildEnvironment(ctx context.Context, backendClient backend.Backend, opts buildEnvOptions) error {
	// Helper to log only in verbose mode
	logSection := func(format string, args ...any) {
		if opts.verbose {
			cli.LogTo(opts.stderr, format, args...)
		}
	}
	logBullet := func(format string, args ...any) {
		if opts.verbose {
			cli.LogBulletTo(opts.stderr, format, args...)
		}
	}
	logSuccessBullet := func(format string, args ...any) {
		if opts.verbose {
			cli.LogSuccessBulletTo(opts.stderr, format, args...)
		}
	}

	// Log post-build hooks (before building so user knows what will be run)
	if len(opts.globalPostBuild) > 0 {
		logSection("Post-build hooks:")
		for _, hook := range opts.globalPostBuild {
			logBullet("%s", hook)
		}
	}
	if len(opts.toolPostBuildHooks) > 0 {
		logSection("Post-build hooks (%s):", opts.tool)
		for _, hook := range opts.toolPostBuildHooks {
			logBullet("%s", hook)
		}
	}
	if len(opts.repoPostBuildHooks) > 0 {
		logSection("Post-build hooks (repo: %s):", strings.Join(opts.matchedRepoNames, ", "))
		for _, hook := range opts.repoPostBuildHooks {
			logBullet("%s", hook)
		}
	}

	// Check if image already exists (skip if force rebuild requested)
	exists := false
	if !opts.forceBuild {
		var err error
		exists, err = backendClient.ImageExists(ctx, opts.imageTag)
		if err != nil {
			exists = false
		}
	}

	if opts.progress != nil {
		opts.progress.SetSection("Building environment")
	}
	logSection("Building environment for %s...", opts.tool)
	if opts.forceBuild {
		logBullet("Force rebuild requested, ignoring cache")
	}

	if exists {
		logSuccessBullet("Environment cached")
		return nil
	}

	_, err := backendClient.Build(ctx, backend.BuildOptions{
		Dockerfile: opts.dockerfile,
		Target:     opts.tool,
		Tag:        opts.imageTag,
		BuildArgs:  opts.buildArgs,
		MountsRO:   opts.mountsRO,
		MountsRW:   opts.mountsRW,
		NoCache:    opts.forceBuild,
		OnProgress: func(msg string) {
			if opts.verbose {
				fmt.Fprint(opts.stderr, msg)
			} else if opts.progress != nil {
				opts.progress.SetDetail(msg)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build environment: %w", err)
	}
	logSuccessBullet("Environment ready")
	return nil
}

// envLogInfo holds environment variable categorization for logging.
type envLogInfo struct {
	explicitGlobal []string // explicit from cfg.Env (KEY=VALUE)
	explicitTool   []string // explicit from toolCfg.Env (KEY=VALUE)
	explicitRepo   []string // explicit from repoCfg.Env (KEY=VALUE)
	fromHost       []string // lifted from host env
	notFound       []string // configured but not in host env
}

// collectEnvVars gathers environment variables from config and host.
func collectEnvVars(tool string, cfg config.Config, cwd string) (envVars []string, log envLogInfo) {
	// Get git identity
	gitName, gitEmail := git.GetGitIdentity()
	if gitName != "" {
		envVars = append(envVars,
			"GIT_AUTHOR_NAME="+gitName,
			"GIT_COMMITTER_NAME="+gitName,
		)
	}
	if gitEmail != "" {
		envVars = append(envVars,
			"GIT_AUTHOR_EMAIL="+gitEmail,
			"GIT_COMMITTER_EMAIL="+gitEmail,
		)
	}

	// Process global env vars (passthrough if no '=', explicit if has '=')
	for _, e := range cfg.Env {
		if strings.Contains(e, "=") {
			envVars = append(envVars, e)
			log.explicitGlobal = append(log.explicitGlobal, strings.SplitN(e, "=", 2)[0])
		} else if val := os.Getenv(e); val != "" {
			envVars = append(envVars, e+"="+val)
			log.fromHost = append(log.fromHost, e)
		} else {
			log.notFound = append(log.notFound, e)
		}
	}

	// Tool-specific env vars
	if toolCfg, ok := cfg.Tools[tool]; ok {
		for _, e := range toolCfg.Env {
			if strings.Contains(e, "=") {
				envVars = append(envVars, e)
				log.explicitTool = append(log.explicitTool, strings.SplitN(e, "=", 2)[0])
			} else if val := os.Getenv(e); val != "" {
				envVars = append(envVars, e+"="+val)
				log.fromHost = append(log.fromHost, e)
			} else {
				log.notFound = append(log.notFound, e)
			}
		}
	}

	// Repo-specific env vars
	for _, rm := range GetMatchingRepos(cfg, cwd) {
		for _, e := range rm.Config.Env {
			if strings.Contains(e, "=") {
				envVars = append(envVars, e)
				log.explicitRepo = append(log.explicitRepo, strings.SplitN(e, "=", 2)[0])
			} else if val := os.Getenv(e); val != "" {
				envVars = append(envVars, e+"="+val)
				log.fromHost = append(log.fromHost, e)
			} else {
				log.notFound = append(log.notFound, e)
			}
		}
	}

	return envVars, log
}

// logRunConfigOptions contains options for logging run configuration.
type logRunConfigOptions struct {
	stderr           io.Writer
	tool             string
	mountsRO         []string
	mountsRW         []string
	envLog           envLogInfo
	globalPreRun     []string
	toolPreRun       []string
	repoPreRun       []string
	matchedRepoNames []string
	containerName    string
	verbose          bool
	progress         *cli.Progress
}

// logRunConfig logs the run configuration to stderr.
func logRunConfig(opts logRunConfigOptions) {
	// Helper to log only in verbose mode
	logSection := func(format string, args ...any) {
		if opts.verbose {
			cli.LogTo(opts.stderr, format, args...)
		}
	}
	logBullet := func(format string, args ...any) {
		if opts.verbose {
			cli.LogBulletTo(opts.stderr, format, args...)
		}
	}

	// Get git identity for logging
	gitName, gitEmail := git.GetGitIdentity()
	if gitName != "" {
		logSection("Git identity: %s <%s>", gitName, gitEmail)
	}

	// Log mounts
	if opts.progress != nil {
		opts.progress.SetSection("Mounts")
	}
	seen := make(map[string]bool)
	if len(opts.mountsRO) > 0 {
		logSection("Mounts (read-only):")
		for _, m := range opts.mountsRO {
			if _, err := os.Lstat(m); err != nil {
				continue
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			logBullet("%s", tilde.Path(m))
		}
	}
	logSection("Mounts (read-write):")
	for _, m := range opts.mountsRW {
		if _, err := os.Lstat(m); err != nil {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		logBullet("%s", tilde.Path(m))
	}

	// Log environment variables
	if opts.progress != nil {
		opts.progress.SetSection("Environment")
	}
	if len(opts.envLog.explicitGlobal) > 0 {
		logSection("Environment (config):")
		for _, name := range opts.envLog.explicitGlobal {
			logBullet("%s", name)
		}
	}
	if len(opts.envLog.explicitTool) > 0 {
		logSection("Environment (config, %s):", opts.tool)
		for _, name := range opts.envLog.explicitTool {
			logBullet("%s", name)
		}
	}
	if len(opts.envLog.explicitRepo) > 0 {
		logSection("Environment (config, repo: %s):", strings.Join(opts.matchedRepoNames, ", "))
		for _, name := range opts.envLog.explicitRepo {
			logBullet("%s", name)
		}
	}
	if len(opts.envLog.fromHost) > 0 || len(opts.envLog.notFound) > 0 {
		logSection("Environment (host):")
		for _, name := range opts.envLog.fromHost {
			logBullet("%s", name)
		}
		for _, name := range opts.envLog.notFound {
			logBullet("%s (not set)", name)
		}
	}

	// Log pre-run hooks
	if opts.progress != nil {
		opts.progress.SetSection("Pre-run hooks")
	}
	if len(opts.globalPreRun) > 0 {
		logSection("Pre-run hooks:")
		for _, hook := range opts.globalPreRun {
			logBullet("%s", hook)
		}
	}
	if len(opts.toolPreRun) > 0 {
		logSection("Pre-run hooks (%s):", opts.tool)
		for _, hook := range opts.toolPreRun {
			logBullet("%s", hook)
		}
	}
	if len(opts.repoPreRun) > 0 {
		logSection("Pre-run hooks (repo: %s):", strings.Join(opts.matchedRepoNames, ", "))
		for _, hook := range opts.repoPreRun {
			logBullet("%s", hook)
		}
	}

	if opts.progress != nil {
		opts.progress.SetSection("Container")
	}
	logSection("Container name: %s", opts.containerName)
}

// preparePreRunHooks combines and prepares pre-run hooks including mount wait.
func preparePreRunHooks(globalHooks, toolHooks, repoHooks []string, mountsRO, mountsRW []string, verbose bool) []string {
	preRunHooks := append(globalHooks, toolHooks...)
	preRunHooks = append(preRunHooks, repoHooks...)

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
	if mountWaitHook := mountwait.GenerateScript(allMountPaths, verbose); mountWaitHook != "" {
		preRunHooks = append([]string{mountWaitHook}, preRunHooks...)
	}

	return preRunHooks
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

// dockerfileWithHooks returns a dockerfile with post-build hooks injected.
// globalHooks are injected into the base stage, toolHooks are injected into the
// specific tool stage, repoHooks are also injected into the tool stage (after toolHooks).
func dockerfileWithHooks(dockerfileTemplate string, globalHooks []string, tool string, toolHooks, repoHooks []string) string {
	result := dockerfileTemplate

	// Inject global hooks at base stage marker
	if len(globalHooks) > 0 {
		var runCmds strings.Builder
		for _, hook := range globalHooks {
			runCmds.WriteString("RUN ")
			runCmds.WriteString(hook)
			runCmds.WriteString("\n")
		}
		result = strings.Replace(result, "# SILO_POST_BUILD_HOOKS\n", runCmds.String()+"# SILO_POST_BUILD_HOOKS\n", 1)
	}

	// Inject tool-specific and repo-specific hooks at tool stage marker
	allToolStageHooks := append(toolHooks, repoHooks...)
	if len(allToolStageHooks) > 0 {
		toolMarker := fmt.Sprintf("# SILO_POST_BUILD_HOOKS_%s\n", strings.ToUpper(tool))
		var runCmds strings.Builder
		for _, hook := range allToolStageHooks {
			runCmds.WriteString("RUN ")
			runCmds.WriteString(hook)
			runCmds.WriteString("\n")
		}
		result = strings.Replace(result, toolMarker, runCmds.String()+toolMarker, 1)
	}

	return result
}

// expandPath expands ~ to the user's home directory.
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
