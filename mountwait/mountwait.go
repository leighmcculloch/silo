package mountwait

import (
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
)

// GenerateScript generates a bash script that waits for all mount paths to exist.
// It polls each path at 1ms intervals for up to 10s total timeout, with logging.
// This should be prepended to pre-run hooks to ensure mounts are ready before other commands run.
func GenerateScript(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var quotedPaths []string
	for _, p := range paths {
		quotedPaths = append(quotedPaths, shellquote.Join(p))
	}
	// ANSI color codes matching cli/cli.go styles:
	// - Info (==>) color 86, Success (✓) color 82, Error (✗) color 196
	return fmt.Sprintf(`__silo_tilde() { case "$1" in "$HOME"*) echo "~${1#$HOME}";; *) echo "$1";; esac; }
__silo_wait_for_mount() {
  local p=$1 timeout=10000 i=0
  local c_success=$'\033[38;5;82m' c_error=$'\033[38;5;196m' c_reset=$'\033[0m'
  local display=$(__silo_tilde "$p")
  if [ -e "$p" ]; then
    printf "  ${c_success}✓${c_reset} %%s\n" "$display" >&2
    return 0
  fi
  while [ ! -e "$p" ] && [ $i -lt $timeout ]; do
    sleep 0.001
    i=$((i+1))
  done
  if [ -e "$p" ]; then
    printf "  ${c_success}✓${c_reset} %%s (${i}ms)\n" "$display" >&2
    return 0
  fi
  printf "  ${c_error}✗${c_reset} %%s (timed out)\n" "$display" >&2
  return 1
}
__silo_wait_for_mounts() {
  local paths=(%s)
  local pids=() p
  local c_info=$'\033[38;5;86m' c_success=$'\033[38;5;82m' c_reset=$'\033[0m'
  printf "${c_info}==> Waiting for ${#paths[@]} mount(s)...${c_reset}\n" >&2
  for p in "${paths[@]}"; do
    __silo_wait_for_mount "$p" &
    pids+=($!)
  done
  local failed=0
  for pid in "${pids[@]}"; do
    wait $pid || failed=1
  done
  if [ $failed -eq 1 ]; then
    exit 1
  fi
  printf "  ${c_success}✓ All mounts ready${c_reset}\n" >&2
}; __silo_wait_for_mounts`, strings.Join(quotedPaths, " "))
}
