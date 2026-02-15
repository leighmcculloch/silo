package run

import (
	"testing"
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
