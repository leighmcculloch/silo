# ============================================
# Cline CLI stage
# ============================================
FROM base AS cline

ARG HOME
ARG USER
ARG TOOL_VERSION

RUN sudo apt-get update \
    && sudo apt-get install -y bubblewrap socat \
    && sudo apt-get clean

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
    npm install -g cline${TOOL_VERSION:+"@$TOOL_VERSION"} && \
    cline version

ENV PATH="${HOME}/.claude/bin:${PATH}"
ENV CLINE_NO_AUTO_UPDATE=1

USER root
RUN mkdir -p /etc/claude-code && \
    printf '{"sandbox":{"enabled":false}}\n' > /etc/claude-code/managed-settings.json
USER ${USER}

# SILO_POST_BUILD_HOOKS_CLINE
