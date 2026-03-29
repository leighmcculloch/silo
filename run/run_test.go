package run

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leighmcculloch/silo/config"
)

func TestSanitizeContainerName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-project", "my-project"},
		{"My Project", "my-project"},
		{"hello.world", "hello-world"},
		{"foo  bar", "foo-bar"},
		{"  leading", "leading"},
		{"trailing  ", "trailing"},
		{"a/b/c", "a-b-c"},
		{"café", "caf"},
		{"", "silo"},
		{"...", "silo"},
		{"my_project", "my-project"},
		{"123", "123"},
		{"MyProject", "myproject"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeContainerName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeContainerName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRepoURLMatches(t *testing.T) {
	tests := []struct {
		url     string
		pattern string
		want    bool
	}{
		// Basic matching
		{"git@github.com:stellar/stellar-core.git", "github.com/stellar", true},
		{"https://github.com/stellar/stellar-core.git", "github.com/stellar", true},
		{"git@github.com:stellar/stellar-core.git", "github.com/stellar/stellar-core", true},
		// Non-matching
		{"git@github.com:other/repo.git", "github.com/stellar", false},
		// More specific patterns
		{"git@github.com:stellar/stellar-core.git", "stellar-core", true},
		{"git@github.com:stellar/js-sdk.git", "stellar-core", false},
	}

	for _, tt := range tests {
		t.Run(tt.url+"_"+tt.pattern, func(t *testing.T) {
			got := repoURLMatches(tt.url, tt.pattern)
			if got != tt.want {
				t.Errorf("repoURLMatches(%q, %q) = %v, want %v", tt.url, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestCollectMountsIncludesAdditionalCLIMounts(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}
	defer os.Setenv("HOME", oldHome)

	cfg := config.Config{
		MountsRO: []string{"~/global-ro"},
		MountsRW: []string{"~/global-rw"},
		Tools: map[string]config.ToolConfig{
			"claude": {
				MountsRO: []string{"~/tool-ro"},
				MountsRW: []string{"~/tool-rw"},
			},
		},
	}
	repoMatches := []RepoMatch{{
		Name: "github.com/example/repo",
		Config: config.RepoConfig{
			MountsRO: []string{"~/repo-ro"},
			MountsRW: []string{"~/repo-rw"},
		},
	}}
	worktreeRoots := []string{filepath.Join(home, "worktree-root")}

	mountsRO, mountsRW := CollectMounts(
		"claude",
		cfg,
		"/workspace/project",
		repoMatches,
		worktreeRoots,
		[]string{"../extra-ro"},
		[]string{"./extra-rw"},
	)

	wantRO := []string{
		filepath.Join(home, "tool-ro"),
		filepath.Join(home, "repo-ro"),
		filepath.Join(home, "global-ro"),
		"/workspace/extra-ro",
	}
	wantRW := []string{
		"/workspace/project",
		filepath.Join(home, "tool-rw"),
		filepath.Join(home, "repo-rw"),
		filepath.Join(home, "global-rw"),
		filepath.Join(home, "worktree-root"),
		"/workspace/project/extra-rw",
	}

	if !reflect.DeepEqual(mountsRO, wantRO) {
		t.Fatalf("CollectMounts() mountsRO = %v, want %v", mountsRO, wantRO)
	}
	if !reflect.DeepEqual(mountsRW, wantRW) {
		t.Fatalf("CollectMounts() mountsRW = %v, want %v", mountsRW, wantRW)
	}
}

func TestCollectMountsPromotesOverlappingROtoRW(t *testing.T) {
	emptyCfg := config.Config{}

	tests := []struct {
		name    string
		roExtra []string
		rwExtra []string
		wantRO  []string
		wantRW  []string
	}{
		{
			name:    "exact duplicate removed from RO",
			roExtra: []string{"/data"},
			rwExtra: []string{"/data"},
			wantRO:  nil,
			wantRW:  []string{"/cwd", "/data"},
		},
		{
			name:    "RO subpath of RW parent dropped",
			roExtra: []string{"/data/sub"},
			rwExtra: []string{"/data"},
			wantRO:  nil,
			wantRW:  []string{"/cwd", "/data"},
		},
		{
			name:    "RO parent of RW child promoted to RW",
			roExtra: []string{"/data"},
			rwExtra: []string{"/data/sub"},
			wantRO:  nil,
			wantRW:  []string{"/cwd", "/data/sub", "/data"},
		},
		{
			name:    "no overlap keeps both lists",
			roExtra: []string{"/readonly"},
			rwExtra: []string{"/writable"},
			wantRO:  []string{"/readonly"},
			wantRW:  []string{"/cwd", "/writable"},
		},
		{
			name:    "similar prefix not treated as subpath",
			roExtra: []string{"/datastore"},
			rwExtra: []string{"/data"},
			wantRO:  []string{"/datastore"},
			wantRW:  []string{"/cwd", "/data"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRO, gotRW := CollectMounts("claude", emptyCfg, "/cwd", nil, nil, tt.roExtra, tt.rwExtra)
			if len(gotRO) == 0 {
				gotRO = nil
			}
			if !reflect.DeepEqual(gotRO, tt.wantRO) {
				t.Errorf("mountsRO = %v, want %v", gotRO, tt.wantRO)
			}
			if !reflect.DeepEqual(gotRW, tt.wantRW) {
				t.Errorf("mountsRW = %v, want %v", gotRW, tt.wantRW)
			}
		})
	}
}
