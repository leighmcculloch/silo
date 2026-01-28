# ============================================
# Base stage: common setup for both tools
# ============================================
FROM ubuntu:24.04 AS base

ARG USER
ARG UID
ARG HOME

# Install system dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    build-essential \
    pkg-config \
    libssl-dev \
    curl \
    git \
    unzip \
    zstd \
    jq \
    ncurses-base \
    && rm -rf /var/lib/apt/lists/*

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
RUN mkdir -p ${HOME}/.local \
    && ARCH=$(dpkg --print-architecture) \
    && GO_VERSION=$(curl -fsSL https://go.dev/VERSION?m=text | head -1 | sed 's/^go//') \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -C ${HOME}/.local -xz \
    && go install golang.org/x/tools/gopls@latest

# Install Node.js and npm
ENV PATH="${HOME}/.local/node/bin:${PATH}"
RUN ARCH=$(dpkg --print-architecture) \
    && NODE_VERSION=$(curl -fsSL https://api.github.com/repos/nodejs/node/releases/latest | jq -r '.tag_name | ltrimstr("v")') \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz" | tar -C ${HOME}/.local -xJ \
    && mv ${HOME}/.local/node-v${NODE_VERSION}-linux-${ARCH} ${HOME}/.local/node

# Install Deno
ENV PATH="${HOME}/.deno/bin:${PATH}"
RUN ARCH=$(uname -m) \
    && DENO_VERSION=$(curl -fsSL https://api.github.com/repos/denoland/deno/releases/latest | jq -r '.tag_name | ltrimstr("v")') \
    && curl -fsSL "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-${ARCH}-unknown-linux-gnu.zip" -o /tmp/deno.zip \
    && unzip -o /tmp/deno.zip -d ${HOME}/.local/bin \
    && rm /tmp/deno.zip

# Install Rust (stable + nightly) with wasm32v1-none target and rust-analyzer
ENV PATH="${HOME}/.cargo/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y \
    && . ${HOME}/.cargo/env \
    && rustup toolchain install stable \
    && rustup target add wasm32v1-none --toolchain stable \
    && rustup component add rust-analyzer

# Install GitHub CLI
RUN ARCH=$(dpkg --print-architecture) \
    && GH_VERSION=$(curl -fsSL https://api.github.com/repos/cli/cli/releases/latest | jq -r '.tag_name | ltrimstr("v")') \
    && curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${ARCH}.tar.gz" | tar -C ${HOME}/.local -xz --strip-components=1

# Install MCP servers
RUN go install github.com/github/github-mcp-server/cmd/github-mcp-server@latest

# SILO_POST_BUILD_HOOKS

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

# SILO_POST_BUILD_HOOKS_OPENCODE

ENTRYPOINT ["opencode"]
 
# ============================================
# Claude Code stage
# ============================================
FROM base AS claude

ARG HOME

RUN curl -fsSL https://claude.ai/install.sh | bash

ENV PATH="${HOME}/.claude/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_CLAUDE

ENTRYPOINT ["/bin/sh", "-c", "exec claude --mcp-config=$HOME/.claude/mcp.json --dangerously-skip-permissions"]

# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME

RUN curl -fsSL https://gh.io/copilot-install | bash

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT

ENTRYPOINT ["/bin/sh", "-c", "exec copilot --allow-all"]
