package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetGitWorktreeRoots(t *testing.T) {
	// Test with a non-git directory
	tmpDir := t.TempDir()
	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(roots) != 0 {
		t.Errorf("expected no roots for non-git directory, got: %v", roots)
	}
}

func TestGetGitWorktreeRootsWithSubdirs(t *testing.T) {
	// Test with subdirectories that are not git repos
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(roots) != 0 {
		t.Errorf("expected no roots, got: %v", roots)
	}
}

func TestGetGitIdentity(t *testing.T) {
	// This test depends on git being configured
	// We just verify it doesn't panic
	name, email := GetGitIdentity()
	// Don't assert on values since it depends on user's git config
	_ = name
	_ = email
}
