# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME
ARG CACHE_BUST

RUN set -e && \
    for i in 1 2 3; do \
        if curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://gh.io/copilot-install; then break; fi; \
        echo "Attempt $i failed, retrying in ${i}s..." && sleep "$i"; \
    done && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.local/bin/copilot --version

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT
