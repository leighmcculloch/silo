package tilde

import (
	"os"
	"strings"
)

// Path replaces the home directory prefix with ~ in paths
func Path(path string) string {
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}
