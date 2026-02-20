package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Connect establishes an SSH connection using the provided configuration.
// It tries identity file authentication first (if configured), then falls
// back to the SSH agent via SSH_AUTH_SOCK.
func Connect(cfg SSHBackendConfig) (*ssh.Client, error) {
	host := cfg.Host
	if host == "" {
		return nil, fmt.Errorf("ssh: host is required")
	}

	port := cfg.Port
	if port == 0 {
		port = 22
	}

	user := cfg.User
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		return nil, fmt.Errorf("ssh: user is required (set backends.ssh.user or $USER)")
	}

	var authMethods []ssh.AuthMethod

	// Try identity file if specified.
	if cfg.IdentityFile != "" {
		keyPath := expandPath(cfg.IdentityFile)
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("ssh: read identity file %s: %w", keyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("ssh: parse identity file %s: %w", keyPath, err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Try SSH agent.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		agentConn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(agentConn)
			authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("ssh: no authentication methods available (set identity_file or run ssh-agent)")
	}

	hostKeyCallback, err := knownHostsCallback()
	if err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}
	return client, nil
}

// knownHostsCallback returns a host key callback that verifies against
// ~/.ssh/known_hosts. Returns an error if the known_hosts file cannot
// be parsed, rather than silently falling back to insecure mode.
func knownHostsCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory for known_hosts: %w", err)
	}
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(knownHostsPath); err != nil {
		return nil, fmt.Errorf("known_hosts file not found at %s: %w (add the host with ssh-keyscan)", knownHostsPath, err)
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("parse known_hosts: %w", err)
	}
	return callback, nil
}

// execRemote runs a command on the remote host and returns its combined
// stdout output as a trimmed string.
func execRemote(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmd); err != nil {
		return "", fmt.Errorf("ssh exec %q: %w\nstderr: %s", cmd, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// execRemoteStreaming runs a command on the remote host and calls onOutput
// for each line of combined stdout/stderr output. It respects context
// cancellation.
func execRemoteStreaming(ctx context.Context, client *ssh.Client, cmd string, onOutput func(string)) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	// Pipe stdout and stderr together through onOutput.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	defer pr.Close()

	session.Stdout = pw
	session.Stderr = pw

	if err := session.Start(cmd); err != nil {
		pw.Close()
		return fmt.Errorf("ssh start %q: %w", cmd, err)
	}
	pw.Close()

	// Read output in a goroutine.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 4096)
		for {
			n, readErr := pr.Read(buf)
			if n > 0 && onOutput != nil {
				onOutput(string(buf[:n]))
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Wait for completion or context cancellation.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		// Signal the remote process to terminate.
		_ = session.Signal(ssh.SIGTERM)
		<-outputDone
		return ctx.Err()
	case err := <-waitDone:
		<-outputDone
		return err
	}
}

// writeRemoteFile writes content to a file on the remote host using cat
// with a heredoc via SSH.
func writeRemoteFile(client *ssh.Client, path string, content string) error {
	// Ensure parent directory exists, then write content via stdin.
	dir := filepath.Dir(path)
	mkdirCmd := fmt.Sprintf("mkdir -p %s", shellQuote(dir))
	if _, err := execRemote(client, mkdirCmd); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	session.Stdin = strings.NewReader(content)

	var stderr bytes.Buffer
	session.Stderr = &stderr

	// TODO: replace shell quote with base64 or hex xxd encoding
	cmd := fmt.Sprintf("cat > %s", shellQuote(path))
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("write %s: %w\nstderr: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// expandPath expands a leading ~ to the user's home directory.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}
