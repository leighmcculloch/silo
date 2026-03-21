# ============================================
# Codex CLI stage
# ============================================
FROM base AS codex

ARG HOME
ARG TOOL_VERSION

RUN sudo apt-get update && sudo apt-get install -y bubblewrap && sudo rm -rf /var/lib/apt/lists/*

RUN set -e && \
    npm install -g @openai/codex${TOOL_VERSION:+"@$TOOL_VERSION"} && \
    codex --version

# SILO_POST_BUILD_HOOKS_CODEX
