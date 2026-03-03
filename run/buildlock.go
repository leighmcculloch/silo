package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/leighmcculloch/silo/config"
)

// BuildLock holds a filesystem-based advisory lock for a build.
type BuildLock struct {
	file *os.File
	dir  string
}

// buildDir returns the state directory for a given image tag build.
func buildDir(imageTag string) string {
	return filepath.Join(config.XDGStateHomeDir(), "silo", "builds", imageTag)
}

// TryLock attempts to acquire an exclusive non-blocking flock for the given
// image tag. Returns a *BuildLock on success. Returns nil, nil if another
// process already holds the lock (i.e. a build is in progress).
func TryLock(imageTag string) (*BuildLock, error) {
	dir := buildDir(imageTag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("buildlock: mkdir: %w", err)
	}

	lockPath := filepath.Join(dir, "lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("buildlock: open: %w", err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		// EWOULDBLOCK means another process holds the lock.
		if err == syscall.EWOULDBLOCK {
			return nil, nil
		}
		return nil, fmt.Errorf("buildlock: flock: %w", err)
	}

	// Write PID for observability.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())

	// Write status file.
	_ = os.WriteFile(filepath.Join(dir, "status"), []byte("building\n"), 0o644)

	return &BuildLock{file: f, dir: dir}, nil
}

// Unlock releases the flock and removes the build directory.
func (bl *BuildLock) Unlock() {
	if bl.file != nil {
		_ = syscall.Flock(int(bl.file.Fd()), syscall.LOCK_UN)
		bl.file.Close()
	}
	_ = os.RemoveAll(bl.dir)
}

// WriteStatus writes a status string to the build directory.
func (bl *BuildLock) WriteStatus(status string) {
	_ = os.WriteFile(filepath.Join(bl.dir, "status"), []byte(status+"\n"), 0o644)
}

// Dir returns the build directory path.
func (bl *BuildLock) Dir() string {
	return bl.dir
}

// IsBuilding checks whether a build for the given image tag is currently in
// progress. It attempts a non-blocking flock: if it fails, a live process
// holds the lock. If it succeeds, the old lock was stale (process died) and
// we clean it up and return false.
func IsBuilding(imageTag string) bool {
	dir := buildDir(imageTag)
	lockPath := filepath.Join(dir, "lock")

	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		// No lock file means no build.
		return false
	}
	defer f.Close()

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Could not acquire → another process holds it → building.
		return true
	}

	// We acquired the lock → stale. Release and clean up.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
	_ = os.RemoveAll(dir)
	return false
}

// ReadBuildPID reads the PID from a build lock directory, or 0 if unavailable.
func ReadBuildPID(imageTag string) int {
	data, err := os.ReadFile(filepath.Join(buildDir(imageTag), "lock"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(data[:len(data)-1])) // trim newline
	return pid
}
