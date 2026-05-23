# ============================================
# Pi Coding Agent stage
# ============================================
FROM base AS pi

ARG HOME
ARG TOOL_VERSION

RUN set -e && \
	npm install -g @earendil-works/pi-coding-agent${TOOL_VERSION:+"@$TOOL_VERSION"} && \
	pi --version

# SILO_POST_BUILD_HOOKS_PI
