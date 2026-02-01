# Silo

Run AI coding assistants in containers/vms.

```
███████╗██╗██╗      ██████╗
██╔════╝██║██║     ██╔═══██╗
███████╗██║██║     ██║   ██║
╚════██║██║██║     ██║   ██║
███████║██║███████╗╚██████╔╝
╚══════╝╚═╝╚══════╝ ╚═════╝
```

Silo lets you run AI coding tools like Claude Code, OpenCode, and GitHub Copilot CLI in isolated Docker containers or Apple containers (lightweight VMs). The coding tools are configured to run in yolo mode.

> [!WARNING]
> This is a side-project and the isolation and sandboxing is best effort. No sandbox is perfect. Use at your own risk.

## Quick Start

```bash
# Install
go install github.com/leighmcculloch/silo@latest

# Run (interactive tool selection)
silo

# Or run a specific tool
silo claude
```

That's it. Silo builds the environment automatically on first run.

## Why Silo?

AI coding assistants need broad access to work effectively—they read files, run commands, and modify code. This creates a tension: give them access and accept the risk, or restrict them and lose capability.

Silo resolves this by running AI tools in isolated containers/vms with:

- Limited access to the work directory and tool configs
- Reusing local configs for moving back and forth from a local agent and a silo agent
- **Git integration**: Your git identity is automatically configured inside the container

## Supported Tools

| Tool | Command | Description |
|------|---------|-------------|
| Claude Code | `silo claude` | Anthropic's CLI for Claude |
| OpenCode | `silo opencode` | AI coding CLI |
| GitHub Copilot CLI | `silo copilot` | GitHub's Copilot CLI |

## Installation

```bash
go install github.com/leighmcculloch/silo@latest
```

### Prerequisites

- **Go 1.25+**: To install
- **Docker or any compatible container runtime**: Required for the `docker` backend
- **Apple Container**: Required for the `container` backend (`brew install container`)

## Usage

### Basic Usage

```bash
# Interactive tool selection
silo

# Run a specific tool
silo claude
silo opencode
silo copilot
```

### Choosing a Backend

Silo supports two backends and auto-detects which one to use if none specified:

| Backend | Flag | Description |
|---------|------|-------------|
| Container | `--backend container` | Apple lightweight VMs (macOS only) |
| Docker | `--backend docker` | Uses Docker containers |

**Default behavior**: If the `container` command is installed, Silo uses the container backend. Otherwise, it falls back to Docker.

```bash
# Use auto-detected backend (container if available, else docker)
silo claude

# Explicitly use Docker
silo --backend docker claude

# Explicitly use Apple container backend
silo --backend container claude
```

You can also set the backend in your configuration file.

#### Backend Comparison

| Feature | Docker | Apple Container |
|---------|--------|-----------------|
| Platform | Any | macOS only |
| Isolation | Shared Linux VM | Per-container VM |
| File mounts | Direct | Staged + symlinks |
| Security | Dropped caps, no-new-privileges | VM isolation |
| Resource control | Docker defaults | Explicit CPU/memory |
| API | Docker SDK | CLI subprocess |


#### Why Apple Containers on macOS?

Docker on macOS runs all containers inside a single shared Linux VM that typically has broad access to the host filesystem (e.g., your entire home directory). The containers inside that VM share this access.

