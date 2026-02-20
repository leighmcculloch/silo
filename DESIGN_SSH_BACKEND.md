# Design: SSH Remote Backend for Silo

## Overview

This document describes the design for adding an SSH remote backend to silo, allowing AI coding tools to run on remote VMs instead of local containers. The design introduces a client-server architecture that works identically for local and remote use.

## Architecture

### Core Concept: Backend Abstraction

Silo already has a clean `Backend` interface (`backend/backend.go`) with two implementations (Docker, Apple Container). The SSH backend becomes a third implementation that delegates all operations to a remote machine over SSH.

The key insight is that the remote machine runs Docker (or another container runtime) - the SSH backend doesn't replace containers, it runs them remotely. This means:

1. The Dockerfile is transferred to the remote and built there
2. Containers run on the remote with the same isolation guarantees
3. File mounts require syncing local files to the remote first

### Architecture Diagram

```
LOCAL (macOS)                          REMOTE (Linux VM)
┌─────────────┐                       ┌──────────────────┐
│  silo CLI    │                       │  Docker daemon   │
│             │                       │                  │
│  SSH Backend ├───── SSH tunnel ──────┤  (builds/runs    │
│  (client)   │                       │   containers)    │
│             │                       │                  │
│  mutagen    ├───── file sync ───────┤  synced files    │
│  (sync)     │                       │  ~/silo-sync/    │
└─────────────┘                       └──────────────────┘
```

### Why Not a Custom Server

We considered a `silo-server` component but decided against it for v1:

- SSH + Docker on the remote is sufficient - no custom daemon needed
- The silo client can execute all operations via SSH commands
- Reduces deployment complexity (just need Docker + SSH on the remote)
- A server component can be added later if needed for performance

## Configuration

### Schema

```jsonc
{
  // Select the SSH backend
  "backend": "ssh",

  // Backend-specific configuration
  "backends": {
    "ssh": {
      // SSH connection settings
      "host": "dev-vm.example.com",
      "port": 22,                          // default: 22
      "user": "ubuntu",                    // default: current user
      "identity_file": "~/.ssh/id_ed25519", // default: SSH agent

      // Remote Docker settings
      "remote_backend": "docker",          // what runs on the remote (default: "docker")

      // File sync settings
      "sync_method": "mutagen",            // "mutagen" (default) or "rsync"
      "sync_ignore": [".git", "node_modules"], // paths to exclude from sync
      "remote_sync_root": "~/silo-sync"    // where synced files land on remote
    }
  }
}
```

### Config Changes Required

Following the configuration system instructions from CLAUDE.md:

1. **`config/config.go`** - Add `Backends` field to `Config`, add `BackendsConfig` and `SSHBackendConfig` structs, update `Merge()`, `SourceInfo`, `NewSourceInfo()`, and `trackConfigSources()`
2. **`silo.schema.json`** - Add `backends` property with SSH sub-schema
3. **`silo.jsonc.example`** - Add commented SSH backend example
4. **`main.go`** - Update `sampleConfig`, `runConfigShow()`, `runConfigDefault()`
5. **`README.md`** - Document SSH backend feature

### Config Types

```go
// BackendsConfig holds backend-specific configuration
type BackendsConfig struct {
    SSH SSHBackendConfig `json:"ssh,omitempty"`
}

// SSHBackendConfig configures the SSH remote backend
type SSHBackendConfig struct {
    Host           string   `json:"host,omitempty"`
    Port           int      `json:"port,omitempty"`            // default: 22
    User           string   `json:"user,omitempty"`            // default: current user
    IdentityFile   string   `json:"identity_file,omitempty"`   // default: SSH agent
    RemoteBackend  string   `json:"remote_backend,omitempty"`  // default: "docker"
    SyncMethod     string   `json:"sync_method,omitempty"`     // default: "mutagen"
    SyncIgnore     []string `json:"sync_ignore,omitempty"`
    RemoteSyncRoot string   `json:"remote_sync_root,omitempty"` // default: "~/silo-sync"
}
```

## Component Design

### 1. SSH Backend (`backend/ssh/`)

Implements `backend.Backend` by executing Docker commands over SSH.

```go
// backend/ssh/ssh.go
package ssh

type Client struct {
    cfg      config.SSHBackendConfig
    sshConn  *ssh.Client   // persistent SSH connection
    syncer   Syncer        // file sync implementation
}

func NewClient(cfg config.SSHBackendConfig) (*Client, error)
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error)
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error)
func (c *Client) NextContainerName(ctx context.Context, baseName string) string
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error
func (c *Client) Exec(ctx context.Context, name string, command []string) error
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error)
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error)
func (c *Client) Close() error
```

