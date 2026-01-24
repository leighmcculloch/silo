package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leighmcculloch/silo/backend"
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

func TestBuildOptions(t *testing.T) {
	opts := backend.BuildOptions{
		Dockerfile: "FROM alpine",
		Target:     "test",
		BuildArgs: map[string]string{
			"ARG1": "value1",
		},
	}

	if opts.Dockerfile != "FROM alpine" {
		t.Error("unexpected dockerfile")
	}
	if opts.Target != "test" {
		t.Error("unexpected target")
	}
	if opts.BuildArgs["ARG1"] != "value1" {
		t.Error("unexpected build arg")
	}
}

func TestRunOptions(t *testing.T) {
	opts := backend.RunOptions{
		Tool:         "test-image",
		Name:         "test-container",
		WorkDir:      "/app",
		MountsRO:     []string{"/host/ro:/container/ro"},
		MountsRW:     []string{"/host/rw:/container/rw"},
		Env:          []string{"KEY=value"},
		Args:         []string{"arg1", "arg2"},
		TTY:          true,
		RemoveOnExit: true,
		SecurityOptions: []string{
			"no-new-privileges:true",
		},
	}

	if opts.Tool != "test-image" {
		t.Error("unexpected tool")
	}
	if opts.Name != "test-container" {
		t.Error("unexpected name")
	}
	if opts.WorkDir != "/app" {
		t.Error("unexpected workdir")
	}
	if len(opts.MountsRO) != 1 {
		t.Error("unexpected mounts_ro")
	}
	if len(opts.MountsRW) != 1 {
		t.Error("unexpected mounts_rw")
	}
	if len(opts.Env) != 1 {
		t.Error("unexpected env")
	}
	if len(opts.Args) != 2 {
		t.Error("unexpected args")
	}
	if !opts.TTY {
		t.Error("expected TTY to be true")
	}
	if !opts.RemoveOnExit {
		t.Error("expected RemoveOnExit to be true")
	}
	if len(opts.SecurityOptions) != 1 {
		t.Error("unexpected security options")
	}
}
