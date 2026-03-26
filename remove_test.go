package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/leighmcculloch/silo/backend"
)

func TestRmCommandAllowsNoArgs(t *testing.T) {
	rootCmd := newRootCmd(io.Discard, io.Discard)
	rmCmd, _, err := rootCmd.Find([]string{"rm"})
	if err != nil {
		t.Fatalf("failed to find rm command: %v", err)
	}

	if err := rmCmd.Args(rmCmd, nil); err != nil {
		t.Fatalf("expected rm to allow zero args, got %v", err)
	}
}

func TestGroupRemoveTargets(t *testing.T) {
	grouped := groupRemoveTargets([]removeTarget{
		{BackendType: "docker", Name: "alpha"},
		{BackendType: "docker", Name: "alpha"},
		{BackendType: "container", Name: "alpha"},
		{BackendType: "docker", Name: "beta"},
	})

	dockerTargets := grouped["docker"]
	if len(dockerTargets) != 2 || dockerTargets[0] != "alpha" || dockerTargets[1] != "beta" {
		t.Fatalf("unexpected docker targets: %v", dockerTargets)
	}

	containerTargets := grouped["container"]
	if len(containerTargets) != 1 || containerTargets[0] != "alpha" {
		t.Fatalf("unexpected container targets: %v", containerTargets)
	}
}

func TestFilterRunningContainers(t *testing.T) {
	var stderr bytes.Buffer

	filtered := filterRunningContainers([]string{"alpha", "beta", "gamma"}, []backend.ContainerInfo{
		{Name: "alpha", IsRunning: true},
		{Name: "beta", IsRunning: false},
	}, &stderr)

	if len(filtered) != 2 || filtered[0] != "beta" || filtered[1] != "gamma" {
		t.Fatalf("unexpected filtered targets: %v", filtered)
	}

	if got := stderr.String(); got == "" || !bytes.Contains([]byte(got), []byte("container alpha is running")) {
		t.Fatalf("expected running container warning, got %q", got)
	}
}
