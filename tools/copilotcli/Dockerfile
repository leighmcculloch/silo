# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME
ARG TOOL_VERSION

RUN set -e && \
    curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://gh.io/copilot-install && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/copilot --version

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT
