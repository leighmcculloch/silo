//go:build !darwin

package container

import (
	"context"
	"fmt"

	"github.com/leighmcculloch/silo/backend"
)

// Client is a stub for non-Darwin platforms.
type Client struct{}

// NewClient returns an error on non-Darwin platforms as the container backend
// requires macOS with the Apple container CLI.
func NewClient() (*Client, error) {
	return nil, fmt.Errorf("container backend is only available on macOS")
}

// Close is a no-op stub.
func (c *Client) Close() error {
	return nil
}

// ImageExists is a stub that always returns an error.
func (c *Client) ImageExists(ctx context.Context, name string) (bool, error) {
	return false, fmt.Errorf("container backend is only available on macOS")
}

// Build is a stub that always returns an error.
func (c *Client) Build(ctx context.Context, opts backend.BuildOptions) (string, error) {
	return "", fmt.Errorf("container backend is only available on macOS")
}

// Run is a stub that always returns an error.
func (c *Client) Run(ctx context.Context, opts backend.RunOptions) error {
	return fmt.Errorf("container backend is only available on macOS")
}

// List is a stub that always returns an error.
func (c *Client) List(ctx context.Context) ([]backend.ContainerInfo, error) {
	return nil, fmt.Errorf("container backend is only available on macOS")
}

// Remove is a stub that always returns an error.
func (c *Client) Remove(ctx context.Context, names []string) ([]string, error) {
	return nil, fmt.Errorf("container backend is only available on macOS")
}

// NextContainerName is a stub that returns an empty string.
func (c *Client) NextContainerName(ctx context.Context, baseName string) string {
	return ""
}

// Ensure Client implements backend.Backend at compile time.
var _ backend.Backend = (*Client)(nil)
