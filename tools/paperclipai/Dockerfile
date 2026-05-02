# ============================================
# Paperclip AI stage
# ============================================
FROM base AS paperclipai

ARG HOME
ARG USER
ARG TOOL_VERSION

RUN set -e && \
    curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://claude.ai/install.sh && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/claude --version

RUN set -e && \
    npm install -g @openai/codex && \
    codex --version

RUN set -e && \
    npm install -g paperclipai${TOOL_VERSION:+"@$TOOL_VERSION"} && \
    paperclipai run --help >/dev/null

ENV PATH="${HOME}/.claude/bin:${PATH}"

USER root
RUN mkdir -p /etc/claude-code && \
    printf '{"sandbox":{"enabled":false}}\n' > /etc/claude-code/managed-settings.json
USER ${USER}

# SILO_POST_BUILD_HOOKS_PAPERCLIPAI
