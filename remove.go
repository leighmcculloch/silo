package main

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/charmbracelet/huh"
	"github.com/leighmcculloch/silo/backend"
	"github.com/leighmcculloch/silo/cli"
	"github.com/leighmcculloch/silo/config"
)

type removeTarget struct {
	BackendType string
	Name        string
}

type removeCandidate struct {
	removeTarget
	Status    string
	IsRunning bool
}

func removeBackends(backendFlag string) []string {
	if backendFlag != "" {
		return []string{backendFlag}
	}
	return []string{"docker", "container", "daytona", "fly"}
}

func resolveRemoveTargets(ctx context.Context, cfg config.Config, backends, args []string, stderr io.Writer) (map[string][]string, error) {
	if len(args) > 0 {
		targetsByBackend := make(map[string][]string, len(backends))
		for _, backendType := range backends {
			targetsByBackend[backendType] = append([]string(nil), args...)
		}
		return targetsByBackend, nil
	}

	candidates := listRemoveCandidates(ctx, cfg, backends, stderr)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no silo containers found")
	}

	return promptRemoveTargets(candidates)
}

func listRemoveCandidates(ctx context.Context, cfg config.Config, backends []string, stderr io.Writer) []removeCandidate {
	var candidates []removeCandidate

	for _, backendType := range backends {
		backendClient, err := createBackendByType(backendType, cfg)
		if err != nil {
			cli.LogWarningTo(stderr, "%s not available: %v", backendType, err)
			continue
		}

		containers, err := backendClient.List(ctx)
		backendClient.Close()
		if err != nil {
			cli.LogWarningTo(stderr, "failed to list containers (%s): %v", backendType, err)
			continue
		}

		for _, ctr := range containers {
			candidates = append(candidates, removeCandidate{
				removeTarget: removeTarget{
					BackendType: backendType,
					Name:        ctr.Name,
				},
				Status:    ctr.Status,
				IsRunning: ctr.IsRunning,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].BackendType < candidates[j].BackendType
	})

	return candidates
}

func promptRemoveTargets(candidates []removeCandidate) (map[string][]string, error) {
	options := make([]huh.Option[removeTarget], 0, len(candidates))
	for _, candidate := range candidates {
		status := candidate.Status
		if status == "" {
			if candidate.IsRunning {
				status = "running"
			} else {
				status = "stopped"
			}
		}

		label := fmt.Sprintf("%s (%s", candidate.Name, candidate.BackendType)
		label += ", " + status
		label += ")"
		options = append(options, huh.NewOption(label, candidate.removeTarget))
	}

	var selected []removeTarget
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[removeTarget]().
				Title("Select Containers to Remove").
				Description("Press ctrl+a to select all, space to toggle, and / to filter. Running containers still require --force.").
				Options(options...).
				Value(&selected).
				Height(minInt(len(options), 12)).
				Validate(func(values []removeTarget) error {
					if len(values) == 0 {
						return fmt.Errorf("select at least one container")
					}
					return nil
				}),
		),
	)

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("selection cancelled")
	}

	return groupRemoveTargets(selected), nil
}

func groupRemoveTargets(targets []removeTarget) map[string][]string {
	grouped := make(map[string][]string, len(targets))
	seen := make(map[string]map[string]struct{}, len(targets))

	for _, target := range targets {
		if _, ok := seen[target.BackendType]; !ok {
			seen[target.BackendType] = make(map[string]struct{})
		}
		if _, ok := seen[target.BackendType][target.Name]; ok {
			continue
		}
		seen[target.BackendType][target.Name] = struct{}{}
		grouped[target.BackendType] = append(grouped[target.BackendType], target.Name)
	}

	return grouped
}

func filterRunningContainers(names []string, containers []backend.ContainerInfo, stderr io.Writer) []string {
	running := make(map[string]bool, len(containers))
	for _, ctr := range containers {
		if ctr.IsRunning {
			running[ctr.Name] = true
		}
	}

	var filtered []string
	for _, name := range names {
		if running[name] {
			cli.LogWarningTo(stderr, "container %s is running, use -f to force removal", name)
			continue
		}
		filtered = append(filtered, name)
	}

	return filtered
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
