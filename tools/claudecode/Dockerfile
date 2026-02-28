# ============================================
# Claude Code stage
# ============================================
FROM base AS claude

ARG HOME
ARG CACHE_BUST

RUN set -e && \
    curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://claude.ai/install.sh && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/claude --version

ENV PATH="${HOME}/.claude/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_CLAUDE
