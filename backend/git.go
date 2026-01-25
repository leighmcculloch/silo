package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GetGitWorktreeRoots returns git worktree common directories for the given directory
func GetGitWorktreeRoots(dir string) ([]string, error) {
	var roots []string
	seen := make(map[string]bool)

	// Check current dir and immediate subdirs
	dirs := []string{dir}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(dir, e.Name()))
			}
		}
	}

	for _, d := range dirs {
		// Check if it's a git worktree
		cmd := exec.Command("git", "-C", d, "rev-parse", "--is-inside-work-tree")
		if err := cmd.Run(); err != nil {
			continue
		}

		// Get git dir
		gitDirCmd := exec.Command("git", "-C", d, "rev-parse", "--git-dir")
		gitDirOut, err := gitDirCmd.Output()
		if err != nil {
			continue
		}
		gitDir := strings.TrimSpace(string(gitDirOut))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(d, gitDir)
		}
		gitDir, _ = filepath.Abs(gitDir)

		// Get git common dir
		gitCommonDirCmd := exec.Command("git", "-C", d, "rev-parse", "--git-common-dir")
		gitCommonDirOut, err := gitCommonDirCmd.Output()
		if err != nil {
			continue
		}
		gitCommonDir := strings.TrimSpace(string(gitCommonDirOut))
		if !filepath.IsAbs(gitCommonDir) {
			gitCommonDir = filepath.Join(d, gitCommonDir)
		}
		gitCommonDir, _ = filepath.Abs(gitCommonDir)

		// If git-dir != git-common-dir, it's a worktree
		if gitDir != gitCommonDir {
			commonRoot := filepath.Dir(gitCommonDir)
			if !seen[commonRoot] {
				seen[commonRoot] = true
				roots = append(roots, commonRoot)
			}
		}
	}

	return roots, nil
}

// GetGitIdentity returns the git user.name and user.email from global config
func GetGitIdentity() (name, email string) {
	nameCmd := exec.Command("git", "config", "--global", "user.name")
	if out, err := nameCmd.Output(); err == nil {
		name = strings.TrimSpace(string(out))
	}

	emailCmd := exec.Command("git", "config", "--global", "user.email")
	if out, err := emailCmd.Output(); err == nil {
		email = strings.TrimSpace(string(out))
	}

	return name, email
}
