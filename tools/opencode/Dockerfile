# ============================================
# OpenCode stage
# ============================================
FROM base AS opencode

ARG HOME

RUN set -e && \
    curl -fsSL --connect-timeout 15 -o /tmp/install.sh https://raw.githubusercontent.com/anomalyco/opencode/refs/heads/dev/install && \
    test -s /tmp/install.sh && \
    bash /tmp/install.sh && \
    rm /tmp/install.sh && \
    $HOME/.opencode/bin/opencode --version

ENV PATH="${HOME}/.opencode/bin:${PATH}"
ENV OPENCODE_PERMISSION='{"edit":"allow","bash":"allow","webfetch":"allow","websearch":"allow","external_directory":"allow"}'
ENV OPENCODE_EXPERIMENTAL=true

# SILO_POST_BUILD_HOOKS_OPENCODE