#### Build Implementation

1. Write the Dockerfile to a temp file on the remote via SSH
2. Execute `docker build` on the remote via SSH
3. Stream build output back to the client for progress display

```go
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
    // Create temp directory on remote
    tmpDir := c.execRemote("mktemp -d")

    // Write Dockerfile to remote
    c.writeRemoteFile(tmpDir+"/Dockerfile", opts.Dockerfile)

    // Build docker image on remote
    tag := opts.Tag
    if tag == "" {
        tag = opts.Target
    }

    args := []string{"docker", "build", "-f", tmpDir + "/Dockerfile"}
    if opts.Target != "" {
        args = append(args, "--target", opts.Target)
    }
    args = append(args, "-t", tag)
    for k, v := range opts.BuildArgs {
        args = append(args, "--build-arg", k+"="+v)
    }
    if opts.NoCache {
        args = append(args, "--no-cache")
    }
    args = append(args, tmpDir)

    // Stream output
    return tag, c.execRemoteStreaming(ctx, args, opts.OnProgress)
}
```

#### Run Implementation

The Run method is the most complex because it needs to:
1. Sync local files to the remote
2. Map local mount paths to remote sync paths
3. Forward the terminal (PTY) bidirectionally
4. Handle signals (SIGWINCH for window resize)

```go
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
    // Sync files to remote
    remoteMountsRO, remoteMountsRW, err := c.syncMounts(ctx, opts.MountsRO, opts.MountsRW)
    if err != nil {
        return err
    }

    // Map workdir to remote path
    remoteWorkDir := c.remotePathFor(opts.WorkDir)

    // Build docker run command
    args := c.buildDockerRunArgs(opts, remoteWorkDir, remoteMountsRO, remoteMountsRW)

    // Execute with PTY forwarding
    return c.execInteractive(ctx, args)
}
```

#### PTY/Terminal Forwarding

Terminal forwarding over SSH requires:
- Allocating a PTY on the remote SSH session
- Setting terminal to raw mode locally
- Forwarding SIGWINCH to update remote terminal size

```go
func (c *Client) execInteractive(ctx context.Context, args []string) error {
    session, err := c.sshConn.NewSession()
    if err != nil {
        return err
    }
    defer session.Close()

    // Get local terminal size
    fd := int(os.Stdin.Fd())
    w, h, _ := term.GetSize(fd)

    // Request PTY on remote
    modes := ssh.TerminalModes{
        ssh.ECHO:          1,
        ssh.TTY_OP_ISPEED: 14400,
        ssh.TTY_OP_OSPEED: 14400,
    }
    if err := session.RequestPty("xterm-256color", h, w, modes); err != nil {
        return err
    }

    // Set local terminal to raw mode
    oldState, err := term.MakeRaw(fd)
    if err != nil {
        return err
    }
    defer term.Restore(fd, oldState)

    // Wire up stdin/stdout/stderr
    session.Stdin = os.Stdin
    session.Stdout = os.Stdout
    session.Stderr = os.Stderr

    // Handle window resize
    go c.watchWindowSize(ctx, session, fd)

    // Run command
    cmd := strings.Join(args, " ")
    return session.Run(cmd)
}
```

### 2. File Sync (`backend/ssh/sync.go`)

#### Recommended: mutagen

mutagen is the recommended sync method because:
- Purpose-built for dev file sync
- Bidirectional sync with conflict resolution
- Efficient delta transfer (only changed bytes)
- Handles large repos well
- Maintains filesystem permissions and timestamps
- Supports `.gitignore`-style exclusion patterns

```go
// Syncer handles file synchronization to the remote
type Syncer interface {
    // Sync synchronizes local paths to the remote, returning the remote paths
    Sync(ctx context.Context, localPaths []string) (remotePaths map[string]string, err error)
    // Close terminates any sync sessions
    Close() error
}

// MutagenSyncer uses mutagen for file sync
type MutagenSyncer struct {
    cfg        config.SSHBackendConfig
    sessionIDs []string // track created sessions for cleanup
}

func (s *MutagenSyncer) Sync(ctx context.Context, localPaths []string) (map[string]string, error) {
    remotePaths := make(map[string]string)

    for _, localPath := range localPaths {
        // Determine remote path
        remotePath := s.remotePathFor(localPath)

        // Create mutagen sync session
        // mutagen sync create <local> <remote-user>@<host>:<path>
        sessionID, err := s.createSession(ctx, localPath, remotePath)
        if err != nil {
            return nil, fmt.Errorf("sync %s: %w", localPath, err)
        }
        s.sessionIDs = append(s.sessionIDs, sessionID)

        // Wait for initial sync to complete
        if err := s.waitForSync(ctx, sessionID); err != nil {
            return nil, err
        }

        remotePaths[localPath] = remotePath
    }

    return remotePaths, nil
}
```

