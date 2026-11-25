package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/PlakarKorp/kloset/caching"
	"github.com/PlakarKorp/kloset/caching/pebble"
	"github.com/PlakarKorp/kloset/kcontext"
	"github.com/PlakarKorp/kloset/logging"
	"github.com/PlakarKorp/pkg"
	"github.com/PlakarKorp/plakar-edge/signify"
)

type pkgManager struct {
	tmpdir        string
	intdir        string
	backend       *pkg.FlatBackend
	verifier      *signify.Verifier
	allowUnsigned []string
	clt           *Client
}

var ErrPkgNotSigned = errors.New("package not signed")

var thePkgManager *pkgManager

func NewPkgManager(cfg *Config, clt *Client) (*pkgManager, error) {
	pkgdir := cfg.plakletPkgDir()
	cachedir := cfg.plakletCacheDir()

	ctx := kcontext.NewKContext()
	ctx.CacheDir = cachedir
	ctx.MaxConcurrency = 1
	ctx.SetLogger(logging.NewLogger(os.Stderr, os.Stderr))
	ctx.SetCache(caching.NewManager(pebble.InMemoryConstructor()))

	var m pkgManager
	var err error

	m.intdir = pkgdir
	m.clt = clt
	m.verifier = signify.NewVerifier(signify.NewTrustStore(signify.BuiltinKeys()...))
	m.tmpdir = os.Getenv("PLAKAR_EDGE_PKG_TMPDIR")

	for _, v := range strings.Split(os.Getenv("PLAKAR_EDGE_PKG_ALLOW_UNSIGNED"), ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			m.allowUnsigned = append(m.allowUnsigned, v)
		}
	}

	m.backend, err = pkg.NewFlatBackend(ctx, pkgdir, cachedir, &pkg.FlatBackendOptions{})
	if err != nil {
		return nil, fmt.Errorf("package backend: %w", err)
	}

	lst, err := m.ListPackages()
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	for _, p := range lst {
		err := m.VerifyPackage(p)
		switch {
		case err != nil:
			if errors.Is(err, ErrPkgNotSigned) && m.unsignedIsAllowed(p) {
				log.Printf("%s: %v (allowed)", p.Filename(), err)
				continue
			}
			log.Printf("%s: %v (removing)", p.Filename(), err)
			if err := m.RemovePackage(p); err != nil {
				return nil, fmt.Errorf("failed to remove package: %w", err)
			}
		default:
			log.Printf("%s: ok", p.Filename())
		}
	}

	if err := m.backend.LoadAll(); err != nil {
		return nil, fmt.Errorf("load package: %w", err)
	}

	return &m, nil
}

func (m *pkgManager) ListPackages() ([]pkg.Package, error) {
	var res []pkg.Package

	for p, err := range m.backend.List("") {
		if err != nil {
			return nil, err
		}
		res = append(res, *p)
	}

	return res, nil
}

func (m *pkgManager) InstallPackage(ctx context.Context, p pkg.Package) error {
	tmpdir, err := os.MkdirTemp(m.tmpdir, "edge-pkg-install-*")
	if err != nil {
		return fmt.Errorf("failed to create tempdir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	pkgfile := filepath.Join(tmpdir, p.Filename())
	sigfile := pkgfile + ".sum.sig"

	hasSig := true
	if err := m.clt.FetchPackage(ctx, &p, true, sigfile); err != nil {
		var herr *HTTPError
		if m.unsignedIsAllowed(p) &&
			errors.As(err, &herr) &&
			herr.Status == http.StatusNotFound {
			log.Printf("warning: failed to fetch signature for %s: %v", p.Filename(), err)
			hasSig = false
		} else {
			return fmt.Errorf("error fetching signature: %w", err)
		}
	}

	if err := m.clt.FetchPackage(ctx, &p, false, pkgfile); err != nil {
		return fmt.Errorf("error fetching package: %w", err)
	}

	rd, err := os.Open(pkgfile)
	if err != nil {
		return fmt.Errorf("fail to open package file: %w", err)
	}
	defer rd.Close()

	var sig []byte
	if hasSig {
		var err error
		sig, err = os.ReadFile(sigfile)
		if err != nil {
			return fmt.Errorf("fail to read signature file: %w", err)
		}
		if err := m.verifyPkgFile(p, rd, sig); err != nil {
			return fmt.Errorf("fail to verify package signature: %w", err)
		}
		if _, err := rd.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek: %w", err)
		}
	}

	return m.backend.Load(&p, rd, sig)
}

func (m *pkgManager) RemovePackage(p pkg.Package) error {
	return m.backend.Unload(&p)
}

func (m *pkgManager) HasPackage(p pkg.Package) (bool, error) {
	for e, err := range m.backend.List(p.Name) {
		if err != nil {
			return false, err
		}
		if p == *e {
			return true, nil
		}
	}
	return false, nil
}

func (m *pkgManager) VerifyPackage(p pkg.Package) error {
	sig, err := m.backend.Signature(&p)
	if err != nil {
		return err
	}

	if sig == nil {
		return ErrPkgNotSigned
	}

	filename := filepath.Join(m.intdir, p.Filename())
	rd, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer rd.Close()

	if err := m.verifyPkgFile(p, rd, sig); err != nil {
		return err
	}
	return nil
}

func (m *pkgManager) verifyPkgFile(p pkg.Package, rd io.Reader, signature []byte) error {
	if signature == nil {
		return ErrPkgNotSigned
	}

	artifact := pkg.Artifact{
		Filename:  p.Filename(),
		Origin:    pkg.LocalOrigin,
		Signature: signature,
	}

	if err := m.verifier.Verify(&artifact, rd); err != nil {
		return err
	}

	return nil
}

func (m *pkgManager) unsignedIsAllowed(p pkg.Package) bool {
	if slices.Contains(m.allowUnsigned, p.Name) {
		return true
	}
	if slices.Contains(m.allowUnsigned, fmt.Sprintf("%s@%s", p.Name, p.Version)) {
		return true
	}
	if slices.Contains(m.allowUnsigned, p.Filename()) {
		return true
	}
	return false
}
