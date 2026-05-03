package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandSkipsMissingMCPConfig(t *testing.T) {
	tmp := t.TempDir()
	got := Tool.Command(tmp)

	for _, arg := range got {
		if arg == "--mcp-config="+filepath.Join(tmp, ".claude/mcp.json") {
			t.Fatalf("Command included mcp config flag when file is missing: %v", got)
		}
	}
}

func TestCommandIncludesExistingMCPConfig(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755); err != nil {
		t.Fatalf("failed to create claude config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".claude", "mcp.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("failed to write mcp config: %v", err)
	}

	got := Tool.Command(tmp)
	want := "--mcp-config=" + filepath.Join(tmp, ".claude/mcp.json")
	found := false
	for _, arg := range got {
		if arg == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("Command missing mcp config flag %q: %v", want, got)
	}
}
