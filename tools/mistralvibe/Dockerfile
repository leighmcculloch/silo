# ============================================
# Mistral Vibe stage
# ============================================
FROM base AS vibe

ARG HOME

RUN set -e && \
    curl -LsSf --connect-timeout 15 -o /tmp/install.sh https://mistral.ai/vibe/install.sh && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/vibe --version

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_VIBE
