# ============================================
# Claude Code stage
# ============================================
FROM base AS claude

ARG HOME
ARG CACHE_BUST

RUN curl -fsSL https://claude.ai/install.sh | bash

ENV PATH="${HOME}/.claude/bin:${PATH}"

# SILO_POST_BUILD_HOOKS_CLAUDE
