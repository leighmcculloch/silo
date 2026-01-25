//go:build darwin

package avf

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTarWithRestrictivePermissions(t *testing.T) {
	// Create a temp directory for our test
	tmpDir, err := os.MkdirTemp("", "extract-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a tar archive with directories that have restrictive permissions
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add a directory with restrictive permissions (no write)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "restrictive-dir/",
		Mode:     0555, // read + execute only
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write dir header: %v", err)
	}

	// Add a subdirectory inside the restrictive directory
	if err := tw.WriteHeader(&tar.Header{
		Name:     "restrictive-dir/subdir/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write subdir header: %v", err)
	}

	// Add a file inside the subdirectory
	content := []byte("test content")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "restrictive-dir/subdir/file.txt",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("failed to write file header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar: %v", err)
	}

	// Test extraction
	destDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	if err := extractTar(&buf, destDir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	// Verify the file was extracted
	filePath := filepath.Join(destDir, "restrictive-dir/subdir/file.txt")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(data) != "test content" {
		t.Errorf("file content mismatch: got %q, want %q", string(data), "test content")
	}
}

func TestExtractTarWithPreExistingRestrictiveDir(t *testing.T) {
	// Create a temp directory for our test
	tmpDir, err := os.MkdirTemp("", "extract-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	destDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	// Pre-create a directory with restrictive permissions (simulating previous extraction)
	restrictiveDir := filepath.Join(destDir, "pre-existing")
	if err := os.MkdirAll(restrictiveDir, 0555); err != nil {
		t.Fatalf("failed to create restrictive dir: %v", err)
	}

	// Create a tar archive that tries to create a subdir inside the pre-existing dir
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add a subdirectory inside the pre-existing directory
	if err := tw.WriteHeader(&tar.Header{
		Name:     "pre-existing/newsubdir/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write subdir header: %v", err)
	}

	// Add a file
	content := []byte("new content")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "pre-existing/newsubdir/newfile.txt",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("failed to write file header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("failed to write file content: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar: %v", err)
	}

	// Test extraction - this should fix permissions and succeed
	if err := extractTar(&buf, destDir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	// Verify the file was extracted
	filePath := filepath.Join(destDir, "pre-existing/newsubdir/newfile.txt")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("file content mismatch: got %q, want %q", string(data), "new content")
	}
}

func TestForceRemoveAll(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "remove-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir) // Fallback cleanup

	// Create a directory structure with restrictive permissions
	testDir := filepath.Join(tmpDir, "testdir")
	subDir := filepath.Join(testDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	// Create a file
	filePath := filepath.Join(subDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Make directories restrictive
	os.Chmod(subDir, 0555)
	os.Chmod(testDir, 0555)

	// forceRemoveAll should still be able to remove it
	forceRemoveAll(testDir)

	// Verify it's gone
	if _, err := os.Stat(testDir); !os.IsNotExist(err) {
		t.Errorf("testDir should not exist after forceRemoveAll, got err: %v", err)
	}
}

func TestExtractTarWithDeepRestrictiveDir(t *testing.T) {
	// Simulates the exact error:
	// mkdir .../toml@v1.5.0/.github: permission denied
	// where toml@v1.5.0 exists but is restrictive

	tmpDir, err := os.MkdirTemp("", "extract-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	destDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	// Create tar with a deep path where an intermediate dir is restrictive
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// First, add the restrictive parent directory
	if err := tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/",
		Mode:     0555, // restrictive!
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}

	// Then add a subdirectory - this would fail if permissions aren't fixed
	if err := tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}

	// Add a file inside
	content := []byte("workflow content")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/workflows.yml",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	tw.Write(content)
	tw.Close()

	// Extract
	if err := extractTar(&buf, destDir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	// Verify
	filePath := filepath.Join(destDir, "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/workflows.yml")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != "workflow content" {
		t.Errorf("content mismatch")
	}
}

func TestExtractTarFilesBeforeDirs(t *testing.T) {
	// Test case where file entries come before their parent directory entries
	// (tar archives don't guarantee order)

	tmpDir, err := os.MkdirTemp("", "extract-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	destDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// File BEFORE its parent directory entry
	content := []byte("file content")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "parentdir/subdir/file.txt",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}
	tw.Write(content)

	// Parent directory with restrictive permissions comes AFTER
	if err := tw.WriteHeader(&tar.Header{
		Name:     "parentdir/",
		Mode:     0555,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("failed to write header: %v", err)
	}

	tw.Close()

	if err := extractTar(&buf, destDir); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	filePath := filepath.Join(destDir, "parentdir/subdir/file.txt")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != "file content" {
		t.Errorf("content mismatch")
	}
}

func TestFullScenarioWithLeftoverRestrictiveDir(t *testing.T) {
	// This simulates the exact production scenario:
	// 1. Previous extraction left directories with restrictive permissions
	// 2. forceRemoveAll tries to clean up
	// 3. New extraction happens

	tmpDir, err := os.MkdirTemp("", "extract-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rootfsPath := filepath.Join(tmpDir, "opencode-rootfs")

	// Simulate previous extraction that left restrictive directories
	deepPath := filepath.Join(rootfsPath, "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github")
	if err := os.MkdirAll(deepPath, 0755); err != nil {
		t.Fatalf("failed to create deep path: %v", err)
	}

	// Write a file
	if err := os.WriteFile(filepath.Join(deepPath, "old-file.txt"), []byte("old"), 0644); err != nil {
		t.Fatalf("failed to write old file: %v", err)
	}

	// Make directories restrictive (simulating previous extraction)
	restrictivePath := filepath.Join(rootfsPath, "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0")
	os.Chmod(filepath.Join(restrictivePath, ".github"), 0555)
	os.Chmod(restrictivePath, 0555)
	os.Chmod(filepath.Join(rootfsPath, "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi"), 0555)
	os.Chmod(filepath.Join(rootfsPath, "Users/leighmcculloch/go/pkg/mod/github.com"), 0555)

	// Now do what exportRootfs does: forceRemoveAll then extract
	forceRemoveAll(rootfsPath)

	// Verify it's gone
	if _, err := os.Stat(rootfsPath); !os.IsNotExist(err) {
		t.Fatalf("rootfsPath should be removed, but got: %v", err)
	}

	// Recreate and extract
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		t.Fatalf("failed to recreate rootfsPath: %v", err)
	}

	// Create tar with same structure
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	tw.WriteHeader(&tar.Header{
		Name:     "Users/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/",
		Mode:     0555, // restrictive!
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/",
		Mode:     0555, // restrictive!
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/",
		Mode:     0555, // restrictive!
		Typeflag: tar.TypeDir,
	})
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	})

	content := []byte("new content")
	tw.WriteHeader(&tar.Header{
		Name:     "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/workflows.yml",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	})
	tw.Write(content)
	tw.Close()

	if err := extractTar(&buf, rootfsPath); err != nil {
		t.Fatalf("extractTar failed: %v", err)
	}

	// Verify
	filePath := filepath.Join(rootfsPath, "Users/leighmcculloch/go/pkg/mod/github.com/BurntSushi/toml@v1.5.0/.github/workflows.yml")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("content mismatch")
	}
}

// Helper to create tar for testing
func createTestTar(t *testing.T, entries []tarEntry) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     e.Mode,
			Typeflag: e.Type,
		}
		if e.Type == tar.TypeReg {
			hdr.Size = int64(len(e.Content))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write header for %s: %v", e.Name, err)
		}
		if e.Type == tar.TypeReg && len(e.Content) > 0 {
			if _, err := tw.Write([]byte(e.Content)); err != nil {
				t.Fatalf("failed to write content for %s: %v", e.Name, err)
			}
		}
	}
	tw.Close()
	return &buf
}

