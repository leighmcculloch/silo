# ============================================
# OpenCode stage
# ============================================
FROM base AS opencode

ARG HOME

RUN curl -fsSL https://raw.githubusercontent.com/anomalyco/opencode/refs/heads/dev/install | bash

ENV PATH="${HOME}/.opencode/bin:${PATH}"
ENV OPENCODE_PERMISSION='{"edit":"allow","bash":"allow","webfetch":"allow","websearch":"allow","external_directory":"allow"}'
ENV OPENCODE_EXPERIMENTAL=true

# SILO_POST_BUILD_HOOKS_OPENCODE
