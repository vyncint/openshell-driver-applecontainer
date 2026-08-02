package seed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
)

const supImage = "ghcr.io/nvidia/openshell/supervisor:0.0.96"

func TestExtractorCachesByDigest(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddImage(supImage, "sha256:abcdef1234567890")
	ex := &Extractor{RT: fake, CacheDir: t.TempDir()}

	p1, err := ex.Ensure(context.Background(), supImage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p1, "abcdef123456") {
		t.Errorf("cache path lacks digest component: %s", p1)
	}
	info, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cached supervisor perms = %o, want 600 (read-only cache, never exec'd on host)", info.Mode().Perm())
	}
	firstRuns := len(fake.RunCalls())
	if firstRuns == 0 {
		t.Fatal("no extraction container was run")
	}

	// Second call is a pure cache hit: no new containers.
	p2, err := ex.Ensure(context.Background(), supImage)
	if err != nil {
		t.Fatal(err)
	}
	if p2 != p1 {
		t.Errorf("cache path changed: %s vs %s", p2, p1)
	}
	if got := len(fake.RunCalls()); got != firstRuns {
		t.Errorf("cache hit still ran %d extra containers", got-firstRuns)
	}
}

func TestExtractorPullsMissingImage(t *testing.T) {
	fake := &backend.Fake{}
	ex := &Extractor{RT: fake, CacheDir: t.TempDir()}
	if _, err := ex.Ensure(context.Background(), supImage); err != nil {
		t.Fatal(err)
	}
	if pulls := fake.Pulls(); len(pulls) != 1 || pulls[0] != supImage {
		t.Errorf("pulls = %v", pulls)
	}
}

func TestExtractorRejectsNonELF(t *testing.T) {
	fake := &backend.Fake{CopySrc: []byte("<html>registry error</html>")}
	fake.AddImage(supImage, "sha256:bad")
	ex := &Extractor{RT: fake, CacheDir: t.TempDir()}
	if _, err := ex.Ensure(context.Background(), supImage); err == nil || !strings.Contains(err.Error(), "ELF") {
		t.Errorf("want ELF validation error, got %v", err)
	}
}

func TestEnsureConcurrentColdCache(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddImage(supImage, "sha256:concurrent00")
	ex := &Extractor{RT: fake, CacheDir: t.TempDir(), Labels: map[string]string{"managed": "yes"}}

	const n = 8
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // maximize the race window
			paths[i], errs[i] = ex.Ensure(context.Background(), supImage)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Errorf("goroutine %d got %q, want %q", i, paths[i], paths[0])
		}
	}
	// The cached binary is the full expected content, not a truncated race
	// artifact, and no extraction container leaked.
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4 || string(data[:4]) != "\x7fELF" {
		t.Errorf("cached binary is not a valid ELF (%d bytes)", len(data))
	}
	list, err := fake.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("extraction container(s) leaked: %+v", list)
	}
}

func TestExtractionContainerCarriesLabels(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddImage(supImage, "sha256:labeltest0")
	ex := &Extractor{RT: fake, CacheDir: t.TempDir(), Labels: map[string]string{"openshell.ai/managed-by": "x"}}
	if _, err := ex.Ensure(context.Background(), supImage); err != nil {
		t.Fatal(err)
	}
	calls := fake.RunCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 run, got %d", len(calls))
	}
	if calls[0].Labels["openshell.ai/managed-by"] != "x" {
		t.Errorf("extraction container missing managed-by label: %+v", calls[0].Labels)
	}
	if !strings.HasPrefix(calls[0].Name, "oshl-extract-") {
		t.Errorf("extraction name = %q", calls[0].Name)
	}
}

func TestExtractionContainerCleanedUp(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddImage(supImage, "sha256:cleanup")
	ex := &Extractor{RT: fake, CacheDir: t.TempDir()}
	if _, err := ex.Ensure(context.Background(), supImage); err != nil {
		t.Fatal(err)
	}
	list, err := fake.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("extraction container leaked: %+v", list)
	}
}

func TestWriteSeedDir(t *testing.T) {
	src := t.TempDir()
	for name, content := range map[string]string{
		"sup":     "\x7fELFbinary",
		"ca.crt":  "ca",
		"tls.crt": "cert",
		"tls.key": "key",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "seed")
	err := Write(dir, Materials{
		SupervisorPath: filepath.Join(src, "sup"),
		CAPath:         filepath.Join(src, "ca.crt"),
		CertPath:       filepath.Join(src, "tls.crt"),
		KeyPath:        filepath.Join(src, "tls.key"),
		Token:          "tok",
	})
	if err != nil {
		t.Fatal(err)
	}

	boot, err := os.ReadFile(filepath.Join(dir, "boot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#!/bin/sh", "chmod 0755 /opt/openshell/bin/openshell-sandbox", "exec /opt/openshell/bin/openshell-sandbox"} {
		if !strings.Contains(string(boot), want) {
			t.Errorf("boot.sh missing %q", want)
		}
	}
	token, err := os.ReadFile(filepath.Join(dir, "auth", "sandbox.jwt"))
	if err != nil || string(token) != "tok\n" {
		t.Errorf("token = %q, %v", token, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("seed dir perms = %o, want 700", info.Mode().Perm())
	}
}

func TestWriteSeedDirWithoutToken(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{"sup", "ca.crt", "tls.crt", "tls.key"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "seed")
	err := Write(dir, Materials{
		SupervisorPath: filepath.Join(src, "sup"),
		CAPath:         filepath.Join(src, "ca.crt"),
		CertPath:       filepath.Join(src, "tls.crt"),
		KeyPath:        filepath.Join(src, "tls.key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth", "sandbox.jwt")); !os.IsNotExist(err) {
		t.Errorf("token file should not exist: %v", err)
	}
}
