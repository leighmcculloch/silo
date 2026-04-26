package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/leighmcculloch/silo/config"
)

// BackgroundBuildOptions contains the parameters needed to launch a
// detached background build process.
type BackgroundBuildOptions struct {
	ImageTag      string
	Dockerfile    string
	BuildArgs     map[string]string
	Backend       string
	Tool          string
	FlyApp        string
	FlyRegion     string
	NamespaceSize string
}

// buildManifest is the JSON structure written to the build state directory.
// The __build command reads this single file to get all build parameters.
type buildManifest struct {
	ImageTag      string            `json:"image_tag"`
	Tool          string            `json:"tool"`
	Backend       string            `json:"backend,omitempty"`
	FlyApp        string            `json:"fly_app,omitempty"`
	FlyRegion     string            `json:"fly_region,omitempty"`
	NamespaceSize string            `json:"namespace_size,omitempty"`
	BuildArgs     map[string]string `json:"build_args"`
	Dockerfile    string            `json:"dockerfile"`
}

// ReadBuildManifest reads the build manifest from a state directory.
func ReadBuildManifest(dir string) (imageTag string, cfg config.Config, tool, dockerfile string, buildArgs map[string]string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, "build.json"))
	if err != nil {
		return "", config.Config{}, "", "", nil, fmt.Errorf("read manifest: %w", err)
	}
	var m buildManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", config.Config{}, "", "", nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.ImageTag == "" || m.Tool == "" {
		return "", config.Config{}, "", "", nil, fmt.Errorf("manifest missing required fields (image_tag, tool)")
	}
	c := config.Config{
		Backend: m.Backend,
		Backends: config.BackendsConfig{
			Fly: config.FlyConfig{
				App:    m.FlyApp,
				Region: m.FlyRegion,
			},
			Namespace: config.NamespaceConfig{
				Size: m.NamespaceSize,
			},
		},
	}
	return m.ImageTag, c, m.Tool, m.Dockerfile, m.BuildArgs, nil
}

// LaunchBackgroundBuild writes the build inputs to the build state directory
// and spawns a detached `silo __build` subprocess that will survive if the
// parent session exits.
func LaunchBackgroundBuild(opts BackgroundBuildOptions) error {
	dir := buildDir(opts.ImageTag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("bgbuild: mkdir: %w", err)
	}

	// Write all build parameters to a single manifest file.
	manifest := buildManifest{
		ImageTag:      opts.ImageTag,
		Tool:          opts.Tool,
		Backend:       opts.Backend,
		FlyApp:        opts.FlyApp,
		FlyRegion:     opts.FlyRegion,
		NamespaceSize: opts.NamespaceSize,
		BuildArgs:     opts.BuildArgs,
		Dockerfile:    opts.Dockerfile,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("bgbuild: marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.json"), manifestJSON, 0o644); err != nil {
		return fmt.Errorf("bgbuild: write manifest: %w", err)
	}

	// Find the silo binary (same binary that is currently running).
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("bgbuild: find executable: %w", err)
	}

	// Open log file in the shared logs directory so it appears in `silo logs`.
	logDir := filepath.Join(config.XDGStateHomeDir(), "silo", "logs")
	_ = os.MkdirAll(logDir, 0o755)
	logName := fmt.Sprintf("%s-build-%s.log", time.Now().Format("20060102-150405"), opts.Tool)
	logPath := filepath.Join(logDir, logName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("bgbuild: open log: %w", err)
	}

	cmd := exec.Command(selfPath, "__build", "--dir", dir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // detach from parent session
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("bgbuild: start: %w", err)
	}

	// Release the file and process handles — the child is fully detached.
	logFile.Close()
	_ = cmd.Process.Release()

	return nil
}
