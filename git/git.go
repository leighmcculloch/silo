package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GetGitWorktreeRoots returns git worktree common directories for the given directory.
// It detects worktrees by checking for a .git file (not directory) containing a gitdir pointer,
// avoiding subprocess calls entirely.
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
		commonRoot, ok := detectWorktreeRoot(d)
		if !ok {
			continue
		}
		if !seen[commonRoot] {
			seen[commonRoot] = true
			roots = append(roots, commonRoot)
		}
	}

	return roots, nil
}

// detectWorktreeRoot checks if d is a git worktree by reading its .git file.
// Git worktrees have a .git file (not directory) containing "gitdir: <path>",
// where <path> points into the main repo's .git/worktrees/<name> directory.
// Returns the main repo root and true if d is a worktree, or ("", false) otherwise.
func detectWorktreeRoot(d string) (string, bool) {
	dotGit := filepath.Join(d, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", false
	}

	// A .git directory means a standalone repo, not a worktree
	if info.IsDir() {
		return "", false
	}

	// A .git file means this is a worktree — read the gitdir pointer
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}

	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(content, "gitdir: ") {
		return "", false
	}
	gitDir := strings.TrimPrefix(content, "gitdir: ")

	// Resolve relative paths against the worktree directory
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(d, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	// The gitdir points to .git/worktrees/<name> in the main repo.
	// Walk up to find the .git directory (the common dir).
	// We look for a parent path component named "worktrees" whose parent is the .git dir.
	commonDir := resolveCommonDir(gitDir)
	if commonDir == "" {
		return "", false
	}

	// The root of the main repo is the parent of its .git directory
	commonRoot := filepath.Dir(commonDir)
	return commonRoot, true
}

// resolveCommonDir extracts the git common directory from a worktree gitdir path.
// Given a path like /repo/.git/worktrees/branch-name, it returns /repo/.git.
func resolveCommonDir(gitDir string) string {
	// Walk up the path looking for a "worktrees" component
	current := gitDir
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding worktrees/
			return ""
		}
		if filepath.Base(current) == "worktrees" {
			return parent
		}
		current = parent
	}
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

// GetGitRemoteURLs returns all remote URLs for the git repository in the given directory.
// If the directory is not a git repository, it returns an empty slice.
func GetGitRemoteURLs(dir string) []string {
	// Get list of remotes
	cmd := exec.Command("git", "-C", dir, "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	remotes := strings.Fields(string(out))
	var urls []string

	for _, remote := range remotes {
		urlCmd := exec.Command("git", "-C", dir, "remote", "get-url", remote)
		urlOut, err := urlCmd.Output()
		if err != nil {
			continue
		}
		url := strings.TrimSpace(string(urlOut))
		if url != "" {
			urls = append(urls, url)
		}
	}

	return urls
}
