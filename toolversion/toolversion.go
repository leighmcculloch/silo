package toolversion

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
)

// FetchAndCache fetches the latest version for tool from url and writes it to
// the cache file. Intended to be called from a goroutine. Errors are silently
// ignored. If url is empty the call is a no-op.
func FetchAndCache(ctx context.Context, tool, url string) {
	if url == "" {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	version := strings.TrimSpace(string(body))
	if version == "" {
		return
	}

	p := cachePath(tool)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(version), 0o644)
}

// ReadCached reads the cached version for tool. Returns "" if no cache exists.
func ReadCached(tool string) string {
	data, err := os.ReadFile(cachePath(tool))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

var cachePath = func(tool string) string {
	return filepath.Join(xdg.CacheHome, "silo", "tool-versions", tool)
}
