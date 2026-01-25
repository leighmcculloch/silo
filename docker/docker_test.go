package docker

import (
	"testing"

	"github.com/leighmcculloch/silo/backend"
)

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
		Image:        "test-image",
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

	if opts.Image != "test-image" {
		t.Error("unexpected image")
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
