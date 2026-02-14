package tools

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
	orig := versionCachePath
	versionCachePath = func(tool string) string {
		return filepath.Join(tmp, tool)
	}
	t.Cleanup(func() { versionCachePath = orig })
	return tmp
}

func TestFetchVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("1.2.3\n"))
	}))
	defer srv.Close()

	overrideCachePath(t)

	tool := Tool{Name: "claude", LatestVersion: FetchURLVersion(srv.URL)}
	tool.FetchVersion(context.Background())

	got := tool.CachedVersion()
	if got != "1.2.3" {
		t.Errorf("CachedVersion = %q, want %q", got, "1.2.3")
	}
}

func TestCachedVersionEmpty(t *testing.T) {
	overrideCachePath(t)

	tool := Tool{Name: "claude"}
	got := tool.CachedVersion()
	if got != "" {
		t.Errorf("CachedVersion = %q, want empty string", got)
	}
}

func TestFetchVersionNetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmp := overrideCachePath(t)

	// Pre-populate cache
	os.WriteFile(filepath.Join(tmp, "claude"), []byte("1.0.0"), 0o644)

	tool := Tool{Name: "claude", LatestVersion: FetchURLVersion(srv.URL)}
	tool.FetchVersion(context.Background())

	// Existing cache should not be overwritten on failure
	got := tool.CachedVersion()
	if got != "1.0.0" {
		t.Errorf("CachedVersion = %q, want %q (should preserve existing cache)", got, "1.0.0")
	}
}

func TestFetchVersionNilFunc(t *testing.T) {
	overrideCachePath(t)

	tool := Tool{Name: "unsupported-tool"}
	tool.FetchVersion(context.Background())

	got := tool.CachedVersion()
	if got != "" {
		t.Errorf("CachedVersion = %q, want empty string for tool with no LatestVersion", got)
	}
}
