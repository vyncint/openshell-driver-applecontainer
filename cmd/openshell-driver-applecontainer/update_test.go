package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseArchiveName(t *testing.T) {
	want := "openshell-driver-applecontainer_0.2.4_darwin_arm64.tar.gz"
	if got := releaseArchiveName("v0.2.4"); got != want {
		t.Errorf("with v prefix: got %q, want %q", got, want)
	}
	if got := releaseArchiveName("0.2.4"); got != want {
		t.Errorf("without v prefix: got %q, want %q", got, want)
	}
}

// makeTarGz writes a gzipped tar of name->content and returns its path.
func makeTarGz(t *testing.T, dir string, entries map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "archive.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	dir := t.TempDir()
	want := []byte("\x7fELF fake binary body")
	archive := makeTarGz(t, dir, map[string][]byte{
		"README.md":      []byte("docs"),
		updateBinaryName: want,
	})

	dest := filepath.Join(dir, "out")
	if err := extractBinaryFromTarGz(archive, updateBinaryName, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("extracted content mismatch")
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755", info.Mode().Perm())
	}
}

func TestExtractBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	archive := makeTarGz(t, dir, map[string][]byte{"other": []byte("x")})
	if err := extractBinaryFromTarGz(archive, updateBinaryName, filepath.Join(dir, "out")); err == nil {
		t.Error("expected an error when the binary is absent from the archive")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "app.tar.gz")
	body := []byte("release payload")
	if err := os.WriteFile(archive, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	checksums := []byte("deadbeef  other.tar.gz\n" + hex.EncodeToString(sum[:]) + "  app.tar.gz\n")

	if err := verifyChecksum(archive, checksums, "app.tar.gz"); err != nil {
		t.Errorf("a valid checksum should pass: %v", err)
	}
	if err := verifyChecksum(archive, []byte("00  app.tar.gz\n"), "app.tar.gz"); err == nil {
		t.Error("expected a mismatch error")
	}
	if err := verifyChecksum(archive, checksums, "missing.tar.gz"); err == nil {
		t.Error("expected an error when the archive is not listed")
	}
}