Apple containers are different: each container runs in its own minimal lightweight VM with only the specific directories you've mounted. This provides stronger isolation since each VM has its own resource constraints and no shared filesystem access beyond what's explicitly configured. See [apple/container#technical-overview](https://github.com/apple/container/blob/main/docs/technical-overview.md) and [youtube](https://www.youtube.com/watch?v=JvQtvbhtXmo) for more details.

## Configuration

Silo uses a hierarchical configuration system. Settings are merged from multiple files, with later files overriding earlier ones.

### Configuration Files

Configuration is loaded in this order (later overrides earlier):

1. **Built-in defaults** — Defaults for each tool
2. **Global config** — `~/.config/silo/silo.jsonc`, respecting `XDG_CONFIG_HOME`
3. **Local configs** — `silo.jsonc` files from filesystem root to current directory

For an example config file, see my config file at [leighmcculloch/dotfiles#silo.jsonc](https://github.com/leighmcculloch/dotfiles/blob/main/files/config/silo/silo.jsonc).

### Quick Setup

```bash
# Create a configuration file interactively
silo config init

# Or specify directly
silo config init --global  # ~/.config/silo/silo.jsonc
silo config init --local   # ./silo.jsonc
```

### Configuration Format

Silo uses JSONC (JSON with Comments). All fields are optional.

```jsonc
{
  // Backend: "docker" or "container" (default: container if installed, else docker)
  "backend": "container",

  // Default tool: "claude", "opencode", or "copilot" (if not set, interactive prompt is shown)
  "tool": "claude",

  // Read-only mounts (paths visible to the AI but not writable)
  "mounts_ro": [
    "/path/to/reference/docs"
  ],

  // Read-write mounts (paths the AI can modify)
  "mounts_rw": [
    "/path/to/shared/libraries"
  ],

  // Environment variables
  // - Without '=': Pass through from host (e.g., "GITHUB_TOKEN")
  // - With '=': Set explicitly (e.g., "DEBUG=true")
  "env": [
    "GITHUB_TOKEN",
    "ANTHROPIC_API_KEY",
    "MY_VAR=custom_value"
  ],

  // Shell commands to run inside the container after building the image (once per build)
  "post_build_hooks": [
    "deno install --global --allow-env --allow-net npm:some-mcp-server"
  ],

  // Shell commands to run inside the container before the tool (every run)
  "pre_run_hooks": [
    "source ~/.env_api_keys"
  ],

  // Tool-specific configuration (merged with global settings)
  "tools": {
    "claude": {
      "mounts_rw": ["~/.claude.json", "~/.claude"],
      "env": ["CLAUDE_SPECIFIC_VAR"]
    }
  }
}
```

### Configuration Merging

Arrays are **appended** (not replaced) when configs are merged:

```jsonc
// ~/.config/silo/silo.jsonc (global)
{ "env": ["GITHUB_TOKEN"] }

// ./silo.jsonc (local)
{ "env": ["PROJECT_TOKEN"] }

// Result: env = ["GITHUB_TOKEN", "PROJECT_TOKEN"]
```

The `backend` and `tool` settings are replaced (later config wins).

### Managing Configuration

```bash
# Show merged configuration with source annotations
silo config show

# List all config file paths being checked
silo config paths

# Edit a config file in your $EDITOR
silo config edit

# Show built-in default configuration
silo config default
```

Example output from `silo config show`:
```jsonc
{
  "backend": "docker", // ~/.config/silo/silo.jsonc
  "mounts_rw": [
    "~/.claude.json", // default
    "~/.claude" // default
  ],
  "env": [
    "GITHUB_TOKEN", // ~/.config/silo/silo.jsonc
    "PROJECT_KEY" // /path/to/project/silo.jsonc
  ]
}
```

## Default Behavior

### What Gets Mounted Automatically

Silo automatically mounts these paths (read-write):

| Tool | Auto-mounted Paths |
|------|-------------------|
| All | Current working directory |
| All | Git worktree common directories (detected automatically) |
| Claude | `~/.claude.json`, `~/.claude/` |
| OpenCode | `~/.config/opencode/`, `~/.local/share/opencode/`, `~/.local/state/opencode/` (respecting XDG env vars) |
| Copilot | `~/.config/.copilot/` (respecting XDG env vars) |

### Environment Variables

Some environment variables are automatically passed through:

| Tool | Auto-passed Variables |
|------|----------------------|
| Copilot | `COPILOT_GITHUB_TOKEN` |

Git identity is configured automatically from your host:
- `GIT_AUTHOR_NAME`, `GIT_COMMITTER_NAME`
- `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_EMAIL`

## Container Environment

The container environment includes a development toolchain. This is not
configurable today, other than through the hooks.

### Pre-installed Software

| Category | Included |
|----------|----------|
| **Base** | Ubuntu 24.04, build-essential, pkg-config, libssl-dev |
| **Languages** | Node.js (latest), Go (latest), Rust (stable) |
| **Tools** | git, curl, jq, zstd, unzip, GitHub CLI |
| **Go** | gopls (LSP server) |
| **Rust** | rust-analyzer, wasm32v1-none target |

### Pre-installed MCP Servers

| Server | Description |
|--------|-------------|
| `github-mcp-server` | GitHub integration for AI tools |


## Advanced Usage

### Hooks

Silo supports two types of hooks for customizing the container environment:

#### Post-build Hooks

Post-build hooks run once after the image is built. Use them to install additional software or MCP servers:

```jsonc
{
  "post_build_hooks": [
    "deno install --global --allow-env --allow-net npm:server-perplexity-ask",
    "go install github.com/example/my-mcp-server@latest"
  ]
}
```

Post-build hooks are chained with `&&`, so if any fails, the build will fail.

#### Pre-run Hooks

Pre-run hooks run every time before the AI tool starts. Use them to set up environment variables or run initialization scripts:

```jsonc
{
  "pre_run_hooks": [
    "source ~/.env_api_keys",
    "export CUSTOM_VAR=$(cat /secrets/key)"
  ]
}
```

Pre-run hooks are chained with `&&`, so if any fails, the tool won't start.

### Image Caching

Silo uses content-addressed image tagging. Images are tagged with a hash of:
- Dockerfile content
- Target tool name
- Build arguments (HOME, USER, UID)

This means:
- Images are only rebuilt when something changes
- Multiple users with the same setup share cached images
- Different tools have separate images

### Container Naming

Containers are named `<project>-<N>` where:
- `<project>` is your current directory name
- `<N>` is auto-incremented based on existing containers

Example: If you're in `~/Code/myapp`, containers will be named `myapp-1`, `myapp-2`, etc.

### Terminal Handling

- **TTY support**: Full terminal emulation with colors and formatting
- **Resize handling**: Terminal resize signals (SIGWINCH) are forwarded
- **Double Ctrl-C**: Press Ctrl-C twice quickly to force-kill a stuck container
- **Clean exit**: Terminal state is restored on exit

### Listing Containers

See all silo-created containers:

```bash
# List from all backends
silo list

# List from specific backend only
silo list --backend docker
silo list --backend container
```

Output shows container name, image, backend, and status.

### Cleanup

Remove all silo-created containers:

```bash
# Remove from all backends
silo destroy

# Remove from specific backend only
silo destroy --backend docker
silo destroy --backend container
```

## Examples

### Minimal Setup

Just run `silo claude` — it works out of the box with defaults.

### API Keys from Environment

```jsonc
// ~/.config/silo/silo.jsonc
{
  "env": [
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "GITHUB_TOKEN"
  ]
}
```

These will be passed through from your host environment.

### API Keys from File

```jsonc
// ~/.config/silo/silo.jsonc
{
  "mounts_ro": [
    "~/.env_api_keys"
  ],
  "pre_run_hooks": [
    "source ~/.env_api_keys"
  ]
}
```

Where `~/.env_api_keys` contains env vars like:
```bash
export ANTHROPIC_API_KEY=sk-ant-...
export GITHUB_TOKEN=ghp_...
```

### Project-specific Configuration

```jsonc
// ~/Code/my-rust-project/silo.jsonc
{
  "mounts_rw": [
    "~/.cargo/registry"  // Share cargo cache
  ],
  "env": [
    "RUST_BACKTRACE=1"
  ]
}
```

### Using Apple Container Backend

```jsonc
// ~/.config/silo/silo.jsonc
{
  "backend": "container"
}
```

Or per-invocation:
```bash
silo --backend container claude
```

### Multiple Tool Configuration

```jsonc
{
  "env": ["GITHUB_TOKEN"],  // Shared by all tools
  "tools": {
    "claude": {
      "env": ["ANTHROPIC_API_KEY"]
    },
    "copilot": {
      "env": ["COPILOT_GITHUB_TOKEN"]
    }
  }
}
```

