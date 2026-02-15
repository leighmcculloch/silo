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

func TestGetGitWorktreeRootsWithWorktree(t *testing.T) {
	// Simulate a worktree layout:
	// tmpDir/
	//   main-repo/
	//     .git/
	//       worktrees/
	//         feature-branch/   (worktree metadata)
	//   feature-branch/
	//     .git  (file containing "gitdir: ../main-repo/.git/worktrees/feature-branch")
	tmpDir := t.TempDir()

	mainRepo := filepath.Join(tmpDir, "main-repo")
	worktreeMetaDir := filepath.Join(mainRepo, ".git", "worktrees", "feature-branch")
	if err := os.MkdirAll(worktreeMetaDir, 0755); err != nil {
		t.Fatalf("failed to create worktree metadata dir: %v", err)
	}

	featureBranch := filepath.Join(tmpDir, "feature-branch")
	if err := os.Mkdir(featureBranch, 0755); err != nil {
		t.Fatalf("failed to create feature-branch dir: %v", err)
	}

	// Write .git file with relative gitdir pointer
	gitFileContent := "gitdir: ../main-repo/.git/worktrees/feature-branch\n"
	if err := os.WriteFile(filepath.Join(featureBranch, ".git"), []byte(gitFileContent), 0644); err != nil {
		t.Fatalf("failed to write .git file: %v", err)
	}

	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d: %v", len(roots), roots)
	}

	expected, _ := filepath.Abs(mainRepo)
	if roots[0] != expected {
		t.Errorf("expected root %q, got %q", expected, roots[0])
	}
}

func TestGetGitWorktreeRootsSkipsStandaloneRepos(t *testing.T) {
	// Standalone repos have a .git directory, not a file — should be skipped
	tmpDir := t.TempDir()

	standaloneRepo := filepath.Join(tmpDir, "standalone")
	if err := os.MkdirAll(filepath.Join(standaloneRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}

	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 0 {
		t.Errorf("expected no roots for standalone repos, got: %v", roots)
	}
}

func TestGetGitWorktreeRootsAbsoluteGitdir(t *testing.T) {
	// Test with absolute path in .git file
	tmpDir := t.TempDir()

	mainRepo := filepath.Join(tmpDir, "main-repo")
	worktreeMetaDir := filepath.Join(mainRepo, ".git", "worktrees", "feature")
	if err := os.MkdirAll(worktreeMetaDir, 0755); err != nil {
		t.Fatalf("failed to create worktree metadata dir: %v", err)
	}

	worktree := filepath.Join(tmpDir, "feature")
	if err := os.Mkdir(worktree, 0755); err != nil {
		t.Fatalf("failed to create worktree dir: %v", err)
	}

	// Write .git file with absolute gitdir pointer
	gitFileContent := "gitdir: " + worktreeMetaDir + "\n"
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte(gitFileContent), 0644); err != nil {
		t.Fatalf("failed to write .git file: %v", err)
	}

	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d: %v", len(roots), roots)
	}

	if roots[0] != mainRepo {
		t.Errorf("expected root %q, got %q", mainRepo, roots[0])
	}
}

func TestGetGitWorktreeRootsDeduplicates(t *testing.T) {
	// Two worktrees from the same main repo should produce one root
	tmpDir := t.TempDir()

	mainRepo := filepath.Join(tmpDir, "main-repo")
	for _, name := range []string{"wt1", "wt2"} {
		metaDir := filepath.Join(mainRepo, ".git", "worktrees", name)
		if err := os.MkdirAll(metaDir, 0755); err != nil {
			t.Fatalf("failed to create worktree metadata dir: %v", err)
		}

		wtDir := filepath.Join(tmpDir, name)
		if err := os.Mkdir(wtDir, 0755); err != nil {
			t.Fatalf("failed to create worktree dir: %v", err)
		}

		gitFileContent := "gitdir: " + metaDir + "\n"
		if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte(gitFileContent), 0644); err != nil {
			t.Fatalf("failed to write .git file: %v", err)
		}
	}

	roots, err := GetGitWorktreeRoots(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 deduplicated root, got %d: %v", len(roots), roots)
	}
	if roots[0] != mainRepo {
		t.Errorf("expected root %q, got %q", mainRepo, roots[0])
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
