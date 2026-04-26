# ============================================
# Kilo Code CLI stage
# ============================================
FROM base AS kilo

ARG HOME
ARG TOOL_VERSION

RUN set -e && \
    npm install -g @kilocode/cli${TOOL_VERSION:+"@$TOOL_VERSION"} && \
    kilo --version

# SILO_POST_BUILD_HOOKS_KILO
