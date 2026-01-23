# Silo

Run AI coding assistants (Claude Code, OpenCode, GitHub Copilot CLI) in isolated Docker containers with proper security sandboxing.

```
███████╗██╗██╗      ██████╗
██╔════╝██║██║     ██╔═══██╗
███████╗██║██║     ██║   ██║
╚════██║██║██║     ██║   ██║
███████║██║███████╗╚██████╔╝
╚══════╝╚═╝╚══════╝ ╚═════╝
```

## Features

- **Isolated Execution**: Run AI coding tools in Docker containers with security sandboxing
- **Multiple Tools**: Support for Claude Code, OpenCode, and GitHub Copilot CLI
- **Automatic Setup**: Builds Docker images with all required development tools (Go, Rust, Deno, GitHub CLI)
- **Git Integration**: Automatically configures git identity in the container
- **Worktree Support**: Detects and mounts git worktree common directories
- **Configurable**: Flexible configuration system for mounts, environment variables, and API keys
- **Beautiful CLI**: Interactive tool selection with colorful, informative output

## Installation

### From Source

```bash
go install github.com/leighmcculloch/silo@latest
```

### Build Locally

```bash
git clone https://github.com/leighmcculloch/silo.git
cd silo
go build -o silo .
```

## Prerequisites

- **Docker**: Silo requires Docker to be installed and running
- **Go 1.21+**: Required if building from source

## Usage

### Interactive Mode

Run `silo` without arguments to interactively select a tool:

```bash
silo
```

### Run a Specific Tool

```bash
# Run Claude Code
silo claude

# Run OpenCode
silo opencode

# Run GitHub Copilot CLI
silo copilot
```

### Pass Arguments to the Tool

```bash
silo claude -- --help
```

### Show Current Configuration

```bash
silo config
```

### Create a Local Configuration File

```bash
silo init
```

### Shell Completion

Generate shell completion scripts:

```bash
# Bash
silo completion bash > /etc/bash_completion.d/silo

# Zsh
silo completion zsh > "${fpath[1]}/_silo"

# Fish
silo completion fish > ~/.config/fish/completions/silo.fish
```

## Configuration

Silo uses a hierarchical configuration system that merges settings from multiple sources:

1. **Global config**: `~/.config/silo/silo.jsonc` (or `$XDG_CONFIG_HOME/silo/silo.jsonc`)
2. **Local configs**: `silo.jsonc` files from root to current directory (closer files override)

Configuration files support JSONC (JSON with Comments), allowing `//` and `/* */` style comments.

### Configuration Schema

```jsonc
{
  // Read-only directories or files to mount into the container
  "mounts_ro": [
    "/path/to/readonly/mount"
  ],
  // Read-write directories or files to mount into the container
  "mounts_rw": [
    "/path/to/readwrite/mount"
  ],
  // Environment variables: names without '=' pass through from host,
  // names with '=' set explicitly (e.g., "FOO=bar")
  "env": [
    "MY_API_KEY",
    "FOO=bar"
  ],
  // Shell commands to run before starting the container
  "prehook": [
    "source ~/.env_api_keys"
  ],
  // Tool-specific configuration
  "tools": {
    "claude": {
      "mounts_ro": [],
      "mounts_rw": [],
      "env": []
    }
  }
}
```

### Configuration Options

| Option | Description |
|--------|-------------|
| `mounts_ro` | Read-only directories or files to mount into the container |
| `mounts_rw` | Read-write directories or files to mount into the container |
| `env` | Environment variables: names without `=` pass through from host, with `=` set explicitly |
| `prehook` | Shell commands to run before starting the container |
| `tools` | Tool-specific configuration overrides |

### Example Configurations

#### Global Configuration (`~/.config/silo/silo.jsonc`)

```json
{
  "env": [
    "GITHUB_TOKEN",
    "ANTHROPIC_API_KEY"
  ],
  "source_files": [
    "~/.env_api_keys"
  ]
}
```

#### Project Configuration (`silo.jsonc`)

```json
{
  "mounts_rw": [
    "/path/to/shared/libraries"
  ],
  "env": [
    "PROJECT_ENV=development"
  ],
  "tools": {
    "claude": {
      "env": [
        "CUSTOM_CLAUDE_TOKEN"
      ]
    }
  }
}
```

## What Gets Mounted

By default, Silo mounts:

- **Current directory**: Your project directory
- **Tool-specific directories**:
  - Claude: `~/.claude.json`, `~/.claude/`
  - OpenCode: `~/.config/opencode/`, `~/.local/share/opencode/`
  - Copilot: `~/.config/.copilot/`
- **Git worktree directories**: Automatically detected

## Container Environment

The Docker container includes:

- Ubuntu 24.04 base
- Go (latest version)
- Rust (stable + nightly with wasm32v1-none target)
- Deno
- GitHub CLI
- MCP servers (GitHub, Perplexity, Context7)

## Security

Containers run with:

- `--privileged=false`
- `--cap-drop=ALL`
- `--security-opt=no-new-privileges:true`

This provides a security boundary between the AI tool and your host system.

## Development

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o silo ./cmd/silo
```

## License

MIT
