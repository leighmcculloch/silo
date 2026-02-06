package run

import (
	"testing"
)

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