#### Fallback: rsync

For environments without mutagen, rsync provides a simpler one-shot sync:

```go
// RsyncSyncer uses rsync over SSH for file sync
type RsyncSyncer struct {
    cfg config.SSHBackendConfig
}

func (s *RsyncSyncer) Sync(ctx context.Context, localPaths []string) (map[string]string, error) {
    remotePaths := make(map[string]string)

    for _, localPath := range localPaths {
        remotePath := s.remotePathFor(localPath)

        // rsync -az --delete -e "ssh -i <key> -p <port>" <local>/ <user>@<host>:<remote>/
        args := []string{
            "rsync", "-az", "--delete",
            "-e", s.sshCommand(),
        }
        for _, ignore := range s.cfg.SyncIgnore {
            args = append(args, "--exclude", ignore)
        }
        args = append(args, localPath+"/", s.remoteTarget(remotePath)+"/")

        if err := exec.CommandContext(ctx, args[0], args[1:]...).Run(); err != nil {
            return nil, fmt.Errorf("rsync %s: %w", localPath, err)
        }

        remotePaths[localPath] = remotePath
    }

    return remotePaths, nil
}
```

#### Path Mapping

Local paths are mapped to remote paths under the sync root:

```
Local:  /Users/leigh/Code/myproject
Remote: ~/silo-sync/Users/leigh/Code/myproject

Local:  /Users/leigh/.ssh/known_hosts
Remote: ~/silo-sync/Users/leigh/.ssh/known_hosts
```

This preserves the full path structure so that paths inside the container remain identical to the local machine (the container mounts from the remote sync root with the same path).

### 3. SSH Connection Management (`backend/ssh/conn.go`)

```go
// Connect establishes an SSH connection with the configured authentication
func Connect(cfg config.SSHBackendConfig) (*ssh.Client, error) {
    // Resolve defaults
    host := cfg.Host
    port := cfg.Port
    if port == 0 {
        port = 22
    }
    user := cfg.User
    if user == "" {
        user = os.Getenv("USER")
    }

    // Build auth methods
    var authMethods []ssh.AuthMethod

    // 1. Try identity file if specified
    if cfg.IdentityFile != "" {
        keyPath := expandPath(cfg.IdentityFile)
        key, err := os.ReadFile(keyPath)
        if err != nil {
            return nil, fmt.Errorf("read identity file: %w", err)
        }
        signer, err := ssh.ParsePrivateKey(key)
        if err != nil {
            return nil, fmt.Errorf("parse identity file: %w", err)
        }
        authMethods = append(authMethods, ssh.PublicKeys(signer))
    }

    // 2. Try SSH agent
    if agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
        agentClient := agent.NewClient(agentConn)
        authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
    }

    // Build client config
    sshConfig := &ssh.ClientConfig{
        User:            user,
        Auth:            authMethods,
        HostKeyCallback: knownHostsCallback(), // uses ~/.ssh/known_hosts
        Timeout:         10 * time.Second,
    }

    addr := fmt.Sprintf("%s:%d", host, port)
    return ssh.Dial("tcp", addr, sshConfig)
}
```

#### Known Hosts Verification

The SSH backend uses the standard `~/.ssh/known_hosts` file for host key verification via the `knownhosts` package from `golang.org/x/crypto/ssh/knownhosts`:

```go
func knownHostsCallback() ssh.HostKeyCallback {
    knownHostsPath := filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts")
    callback, err := knownhosts.New(knownHostsPath)
    if err != nil {
        // Fall back to insecure if no known_hosts (with warning)
        return ssh.InsecureIgnoreHostKey()
    }
    return callback
}
```

### 4. Dockerfile-to-Shell Translation (`backend/ssh/provision.go`)

For environments where Docker isn't available on the remote (bare VMs), we provide a provisioning mode that translates Dockerfile directives to shell commands. This uses the official moby/buildkit parser.

**Note:** This is a secondary feature. The primary SSH backend assumes Docker is available on the remote.

#### Parser Integration