type tarEntry struct {
	Name    string
	Mode    int64
	Type    byte
	Content string
}

func TestCreateDiskImage(t *testing.T) {
	// This test verifies that createDiskImage works on macOS
	// (which requires using Docker since mkfs.ext4 isn't available natively)

	tmpDir, err := os.MkdirTemp("", "disk-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a simple rootfs
	rootfsPath := filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfsPath, "etc"), 0755); err != nil {
		t.Fatalf("failed to create rootfs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootfsPath, "etc/hostname"), []byte("testhost\n"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootfsPath, "bin"), 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	// Create backend to test disk image creation
	backend, err := NewBackend()
	if err != nil {
		t.Skipf("Docker not available, skipping test: %v", err)
	}
	defer backend.Close()

	diskPath := filepath.Join(tmpDir, "test.img")

	ctx := context.Background()
	if err := backend.createDiskImage(ctx, rootfsPath, diskPath); err != nil {
		t.Fatalf("createDiskImage failed: %v", err)
	}

	// Verify the disk image was created
	info, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("disk image not created: %v", err)
	}

	// Should be a reasonable size (at least the minimum we set)
	if info.Size() < 100*1024*1024 {
		t.Errorf("disk image too small: %d bytes", info.Size())
	}

	t.Logf("Created disk image: %s (%d bytes)", diskPath, info.Size())
}

func TestCreateDiskImageFromRealRootfs(t *testing.T) {
	// This test uses the actual rootfs from the cache if it exists
	// to test with a realistic scenario

	cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "silo", "avf")

	// Check for any existing rootfs
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Skipf("No cache directory, skipping: %v", err)
	}

	var rootfsPath string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), "-rootfs") {
			rootfsPath = filepath.Join(cacheDir, e.Name())
			break
		}
	}

	if rootfsPath == "" {
		t.Skip("No rootfs found in cache, skipping")
	}

	t.Logf("Testing with rootfs: %s", rootfsPath)

	backend, err := NewBackend()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	defer backend.Close()

	tmpDir, err := os.MkdirTemp("", "disk-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	diskPath := filepath.Join(tmpDir, "test.img")

	ctx := context.Background()
	if err := backend.createDiskImage(ctx, rootfsPath, diskPath); err != nil {
		t.Fatalf("createDiskImage failed: %v", err)
	}

	info, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("disk image not created: %v", err)
	}

	t.Logf("Created disk image: %s (%d bytes)", diskPath, info.Size())
}
