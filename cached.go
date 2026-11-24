package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/PlakarKorp/plaklet/cached"
)

func RunAsCached(args []string) int {
	var pkgdir, cachedir string

	log.SetPrefix("cached: ")

	fs := flag.NewFlagSet("plaklet", flag.ContinueOnError)
	fs.StringVar(&pkgdir, "pkg", "", "package/integrations directory (reserved; connectors are linked in-process)")
	fs.StringVar(&cachedir, "cache", "", "cache directory (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if cachedir == "" {
		log.Println("-cache is required")
		return 2
	}
	if pkgdir == "" {
		log.Println("-pkg is required")
		return 2
	}

	err := cached.RunCached(cachedir, pkgdir)
	if err != nil {
		log.Printf("failed: %v", err)
		return 1
	}

	return 0
}

func startCached(ctx context.Context, cachedir string, pkgdir string) error {
	for _, dir := range []string{cachedir, pkgdir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	mkcmd := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, self, "cached", "-cache", cachedir, "-pkg", pkgdir)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return cmd
	}

	cmd := mkcmd()
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		for {
			delay := time.Second

			err := cmd.Wait()
			if ctx.Err() != nil {
				return
			}

			log.Printf("cached stopped: %v, restarting after %v", err, delay)

			for {
				select {
				case <-time.After(delay):
					break
				case <-ctx.Done():
					return
				}

				cmd = mkcmd()
				if err := cmd.Start(); err != nil {
					delay = min(delay*2, 10)
					log.Printf("failed to restart cached: %v, retrying after %s", err, delay)
				}
				break
			}
		}
	}()

	return nil
}