```go
import (
    "github.com/moby/buildkit/frontend/dockerfile/parser"
)

// DockerfileToShell converts a Dockerfile to a shell provisioning script
func DockerfileToShell(dockerfile string, target string) (string, error) {
    result, err := parser.Parse(strings.NewReader(dockerfile))
    if err != nil {
        return "", fmt.Errorf("parse dockerfile: %w", err)
    }

    var script strings.Builder
    script.WriteString("#!/bin/bash\nset -euo pipefail\n\n")

    // Find the target stage
    stage := findStage(result.AST, target)
    if stage == nil {
        return "", fmt.Errorf("stage not found: %s", target)
    }

    // Also process the base stage (FROM ... AS base)
    baseStage := findStage(result.AST, "base")

    // Convert directives
    for _, stage := range []*parser.Node{baseStage, stage} {
        if stage == nil {
            continue
        }
        for _, child := range stage.Children {
            if line := directiveToShell(child); line != "" {
                script.WriteString(line)
                script.WriteString("\n")
            }
        }
    }

    return script.String(), nil
}
```

#### Directive Mapping

| Dockerfile Directive | Shell Equivalent |
|---------------------|-----------------|
| `RUN cmd` | `cmd` |
| `ENV KEY=VALUE` | `export KEY=VALUE` |
| `WORKDIR /path` | `mkdir -p /path && cd /path` |
| `USER name` | Commands run as `su - name -c "..."` |
| `ARG NAME=default` | `NAME=${NAME:-default}` |
| `COPY src dst` | Skipped (files come from sync) |
| `ADD url dst` | `curl -fsSL url -o dst` |
| `EXPOSE port` | Comment only (informational) |
| `FROM` | Determines base image / stage |

#### Idempotency with Marker Files

```go
const markerDir = "/var/lib/silo"
const markerFile = markerDir + "/provision.hash"

// ProvisionScript wraps a provisioning script with idempotency checks
func ProvisionScript(script string) string {
    hash := sha256Hash(script)

    return fmt.Sprintf(`#!/bin/bash
set -euo pipefail

MARKER_DIR="%s"
MARKER_FILE="%s"
EXPECTED_HASH="%s"

mkdir -p "$MARKER_DIR"

# Check if already provisioned with this exact configuration
if [ -f "$MARKER_FILE" ] && [ "$(cat "$MARKER_FILE")" = "$EXPECTED_HASH" ]; then
    echo "silo: environment already provisioned (hash match)"
    exit 0
fi

echo "silo: provisioning environment..."

%s

# Record successful provisioning
echo "$EXPECTED_HASH" > "$MARKER_FILE"
echo "silo: provisioning complete"
`, markerDir, markerFile, hash, script)
}
```

The hash is computed from:
- The full provisioning script content
- The silo version
- The tool name and version

This ensures re-provisioning happens when:
- The Dockerfile changes (different script)
- Silo is updated (different version)
- The tool version changes

## Integration Points

### Changes to `run/run.go`

The `createBackend` function needs a new case:

```go
func createBackend(backendType string, cfg config.Config, stderr io.Writer, verbose bool) (backend.Backend, error) {
    // ... existing default detection ...

    switch backendType {
    case "docker":
        // ... existing ...
    case "container":
        // ... existing ...
    case "ssh":
        if cfg.Backends.SSH.Host == "" {
            return nil, fmt.Errorf("ssh backend requires backends.ssh.host to be configured")
        }
        if verbose {
            cli.LogTo(stderr, "Using SSH backend (remote: %s)...", cfg.Backends.SSH.Host)
        }
        client, err := sshbackend.NewClient(cfg.Backends.SSH)
        if err != nil {
            return nil, fmt.Errorf("failed to connect via SSH: %w", err)
        }
        return client, nil
    default:
        return nil, fmt.Errorf("unknown backend: %s (valid: docker, container, ssh)", backendType)
    }
}
```

**Note:** The `createBackend` function signature changes to accept the full `config.Config` instead of just `backendType string`, since the SSH backend needs its configuration.

### Changes to `main.go`

The `runRemove`, `runExec`, `runList`, and `completeContainerNames` functions need SSH backend cases in their switch statements. These follow the same pattern as the existing Docker/container cases.

### Changes to `config/config.go`

```go
type Config struct {
    Backend        string          `json:"backend,omitempty"`
    Backends       BackendsConfig  `json:"backends,omitempty"`  // NEW
    Tool           string          `json:"tool,omitempty"`
    // ... rest unchanged ...
}
```

The `Merge` function needs to merge the `Backends` field (SSH config: overlay replaces base for scalar fields, appends for arrays like `SyncIgnore`).

### Changes to `silo.schema.json`

Add `"ssh"` to the backend enum and add the `backends` property:

