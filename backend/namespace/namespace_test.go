package namespace

import "testing"

func TestToHTTPSCloneURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://github.com/foo/bar.git", "https://github.com/foo/bar.git"},
		{"https://github.com/foo/bar", "https://github.com/foo/bar"},
		{"http://example.com/foo/bar", "http://example.com/foo/bar"},
		{"git@github.com:foo/bar.git", "https://github.com/foo/bar.git"},
		{"git@github.com:foo/bar", "https://github.com/foo/bar"},
		{"ssh://git@github.com/foo/bar.git", "https://github.com/foo/bar.git"},
		{"ssh://git@gitlab.example.com:2222/foo/bar.git", "https://gitlab.example.com:2222/foo/bar.git"},
		{"  https://github.com/foo/bar.git  ", "https://github.com/foo/bar.git"},
		{"", ""},
		{"file:///tmp/repo", ""},
		{"git@", ""},
	}
	for _, tt := range tests {
		got := toHTTPSCloneURL(tt.in)
		if got != tt.want {
			t.Errorf("toHTTPSCloneURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGitSeedCommand(t *testing.T) {
	withBranch := gitSeedCommand(gitSeed{
		remotePath: "/home/me/repo",
		cloneURL:   "https://github.com/foo/bar.git",
		branch:     "feature/x",
	})
	// Should try branch first, then fall back to default.
	if !contains(withBranch, "--branch feature/x") {
		t.Errorf("expected branched clone, got: %s", withBranch)
	}
	if !contains(withBranch, "|| true") {
		t.Errorf("expected best-effort fallback, got: %s", withBranch)
	}
	if !contains(withBranch, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("expected non-interactive guard, got: %s", withBranch)
	}

	noBranch := gitSeedCommand(gitSeed{
		remotePath: "/home/me/repo",
		cloneURL:   "https://github.com/foo/bar.git",
	})
	if contains(noBranch, "--branch") {
		t.Errorf("expected no --branch, got: %s", noBranch)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
