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
    zsh \
    tmux \
    && rm -rf /var/lib/apt/lists/*

# Install Docker CE (for container backend which runs in a VM)
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
    $(. /etc/os-release && echo "${VERSION_CODENAME}") stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y docker-ce docker-ce-cli docker-buildx-plugin docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

# Create user with matching UID and macOS-style home path, add to docker group
RUN useradd -m -u ${UID} -d ${HOME} -s /bin/bash -G docker ${USER}

# Allow user passwordless sudo for specific commands
RUN apt-get update && apt-get install -y sudo && rm -rf /var/lib/apt/lists/* \
    && echo "${USER} ALL=(ALL) NOPASSWD: /usr/bin/dockerd" > /etc/sudoers.d/${USER} \
    && echo "${USER} ALL=(ALL) NOPASSWD: /usr/bin/apt-get, /usr/bin/apt" >> /etc/sudoers.d/${USER} \
    && chmod 0440 /etc/sudoers.d/${USER}

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

# Configure tmux for use inside silo (e.g. Claude Code agent teams).
# Uses Ctrl+Space as prefix to avoid conflicts with host tmux (typically Ctrl+A or Ctrl+B).
RUN cat > ${HOME}/.tmux.conf << 'TMUXEOF'
# Prefix: Ctrl+Space (no conflict with host tmux Ctrl+A or Ctrl+B)
unbind C-b
set -g prefix C-Space
bind C-Space send-prefix

# Terminal
set -g default-terminal "xterm-256color"
set -ga terminal-overrides ",xterm-256color:Tc"

# General
set -g mouse on
set -g history-limit 50000
set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on
set -s escape-time 0
set -g focus-events on

# Vi mode
setw -g mode-keys vi
bind -T copy-mode-vi v send -X begin-selection
bind -T copy-mode-vi y send -X copy-selection-and-cancel

# Pane navigation (vim keys)
bind h select-pane -L
bind j select-pane -D
bind k select-pane -U
bind l select-pane -R

# Pane splitting
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"

# Pane resizing
bind -r H resize-pane -L 5
bind -r J resize-pane -D 5
bind -r K resize-pane -U 5
bind -r L resize-pane -R 5

# Status bar - minimal dark theme
set -g status-position bottom
set -g status-style "bg=#1a1b26,fg=#a9b1d6"
set -g status-left-length 30
set -g status-right-length 50
set -g status-left "#[fg=#1a1b26,bg=#7aa2f7,bold] #S #[fg=#7aa2f7,bg=#1a1b26] "
set -g status-right "#[fg=#565f89] %H:%M "
set -g status-justify centre

# Window status
setw -g window-status-format "#[fg=#565f89] #I:#W "
setw -g window-status-current-format "#[fg=#7aa2f7,bold] #I:#W "
setw -g window-status-separator ""

# Pane borders
set -g pane-border-style "fg=#3b4261"
set -g pane-active-border-style "fg=#7aa2f7"

# Messages
set -g message-style "bg=#1a1b26,fg=#7aa2f7"
set -g message-command-style "bg=#1a1b26,fg=#7aa2f7"
TMUXEOF

# SILO_POST_BUILD_HOOKS

ENV TERM="xterm-256color"

# ============================================
# OpenCode stage
# ============================================
FROM base AS opencode

ARG HOME

RUN curl -fsSL https://raw.githubusercontent.com/anomalyco/opencode/refs/heads/dev/install | bash

ENV PATH="${HOME}/.opencode/bin:${PATH}"
ENV OPENCODE_PERMISSION='{"edit":"allow","bash":"allow","webfetch":"allow","websearch":"allow","external_directory":"allow"}'
ENV OPENCODE_EXPERIMENTAL=true

# SILO_POST_BUILD_HOOKS_OPENCODE
 
# ============================================
# Claude Code stage
# ============================================
FROM base AS claude

ARG HOME

RUN curl -fsSL https://claude.ai/install.sh | bash

ENV PATH="${HOME}/.claude/bin:${PATH}"
ENV CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1

# SILO_POST_BUILD_HOOKS_CLAUDE

# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME

RUN curl -fsSL https://gh.io/copilot-install | bash

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT
