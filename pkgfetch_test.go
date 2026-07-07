package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FetchPackage streams the proxied package bytes to disk atomically and sends
// the bearer token + platform query.
func TestFetchPackageWritesFile(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("PTARBYTES"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")

	dst := filepath.Join(t.TempDir(), "s3_v1.1.4_linux_arm64.ptar")
	if err := c.FetchPackage(context.Background(), "s3", "v1.1.4", "linux", "arm64", dst); err != nil {
		t.Fatalf("FetchPackage: %v", err)
	}

	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(b) != "PTARBYTES" {
		t.Errorf("contents = %q, want PTARBYTES", b)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if !strings.Contains(gotPath, "/api/v1/edge/packages/s3/v1.1.4") ||
		!strings.Contains(gotPath, "os=linux") || !strings.Contains(gotPath, "arch=arm64") {
		t.Errorf("request path = %q, missing name/version/os/arch", gotPath)
	}
	// No leftover .part temp file.
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part temp file was not cleaned up")
	}
}

// A non-200 from the proxy is an error and leaves no file behind.
func TestFetchPackageErrorLeavesNoFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "missing.ptar")
	err := c(srv).FetchPackage(context.Background(), "nope", "v9", "linux", "arm64", dst)
	if err == nil {
		t.Fatal("want error on 404")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("dst should not exist after a failed fetch")
	}
}

func c(srv *httptest.Server) *Client { return NewClient(srv.URL) }