```json
{
  "backend": {
    "enum": ["docker", "container", "ssh"]
  },
  "backends": {
    "type": "object",
    "properties": {
      "ssh": {
        "type": "object",
        "properties": {
          "host": { "type": "string" },
          "port": { "type": "integer", "default": 22 },
          "user": { "type": "string" },
          "identity_file": { "type": "string" },
          "remote_backend": { "type": "string", "enum": ["docker"], "default": "docker" },
          "sync_method": { "type": "string", "enum": ["mutagen", "rsync"], "default": "mutagen" },
          "sync_ignore": { "type": "array", "items": { "type": "string" } },
          "remote_sync_root": { "type": "string", "default": "~/silo-sync" }
        },
        "required": ["host"]
      }
    }
  }
}
```

## New Files

```
backend/ssh/
  ssh.go          - Client struct, Backend interface implementation
  conn.go         - SSH connection establishment and management
  sync.go         - Syncer interface, MutagenSyncer, RsyncSyncer
  provision.go    - Dockerfile-to-shell translation (optional, for bare VMs)
  ssh_test.go     - Unit tests
  sync_test.go    - Sync tests
  provision_test.go - Provisioning tests
```

## Dependencies

New Go dependencies:
- `golang.org/x/crypto/ssh` - SSH client
- `golang.org/x/crypto/ssh/agent` - SSH agent forwarding
- `golang.org/x/crypto/ssh/knownhosts` - Host key verification
- `golang.org/x/term` - Terminal raw mode (may already be indirect dep)
- `github.com/moby/buildkit/frontend/dockerfile/parser` - Dockerfile parsing (for provisioning mode only)

External tools (on client machine):
- `mutagen` - File sync (recommended, checked at runtime)
- `rsync` - File sync (fallback)

External requirements (on remote machine):
- `docker` - Container runtime
- `ssh` server - Connection

## Error Handling

### Connection Errors
- SSH connection failure: Clear error message with host/port and troubleshooting hint
- Authentication failure: Suggest checking identity file or SSH agent
- Host key mismatch: Explain how to update known_hosts

### Sync Errors
- mutagen not installed: Suggest installation or fall back to rsync
- rsync not installed: Error with installation instructions
- Sync timeout: Configurable timeout with sensible default (5 minutes for initial sync)
- Disk space on remote: Check before sync, clear error if insufficient

### Remote Docker Errors
- Docker not installed on remote: Clear error message
- Docker daemon not running: Suggest starting it
- Build failures: Stream full output back to client

### Graceful Degradation
- If mutagen isn't available, fall back to rsync with a warning
- If the remote has no Docker, fall back to bare provisioning mode (Dockerfile-to-shell)
- If the remote connection drops during a run, attempt to reconnect and resume

## Implementation Plan

### Phase 1: Core SSH Backend
1. Add config types (`BackendsConfig`, `SSHBackendConfig`)
2. Update config loading, merging, schema, examples
3. Implement SSH connection management (`backend/ssh/conn.go`)
4. Implement remote command execution
5. Implement `Build`, `ImageExists`, `NextContainerName`, `List`, `Remove`
6. Wire into `createBackend` and command switch statements

### Phase 2: File Sync
1. Implement `Syncer` interface
2. Implement `MutagenSyncer`
3. Implement `RsyncSyncer` (fallback)
4. Path mapping logic
5. Integrate sync into `Run` method

### Phase 3: Interactive Run
1. PTY allocation and forwarding
2. Terminal raw mode handling
3. SIGWINCH propagation
4. Signal forwarding (SIGINT, SIGTERM)
5. Full `Run` implementation with sync + PTY

### Phase 4: Dockerfile Provisioning (Optional)
1. Integrate moby/buildkit parser
2. Implement directive-to-shell mapping
3. Idempotency with marker files
4. Provisioning mode for bare VMs

### Phase 5: Polish
1. Error messages and troubleshooting hints
2. Progress reporting during sync
3. Verbose logging parity with other backends
4. Tests
5. Documentation

## Open Questions

1. **SSH agent forwarding into container**: Should we forward the local SSH agent through SSH to the remote, then into the container? This would enable git operations inside the container. The current backends don't do this (they mount SSH keys directly).

2. **Persistent sync sessions**: Should mutagen sessions persist between silo invocations, or be created/destroyed each time? Persistent sessions would make subsequent runs faster but require cleanup.

3. **Multiple remote hosts**: Should the config support named SSH profiles for quickly switching between remotes? e.g., `"backend": "ssh:gpu-vm"` selecting from `"backends": { "ssh": { "profiles": { "gpu-vm": {...} } } }`

4. **Remote Docker socket**: Should we support connecting to a remote Docker daemon via SSH tunnel (`DOCKER_HOST=ssh://...`) instead of running Docker commands over SSH? This would let us reuse the existing Docker backend code.
