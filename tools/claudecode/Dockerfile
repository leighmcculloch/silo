# ============================================
# Claude Code stage
# ============================================
FROM base AS claude

ARG HOME
ARG TOOL_VERSION

RUN set -e && \
    curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://claude.ai/install.sh && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh ${TOOL_VERSION:+"$TOOL_VERSION"} && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/claude --version

ENV PATH="${HOME}/.claude/bin:${PATH}"

RUN sudo mkdir -p /etc/claude-code && \
    echo '{"sandbox":{"enabled":false}}' | sudo tee /etc/claude-code/managed-settings.json >/dev/null

# SILO_POST_BUILD_HOOKS_CLAUDE
