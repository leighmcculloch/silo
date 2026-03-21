# ============================================
# Codex CLI stage
# ============================================
FROM base AS codex

ARG HOME
ARG TOOL_VERSION

RUN set -e && \
    npm install -g @openai/codex${TOOL_VERSION:+"@$TOOL_VERSION"} && \
    codex --version

# SILO_POST_BUILD_HOOKS_CODEX
