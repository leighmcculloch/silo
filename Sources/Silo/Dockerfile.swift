import Foundation

/// Returns the embedded Dockerfile content
public func dockerfile() -> String {
    // Try to load from bundle resource first
    if let url = Bundle.module.url(forResource: "Dockerfile", withExtension: nil),
       let content = try? String(contentsOf: url, encoding: .utf8) {
        return content
    }

    // Fallback: embedded Dockerfile content
    return """
    # syntax=docker/dockerfile:1
    # ============================================
    # Base stage: common setup for both tools
    # ============================================
    FROM ubuntu:24.04 AS base

    ARG USER
    ARG UID
    ARG HOME

    # Install system dependencies
    RUN --mount=type=cache,target=/var/cache/apt \\
        --mount=type=cache,target=/var/lib/apt \\
        apt-get update && apt-get install -y \\
        ca-certificates \\
        build-essential \\
        pkg-config \\
        libssl-dev \\
        curl \\
        git \\
        unzip \\
        zstd \\
        jq \\
        ncurses-base

    # Create user with matching UID and macOS-style home path
    RUN useradd -m -u ${UID} -d ${HOME} -s /bin/bash ${USER}

    # Set up environment
    ENV PATH="${HOME}/.local/bin:${PATH}"
    USER ${USER}
    WORKDIR ${HOME}

    # Install Go
    ENV GOPATH="${HOME}/go"
    ENV GOROOT="${HOME}/.local/go"
    ENV PATH="${HOME}/.local/go/bin:${HOME}/go/bin:${PATH}"
    RUN --mount=type=cache,target=/tmp/downloads,uid=${UID} \\
        mkdir -p ${HOME}/.local \\
        && ARCH=$(dpkg --print-architecture) \\
        && GO_VERSION=$(curl -fsSL https://go.dev/VERSION?m=text | head -1 | sed 's/^go//') \\
        && GO_TAR=/tmp/downloads/go${GO_VERSION}.linux-${ARCH}.tar.gz \\
        && [ -f "$GO_TAR" ] || curl -fsSL -o "$GO_TAR" https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz \\
        && tar -C ${HOME}/.local -xzf "$GO_TAR" \\
        && go install golang.org/x/tools/gopls@latest

    # Install Deno
    ENV PATH="${HOME}/.deno/bin:${PATH}"
    RUN --mount=type=cache,target=/tmp/downloads,uid=${UID} \\
        ARCH=$(uname -m) \\
        && DENO_VERSION=$(curl -fsSL https://api.github.com/repos/denoland/deno/releases/latest | jq -r '.tag_name | ltrimstr("v")') \\
        && DENO_ZIP=/tmp/downloads/deno-${DENO_VERSION}-${ARCH}-unknown-linux-gnu.zip \\
        && [ -f "$DENO_ZIP" ] || curl -fsSL -o "$DENO_ZIP" https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-${ARCH}-unknown-linux-gnu.zip \\
        && unzip -o "$DENO_ZIP" -d ${HOME}/.local/bin

    # Install Rust (stable + nightly) with wasm32v1-none target and rust-analyzer
    ENV PATH="${HOME}/.cargo/bin:${PATH}"
    RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y \\
        && . ${HOME}/.cargo/env \\
        && rustup toolchain install stable \\
        && rustup toolchain install nightly \\
        && rustup target add wasm32v1-none --toolchain stable \\
        && rustup target add wasm32v1-none --toolchain nightly \\
        && rustup component add rust-analyzer

    # Install GitHub CLI
    RUN --mount=type=cache,target=/tmp/downloads,uid=${UID} \\
        ARCH=$(dpkg --print-architecture) \\
        && GH_VERSION=$(curl -fsSL https://api.github.com/repos/cli/cli/releases/latest | jq -r '.tag_name | ltrimstr("v")') \\
        && GH_TAR=/tmp/downloads/gh_${GH_VERSION}_linux_${ARCH}.tar.gz \\
        && [ -f "$GH_TAR" ] || curl -fsSL -o "$GH_TAR" https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${ARCH}.tar.gz \\
        && tar -C ${HOME}/.local -xzf "$GH_TAR" --strip-components=1

    # Install MCP servers
    RUN go install github.com/github/github-mcp-server/cmd/github-mcp-server@latest
    RUN deno install --global --deny-read --allow-env --allow-net npm:server-perplexity-ask
    RUN deno install --global --allow-env --allow-net --allow-read=$HOME/.cache/deno/npm/registry.npmjs.org/@upstash/context7-mcp npm:@upstash/context7-mcp

    ENV TERM="xterm-256color"

    # ============================================
    # OpenCode stage
    # ============================================
    FROM base AS opencode

    ARG HOME

    RUN curl -fsSL https://opencode.ai/install | bash

    ENV PATH="${HOME}/.opencode/bin:${PATH}"
    ENV OPENCODE_PERMISSION='{"edit":"allow","bash":"allow","webfetch":"allow", "websearch":"allow","external_directory":"allow"}'
    ENV OPENCODE_EXPERIMENTAL=true

    ENTRYPOINT ["opencode"]

    # ============================================
    # Claude Code stage
    # ============================================
    FROM base AS claude

    ARG HOME

    RUN curl -fsSL https://claude.ai/install.sh | bash

    ENV PATH="${HOME}/.claude/bin:${PATH}"

    ENTRYPOINT ["/bin/sh", "-c", "exec claude --mcp-config=$HOME/.claude/mcp.json --dangerously-skip-permissions"]

    # ============================================
    # Copilot CLI stage
    # ============================================
    FROM base AS copilot

    ARG HOME

    RUN curl -fsSL https://gh.io/copilot-install | bash

    ENV PATH="${HOME}/.local/bin:${PATH}"

    ENTRYPOINT ["/bin/sh", "-c", "exec copilot --allow-all"]
    """
}

/// Available tool names
public let availableTools = ["opencode", "claude", "copilot"]

/// Returns a description for a tool
public func toolDescription(_ tool: String) -> String {
    switch tool {
    case "opencode":
        return "OpenCode - AI coding assistant"
    case "claude":
        return "Claude Code - Anthropic's CLI for Claude"
    case "copilot":
        return "GitHub Copilot CLI"
    default:
        return "Unknown tool: \(tool)"
    }
}
