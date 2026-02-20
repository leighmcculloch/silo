package ssh

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Syncer handles file synchronization between the local machine and a remote host.
type Syncer interface {
	// Sync synchronizes local paths to the remote, returning a mapping
	// of local path -> remote path for each synced directory.
	Sync(ctx context.Context, localPaths []string) (remotePaths map[string]string, err error)

	// Close terminates any persistent sync sessions and releases resources.
	Close() error
}

// NewSyncer returns a Syncer implementation based on the configured sync method.
// If syncMethod is "rsync", an RsyncSyncer is returned. Otherwise (including
// "mutagen" or empty), a MutagenSyncer is returned.
func NewSyncer(cfg SSHBackendConfig) Syncer {
	if cfg.SyncMethod == "rsync" {
		return &RsyncSyncer{cfg: cfg}
	}
	return &MutagenSyncer{cfg: cfg}
}

// remotePathFor maps a local absolute path to a remote path under the sync root.
// For example, /Users/leigh/Code/myproject -> ~/silo-sync/Users/leigh/Code/myproject
func remotePathFor(syncRoot, localPath string) string {
	// Clean the local path and strip the leading slash so it becomes a relative
	// component under the sync root.
	cleaned := filepath.Clean(localPath)
	rel := strings.TrimPrefix(cleaned, "/")
	return syncRoot + "/" + rel
}

// sshTarget returns the user@host string for SSH/rsync/mutagen commands.
func sshTarget(cfg SSHBackendConfig) string {
	user := cfg.User
	if user == "" {
		return cfg.Host
	}
	return user + "@" + cfg.Host
}

// sshPortFlag returns the SSH port flag value. Returns "22" if not configured.
func sshPortFlag(cfg SSHBackendConfig) string {
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%d", port)
}

// syncRoot returns the configured remote sync root, defaulting to ~/silo-sync.
func syncRoot(cfg SSHBackendConfig) string {
	if cfg.RemoteSyncRoot != "" {
		return cfg.RemoteSyncRoot
	}
	return "~/silo-sync"
}

// --- MutagenSyncer ---

// MutagenSyncer uses the mutagen CLI to create persistent, bidirectional file
// sync sessions between local directories and the remote host.
type MutagenSyncer struct {
	cfg        SSHBackendConfig
	sessionIDs []string
}

func (s *MutagenSyncer) Sync(ctx context.Context, localPaths []string) (map[string]string, error) {
	remotePaths := make(map[string]string)

	for _, localPath := range localPaths {
		remotePath := remotePathFor(syncRoot(s.cfg), localPath)

		sessionID, err := s.createSession(ctx, localPath, remotePath)
		if err != nil {
			return nil, fmt.Errorf("mutagen sync create %s: %w", localPath, err)
		}
		s.sessionIDs = append(s.sessionIDs, sessionID)

		if err := s.waitForSync(ctx, sessionID); err != nil {
			return nil, fmt.Errorf("mutagen sync monitor %s: %w", localPath, err)
		}

		remotePaths[localPath] = remotePath
	}

	return remotePaths, nil
}

// createSession runs `mutagen sync create` and returns the session ID.
func (s *MutagenSyncer) createSession(ctx context.Context, localPath, remotePath string) (string, error) {
	target := sshTarget(s.cfg)
	remote := target + ":" + remotePath

	args := []string{"sync", "create", localPath, remote}

	// Add ignore patterns.
	for _, pattern := range s.cfg.SyncIgnore {
		args = append(args, "--ignore", pattern)
	}

	// If a non-default port or identity file is set, pass SSH flags via
	// the --configuration flag isn't directly supported; mutagen uses the
	// SSH config or agent. For identity files, rely on the SSH agent or
	// ~/.ssh/config. For non-default ports, mutagen respects SSH config.

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "mutagen", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	// mutagen sync create prints the session ID on stdout.
	sessionID := strings.TrimSpace(stdout.String())
	if sessionID == "" {
		// Older mutagen versions may not print the ID. List sessions to find it.
		return s.findLatestSession(ctx, localPath)
	}
	return sessionID, nil
}

// findLatestSession lists mutagen sessions and returns the one matching localPath.
func (s *MutagenSyncer) findLatestSession(ctx context.Context, localPath string) (string, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "list")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mutagen sync list: %w", err)
	}

	// Parse output to find session for this path. The output format includes
	// lines like "Identifier: <id>" and "Alpha: <path>".
	var currentID string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Identifier:") {
			currentID = strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
		}
		if strings.HasPrefix(line, "Alpha:") {
			alpha := strings.TrimSpace(strings.TrimPrefix(line, "Alpha:"))
			if alpha == localPath && currentID != "" {
				return currentID, nil
			}
		}
	}

	return "", fmt.Errorf("could not find mutagen session for %s", localPath)
}

// waitForSync runs `mutagen sync monitor` and blocks until the session reports
// that it is up to date.
func (s *MutagenSyncer) waitForSync(ctx context.Context, sessionID string) error {
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "monitor", sessionID)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// mutagen sync monitor exits once the session reaches a steady state
	// (watching or idle), which indicates the initial sync is complete.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (s *MutagenSyncer) Close() error {
	var errs []string
	for _, id := range s.sessionIDs {
		cmd := exec.Command("mutagen", "sync", "terminate", id)
		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Sprintf("terminate session %s: %v", id, err))
		}
	}
	s.sessionIDs = nil
	if len(errs) > 0 {
		return fmt.Errorf("mutagen cleanup: %s", strings.Join(errs, "; "))
	}
	return nil
}

// --- RsyncSyncer ---

// RsyncSyncer uses rsync over SSH for one-shot file synchronization.
// It does not maintain persistent sessions.
type RsyncSyncer struct {
	cfg SSHBackendConfig
}

func (s *RsyncSyncer) Sync(ctx context.Context, localPaths []string) (map[string]string, error) {
	remotePaths := make(map[string]string)

	for _, localPath := range localPaths {
		remotePath := remotePathFor(syncRoot(s.cfg), localPath)

		if err := s.rsync(ctx, localPath, remotePath); err != nil {
			return nil, fmt.Errorf("rsync %s: %w", localPath, err)
		}

		remotePaths[localPath] = remotePath
	}

	return remotePaths, nil
}

func (s *RsyncSyncer) rsync(ctx context.Context, localPath, remotePath string) error {
	target := sshTarget(s.cfg)
	remote := target + ":" + remotePath + "/"

	sshCmd := s.sshCommand()

	args := []string{
		"rsync", "-az", "--delete",
		"-e", sshCmd,
	}

	for _, pattern := range s.cfg.SyncIgnore {
		args = append(args, "--exclude", pattern)
	}

	// Trailing slash on the source means "contents of this directory".
	args = append(args, localPath+"/", remote)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// sshCommand builds the SSH command string for rsync's -e flag.
func (s *RsyncSyncer) sshCommand() string {
	parts := []string{"ssh"}

	port := sshPortFlag(s.cfg)
	if port != "22" {
		parts = append(parts, "-p", port)
	}

	if s.cfg.IdentityFile != "" {
		parts = append(parts, "-i", s.cfg.IdentityFile)
	}

	return strings.Join(parts, " ")
}

// Close is a no-op for RsyncSyncer since it has no persistent sessions.
func (s *RsyncSyncer) Close() error {
	return nil
}
