package ssh

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
)

// Syncer handles file synchronization between the local machine and a remote
// host over SSH. It pushes files via rsync, then optionally watches for
// remote changes on RW paths using inotifywait and pulls them back.
type Syncer struct {
	cfg     SSHBackendConfig
	sshConn *cryptossh.Client

	watchCancel context.CancelFunc
	watchDone   chan struct{}
}

// NewSyncer creates a Syncer that uses the given SSH connection for
// remote commands and rsync transport.
func NewSyncer(cfg SSHBackendConfig, sshConn *cryptossh.Client) *Syncer {
	return &Syncer{
		cfg:     cfg,
		sshConn: sshConn,
	}
}

// Push synchronizes local paths to the remote host using rsync, returning
// a mapping of local path → remote path for each synced directory.
func (s *Syncer) Push(ctx context.Context, localPaths []string) (map[string]string, error) {
	remotePaths := make(map[string]string)
	for _, localPath := range localPaths {
		remotePath := localPath

		// Ensure the remote directory exists.
		mkdirCmd := fmt.Sprintf("mkdir -p %s", shellQuote(remotePath))
		if _, err := execRemote(s.sshConn, mkdirCmd); err != nil {
			return nil, fmt.Errorf("mkdir remote %s: %w", remotePath, err)
		}

		if err := s.rsyncPush(ctx, localPath, remotePath); err != nil {
			return nil, fmt.Errorf("rsync push %s: %w", localPath, err)
		}
		remotePaths[localPath] = remotePath
	}
	return remotePaths, nil
}

// WatchAndPullBack starts a background inotifywait watcher on the remote
// host for the given RW path mappings (local→remote). When files change
// on the remote, they are pulled back to the local machine after a 1-second
// debounce period.
//
// If inotifywait is not installed on the remote, a warning is logged and
// the method returns nil (push + final pull still work).
func (s *Syncer) WatchAndPullBack(ctx context.Context, rwMappings map[string]string) error {
	if len(rwMappings) == 0 {
		return nil
	}

	// Check if inotifywait is available on remote.
	if _, err := execRemote(s.sshConn, "command -v inotifywait"); err != nil {
		log.Printf("warning: inotifywait not found on remote; RW pull-back disabled (install inotify-tools)")
		return nil
	}

	// Collect remote paths to watch.
	var remotePaths []string
	for _, remotePath := range rwMappings {
		remotePaths = append(remotePaths, shellQuote(remotePath))
	}

	// Build reverse mapping: remote→local.
	remoteToLocal := make(map[string]string, len(rwMappings))
	for local, remote := range rwMappings {
		remoteToLocal[remote] = local
	}

	watchCtx, cancel := context.WithCancel(ctx)
	s.watchCancel = cancel
	s.watchDone = make(chan struct{})

	cmd := fmt.Sprintf("inotifywait -m -r -e close_write,create,delete,moved_to --format '%%w' %s",
		strings.Join(remotePaths, " "))

	go s.runWatcher(watchCtx, cmd, rwMappings, remoteToLocal)
	return nil
}

// PullBack performs a one-shot rsync pull for all RW mappings.
func (s *Syncer) PullBack(ctx context.Context, rwMappings map[string]string) error {
	for localPath, remotePath := range rwMappings {
		if err := s.rsyncPull(ctx, localPath, remotePath); err != nil {
			return fmt.Errorf("rsync pull %s: %w", localPath, err)
		}
	}
	return nil
}

// Close stops the background watcher (if running), performs a final pull-back,
// and releases resources.
func (s *Syncer) Close() error {
	if s.watchCancel != nil {
		s.watchCancel()
		<-s.watchDone
	}
	return nil
}

// runWatcher runs inotifywait over SSH and debounces changes, pulling back
// affected directories when events settle.
func (s *Syncer) runWatcher(ctx context.Context, cmd string, rwMappings, remoteToLocal map[string]string) {
	defer close(s.watchDone)

	// Track which local paths need syncing; protected by mu.
	var mu sync.Mutex
	pendingPaths := make(map[string]string) // local→remote
	var debounceTimer *time.Timer

	flushPending := func() {
		mu.Lock()
		paths := pendingPaths
		pendingPaths = make(map[string]string)
		mu.Unlock()

		for localPath, remotePath := range paths {
			if err := s.rsyncPull(ctx, localPath, remotePath); err != nil {
				log.Printf("pull-back %s: %v", localPath, err)
			}
		}
	}

	onOutput := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}

		// line is the directory where the event occurred. Find which
		// watched root it belongs to.
		for remote, local := range remoteToLocal {
			if strings.HasPrefix(line, remote) {
				mu.Lock()
				pendingPaths[local] = remote
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(1*time.Second, flushPending)
				mu.Unlock()
				break
			}
		}
	}

	err := execRemoteStreaming(ctx, s.sshConn, cmd, onOutput)
	if err != nil && ctx.Err() == nil {
		log.Printf("inotifywait exited: %v", err)
	}

	// Drain any pending debounce.
	mu.Lock()
	if debounceTimer != nil {
		debounceTimer.Stop()
	}
	mu.Unlock()

	// Final pull-back on shutdown.
	pullCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.PullBack(pullCtx, rwMappings); err != nil {
		log.Printf("final pull-back: %v", err)
	}
}

// rsyncPush runs rsync to push local files to the remote (mirrors local).
func (s *Syncer) rsyncPush(ctx context.Context, localPath, remotePath string) error {
	target := sshTarget(s.cfg)
	remote := target + ":" + remotePath + "/"

	args := []string{"-az", "--delete", "-e", s.sshCommand()}
	for _, pattern := range s.cfg.SyncIgnore {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, localPath+"/", remote)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// rsyncPull runs rsync to pull remote files back to local (additive, no --delete).
func (s *Syncer) rsyncPull(ctx context.Context, localPath, remotePath string) error {
	target := sshTarget(s.cfg)
	remote := target + ":" + remotePath + "/"

	args := []string{"-az", "-e", s.sshCommand()}
	for _, pattern := range s.cfg.SyncIgnore {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, remote, localPath+"/")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

// sshCommand builds the SSH command string for rsync's -e flag.
func (s *Syncer) sshCommand() string {
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

// sshTarget returns the user@host string for SSH/rsync commands.
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
