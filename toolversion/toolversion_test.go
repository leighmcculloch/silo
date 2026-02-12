package toolversion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func overrideCachePath(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	orig := cachePath
	cachePath = func(tool string) string {
		return filepath.Join(tmp, tool)
	}
	t.Cleanup(func() { cachePath = orig })
	return tmp
}

func TestFetchAndCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("1.2.3\n"))
	}))
	defer srv.Close()

	orig := versionURLs["claude"]
	versionURLs["claude"] = srv.URL
	defer func() { versionURLs["claude"] = orig }()

	overrideCachePath(t)

	FetchAndCache(context.Background(), "claude")

	got := ReadCached("claude")
	if got != "1.2.3" {
		t.Errorf("ReadCached = %q, want %q", got, "1.2.3")
	}
}

func TestReadCachedEmpty(t *testing.T) {
	overrideCachePath(t)

	got := ReadCached("claude")
	if got != "" {
		t.Errorf("ReadCached = %q, want empty string", got)
	}
}

func TestFetchAndCacheNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := versionURLs["claude"]
	versionURLs["claude"] = srv.URL
	defer func() { versionURLs["claude"] = orig }()

	tmp := overrideCachePath(t)

	// Pre-populate cache
	os.WriteFile(filepath.Join(tmp, "claude"), []byte("1.0.0"), 0o644)

	FetchAndCache(context.Background(), "claude")

	// Existing cache should not be overwritten on failure
	got := ReadCached("claude")
	if got != "1.0.0" {
		t.Errorf("ReadCached = %q, want %q (should preserve existing cache)", got, "1.0.0")
	}
}

func TestFetchAndCacheUnsupportedTool(t *testing.T) {
	overrideCachePath(t)

	FetchAndCache(context.Background(), "unsupported-tool")

	got := ReadCached("unsupported-tool")
	if got != "" {
		t.Errorf("ReadCached = %q, want empty string for unsupported tool", got)
	}
}
