# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME
ARG CACHE_BUST

RUN curl -fsSL https://gh.io/copilot-install | bash

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT
