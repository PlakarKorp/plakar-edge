package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStartCachedCreatesDirs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "pkg")
	cachedir := filepath.Join(base, "cache")
	pkgdir := filepath.Join(base, "integrations")

	// A cancelled context keeps cached from actually being spawned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = startCached(ctx, cachedir, pkgdir)

	for _, dir := range []string{cachedir, pkgdir} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
}
