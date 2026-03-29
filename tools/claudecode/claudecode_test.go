package claudecode

import (
	"strings"
	"testing"
)

func TestCommandMakesMCPConfigOptional(t *testing.T) {
	cmd := Tool.Command("/home/tester")
	if len(cmd) != 3 {
		t.Fatalf("expected bash -lc wrapper, got %v", cmd)
	}
	if cmd[0] != "bash" || cmd[1] != "-lc" {
		t.Fatalf("expected bash -lc wrapper, got %v", cmd)
	}
	if !strings.Contains(cmd[2], `[ -f '/home/tester/.claude/mcp.json' ]`) {
		t.Fatalf("expected conditional mcp config check, got %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "--dangerously-skip-permissions") {
		t.Fatalf("expected claude permissions flag, got %q", cmd[2])
	}
}
