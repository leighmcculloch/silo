//go:build !darwin

package avf

import (
	"context"
	"fmt"

	"github.com/leighmcculloch/silo/backend"
)

// Backend is a stub for non-Darwin platforms
type Backend struct{}

// NewBackend returns an error on non-Darwin platforms
func NewBackend() (*Backend, error) {
	return nil, fmt.Errorf("AVF backend is only available on macOS")
}

// Name returns the backend name
func (b *Backend) Name() string {
	return "avf"
}

// Close is a no-op on non-Darwin platforms
func (b *Backend) Close() error {
	return nil
}

// Build returns an error on non-Darwin platforms
func (b *Backend) Build(ctx context.Context, opts backend.BuildOptions) error {
	return fmt.Errorf("AVF backend is only available on macOS")
}

// Run returns an error on non-Darwin platforms
func (b *Backend) Run(ctx context.Context, opts backend.RunOptions) error {
	return fmt.Errorf("AVF backend is only available on macOS")
}
