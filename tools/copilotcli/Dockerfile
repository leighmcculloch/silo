# ============================================
# Copilot CLI stage
# ============================================
FROM base AS copilot

ARG HOME

RUN curl -fsSL https://gh.io/copilot-install | bash

ENV PATH="${HOME}/.local/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_COPILOT
