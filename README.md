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

Silo lets you run AI coding tools like Claude Code, Cline, Codex, OpenCode, GitHub Copilot CLI, and Mistral Vibe in isolated Docker containers, Apple containers (lightweight VMs), or Fly.io machines (remote VMs). The coding tools are configured to run in auto-approve mode.

> [!WARNING]
> Built using AI. No isolation is perfect. Use at your own risk.

> [!WARNING]
> Even though containers and VMs in silo act as an isolate, silo mounts directories from the local file system which means that the host is not completely isolated from the agents.

## Quick Start

```bash
# Install
go install github.com/leighmcculloch/silo@latest

# Or with Homebrew
brew tap leighmcculloch/silo
brew install --HEAD leighmcculloch/silo/silo

# Run
silo
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
| Cline CLI | `silo cline` | Cline's terminal coding agent |
| Codex CLI | `silo codex` | OpenAI's CLI coding agent |
| OpenCode | `silo opencode` | AI coding CLI |
| GitHub Copilot CLI | `silo copilot` | GitHub's Copilot CLI |
| Mistral Vibe | `silo vibe` | Mistral's CLI coding assistant |

## Installation

### Go

```bash
go install github.com/leighmcculloch/silo@latest
```

### Homebrew

```
brew tap leighmcculloch/silo
brew install --HEAD leighmcculloch/silo/silo
```

Upgrade with:

```
brew upgrade --fetch-head leighmcculloch/silo/silo
```

### Prerequisites

- **Go 1.25+**: To install silo
- **Docker or any compatible container runtime**: Required for the `docker` backend
- **Apple Container**: Required for the `container` backend (see [apple/container](https://github.com/apple/container))
- **Fly.io CLI (`fly`)**: Required for the `fly` backend (see [fly.io/docs/flyctl/install](https://fly.io/docs/flyctl/install/))

## Usage

### Basic Usage

```bash
# Interactive tool selection
silo

# Run a specific tool
silo claude
silo cline
silo codex
silo opencode
silo copilot
silo vibe

# Pass arguments to the tool (after --)
silo claude -- --help
silo opencode -- --version
```

### Choosing a Backend

Silo supports three backends and auto-detects which one to use if none specified:

| Backend | Flag | Description |
|---------|------|-------------|
| Container | `--backend container` | Apple lightweight VMs (macOS only) |
| Docker | `--backend docker` | Uses Docker containers |
| Fly | `--backend fly` | Remote VMs on Fly.io |

**Default behavior**: If the `container` command is installed, Silo uses the container backend. Otherwise, it falls back to Docker. The `fly` backend must be selected explicitly.

```bash
# Use auto-detected backend (container if available, else docker)
silo claude

# Explicitly use Docker
silo --backend docker claude

# Explicitly use Apple container backend
silo --backend container claude

# Explicitly use Fly.io backend
silo --backend fly claude

# Force rebuild of the container image (ignore cache)
silo --force-build claude
```

You can also set the backend in your configuration file.

#### Backend Comparison

| Feature | Docker | Apple Container | Fly |
|---------|--------|-----------------|-----|
| Platform | Any | macOS only | Any |
| Isolation | Shared Linux VM | Per-container VM | Remote VM |
| Docker Inside | Shared Engine | Per-container Engine | Per-container Engine |
| File mounts | Direct | Staged + symlinks | Synced (mutagen) |
| Security | Dropped caps, no-new-privileges | VM isolation | Remote VM isolation |
| Resource control | Docker defaults | Explicit CPU/memory | Fly machine defaults |
| API | Docker SDK | CLI subprocess | Fly CLI subprocess |
| Reconnect | No | No | Yes (`silo reconnect`) |


#### Why Apple Containers on macOS?

Docker on macOS runs all containers inside a single shared Linux VM that typically has broad access to the host filesystem (e.g., your entire home directory). The containers inside that VM share this access.

Apple containers are different: each container runs in its own minimal lightweight VM with only the specific directories you've mounted. This provides stronger isolation since each VM has its own resource constraints and no shared filesystem access beyond what's explicitly configured. See [apple/container#technical-overview](https://github.com/apple/container/blob/main/docs/technical-overview.md) and [youtube](https://www.youtube.com/watch?v=JvQtvbhtXmo) for more details.

#### Fly.io Backend

The Fly backend runs your silo environment on remote Fly.io machines. This is useful when you want to offload compute to a remote VM or run silo from a machine that doesn't have Docker or Apple containers.

**Setup:**

1. Install the Fly CLI: `curl -L https://fly.io/install.sh | sh`
2. Install [mutagen](https://mutagen.io) for file sync: `brew install mutagen-io/mutagen/mutagen`
3. Authenticate: `fly auth login`
4. Create a Fly app for silo (one-time): `fly apps create <your-app-name>`
5. Configure silo with your app name:
   ```jsonc
   // ~/.config/silo/silo.jsonc
   {
     "backend": "fly",
     "backends": {
       "fly": { "app": "<your-app-name>" }
     }
   }
   ```

**How it works:**

- **Build**: Images are built remotely using Fly's Depot builders and pushed to `registry.fly.io/<app>:<tag>`
- **Run**: A Fly machine is created from the image, files are continuously synced using [mutagen](https://mutagen.io), then an interactive SSH session connects you to the tool
- **Exit**: Final changes are flushed back, then the machine is destroyed
- **Reconnect**: If your connection drops, use `silo reconnect <name>` to reconnect to the still-running machine

**Configuration:**

| Setting | Config Key | Env Var | Default | Description |
|---------|-----------|---------|---------|-------------|
| App name | `backends.fly.app` | `FLY_APP` | *(required)* | The Fly app to create machines in |
| Region | `backends.fly.region` | `FLY_REGION` | `syd` | The Fly region for new machines |

App names are globally unique on Fly.io — you must create your own with `fly apps create <name>`.

**File sync:**

Since Fly machines don't support bind mounts, files are continuously synced using [mutagen](https://mutagen.io):
- **Read-only mounts** use one-way sync (local → remote)
- **Read-write mounts** use bidirectional sync (changes on either side are propagated)

Mutagen syncs only deltas, so it's efficient even for large directories. If your connection drops, the remote machine keeps working with the files it had. When you reconnect, mutagen picks up where it left off, syncing only what changed on either side.

Requires `mutagen` installed locally (`brew install mutagen-io/mutagen/mutagen`).

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
  // Backend: "docker", "container", or "fly" (default: container if installed, else docker)
  "backend": "container",

  // Default tool: "claude", "cline", "codex", "opencode", "copilot", or "vibe" (if not set, interactive prompt is shown)
  "tool": "claude",

  // Backend-specific configuration
  // "backends": {
  //   "fly": {
  //     "app": "my-silo-app",  // required for fly backend
  //     "region": "syd"        // default: "syd"
  //   }
  // },

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

  // Host:container port mappings to publish
  "ports": [
    "8080:8080"
  ],

  // Tool-specific configuration (merged with global settings)
  "tools": {
    "claude": {
      "mounts_rw": ["~/.claude.json", "~/.claude"],
      "env": ["CLAUDE_SPECIFIC_VAR"]
    }
  },

  // Repository-specific configuration (applied when git remote URL contains the key)
  "repos": {
    "github.com/myorg": {
      "env": ["ORG_API_KEY"],
      "post_build_hooks": ["npm install -g @myorg/cli"]
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

| Tool | Auto-mounted Paths (read-write) |
|------|-------------------|
| All | Current working directory |
| All | Git worktree common directories (detected automatically) |
| Claude | `~/.claude.json`, `~/.claude/` |
| Cline | `~/.cline/`, `~/.claude.json`, `~/.claude/`, `~/.codex/` |
| Codex | `~/.codex/` |
| OpenCode | `~/.config/opencode/`, `~/.local/share/opencode/`, `~/.local/state/opencode/` (respecting XDG env vars) |
| Copilot | `~/.config/.copilot/` (respecting XDG env vars) |
| Vibe | `~/.vibe/` |

Additionally, some tools mount paths read-only to share configuration:

| Tool | Auto-mounted Paths (read-only) |
|------|-------------------|
| OpenCode | `~/.claude/` (for sharing CLAUDE.md files) |
| Copilot | `~/.claude/` (for sharing CLAUDE.md files) |

### Published Ports

Some tools publish container ports to the host by default:

| Tool | Published Ports |
|------|----------------|
| Cline | `3484:3484` (Cline browser preview) |

### Environment Variables

Some environment variables are automatically set or passed through:

| Tool | Auto-set Variables |
|------|----------------------|
| Cline | `CLINE_NO_AUTO_UPDATE=1` |
| Codex | `OPENAI_API_KEY` (passed through from host) |
| OpenCode | `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` |
| Copilot | `COPILOT_GITHUB_TOKEN` (passed through from host) |
| Vibe | `MISTRAL_API_KEY` (passed through from host) |

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
| **Tools** | git, curl, jq, zstd, unzip, zsh, GitHub CLI, Docker CE |
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

### Auto-rebuild on Tool Updates

Silo automatically detects when a new version of a tracked tool is available and triggers a rebuild. Today that includes Cline, Claude Code, Codex CLI, and GitHub Copilot CLI. On each run, a background fetch checks the latest version and caches it to disk. The cached version is included in the image hash, so when a new release is published the image tag changes and a rebuild is triggered on the next run.

This adds zero latency — the version fetch happens asynchronously and the cached value from the previous run is used. New versions are picked up on the run after they are detected. Use `--force-build` to force a rebuild at any time.

### Container Naming

Containers are named `<project>-<N>` where:
- `<project>` is your current directory name
- `<N>` is auto-incremented based on existing containers

Example: If you're in `~/Code/myapp`, containers will be named `myapp-1`, `myapp-2`, etc.

### Terminal Handling

- **TTY support**: Full terminal emulation with colors and formatting
- **Resize handling**: Terminal resize signals (SIGWINCH) are forwarded
- **Triple Ctrl-C**: Press Ctrl-C three times quickly to force-kill a stuck container
- **Clean exit**: Terminal state is restored on exit

### Reconnecting (Fly Backend)

If your SSH connection drops while using the Fly backend, the machine keeps running. You can reconnect to it:

```bash
# List running machines to find the name
silo ls --backend fly

# Reconnect to a running machine
silo --backend fly reconnect myproject-1
```

On a normal exit, files are synced back and the machine is destroyed automatically. When reconnecting, the machine is not destroyed on disconnect — use `silo rm` to clean up manually if needed.

### Listing Containers

See all silo-created containers:

```bash
# List from all backends
silo ls

# List from specific backend only
silo ls --backend docker
silo ls --backend container
silo ls --backend fly

# Quiet mode (just container names)
silo ls -q
```

Output shows container name, image, backend, and status.

### Removing Containers

Remove specific silo containers by name, or run `silo rm` with no arguments to get an interactive multi-select list of all containers:

```bash
# Open an interactive multi-select prompt
silo rm

# Remove specific containers
silo rm myproject-1 myproject-2

# Remove all silo containers
silo rm $(silo ls -q)

# Remove from specific backend only
silo rm --backend docker myproject-1
silo rm --backend container myproject-2
silo rm --backend fly myproject-3
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

### Using Fly.io Backend

```jsonc
// ~/.config/silo/silo.jsonc
{
  "backend": "fly",
  "backends": {
    "fly": { "app": "my-silo-app" }
  }
}
```

```bash
# First-time setup
fly auth login
fly apps create my-silo-app

# Run
silo claude

# If your connection drops, reconnect
silo reconnect myproject-1
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

### Repository-specific Configuration

Apply configuration automatically based on git remote URLs. When you run silo in a git repository, it checks if any remote URL contains the specified pattern and applies that configuration.

```jsonc
// ~/.config/silo/silo.jsonc
{
  "repos": {
    "github.com/mycompany": {
      "tool": "opencode",
      "env": ["COMPANY_API_KEY"],
      "post_build_hooks": ["npm install -g @mycompany/internal-cli"],
      "mounts_ro": ["~/.mycompany-config"]
    },
    "github.com/mycompany/special-repo": {
      "tool": "claude",
      "env": ["SPECIAL_TOKEN"],
      "pre_run_hooks": ["echo 'Setting up special-repo'"]
    },
    "gitlab.com/client-project": {
      "env": ["CLIENT_TOKEN"],
      "pre_run_hooks": ["echo 'Working on client project'"]
    }
  }
}
```

This is useful for:
- Setting a default tool for an organization or specific repository
- Setting organization-specific API keys or tokens
- Installing internal tools needed for specific repositories
- Mounting configuration files only relevant to certain projects

The pattern matching is substring-based (prefix matching), so `"github.com/myorg"` matches remotes like:
- `git@github.com:myorg/repo.git`
- `https://github.com/myorg/repo.git`

When multiple patterns match, they are merged in order of specificity (shortest pattern first). In the example above, if you're in `github.com/mycompany/special-repo`:
1. First `github.com/mycompany` config is applied (tool=opencode, COMPANY_API_KEY, post_build_hooks, mounts_ro)
2. Then `github.com/mycompany/special-repo` config is merged (overrides tool=claude, adds SPECIAL_TOKEN and pre_run_hooks)

## License

Copyright 2026 Stellar Development Foundation (This is not an official project of the Stellar Development Foundation)

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
