// Package seed prepares everything a sandbox VM needs at boot: the
// openshell-sandbox supervisor binary (extracted from the release-matched
// supervisor image and cached), the gateway TLS material, the per-sandbox
// token, and the boot shim that starts the supervisor inside the guest.
package seed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
)

// GuestSeedDir is where the per-sandbox seed directory is mounted read-only
// inside the guest. User mounts may not target it.
const GuestSeedDir = "/openshell-seed"

// SupervisorImagePath is the well-known binary location inside the
// supervisor image.
const SupervisorImagePath = "/openshell-sandbox"

// bootScript copies the supervisor to a writable path and restores the
// executable bit before exec'ing it: `container cp` drops the bit, and the
// copy also keeps working if seed mounts ever gain noexec semantics.
const bootScript = `#!/bin/sh
set -eu
mkdir -p /opt/openshell/bin /run/openshell
cp /openshell-seed/openshell-sandbox /opt/openshell/bin/openshell-sandbox
chmod 0755 /opt/openshell/bin/openshell-sandbox
exec /opt/openshell/bin/openshell-sandbox
`

// Extractor extracts the supervisor binary out of the supervisor image and
// caches it on the host, keyed by image reference and digest — the digest
// pins content, the reference keeps distinct tags apart (mirroring the
// upstream invalidation rule of image identity plus OpenShell version).
type Extractor struct {
	RT       backend.Runtime
	CacheDir string
	Log      *slog.Logger
}

// sanitizeRef makes an image reference safe as a directory name component.
func sanitizeRef(ref string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, ref)
}

func shortDigest(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	if d == "" {
		d = "nodigest"
	}
	return d
}

// Ensure returns the host path of the cached supervisor binary, extracting
// it first when the cache misses.
func (e *Extractor) Ensure(ctx context.Context, image string) (string, error) {
	digest, err := e.ensureImage(ctx, image)
	if err != nil {
		return "", err
	}
	key := sanitizeRef(image) + "-" + shortDigest(digest)
	binPath := filepath.Join(e.CacheDir, key, "openshell-sandbox")
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o700); err != nil {
		return "", fmt.Errorf("seed: create cache dir: %w", err)
	}

	// The binary can only be copied out of a RUNNING container, so boot the
	// image with a harmless entrypoint, copy, and always tear down.
	tmpName := "oshl-extract-" + shortDigest(digest)
	_ = e.RT.Delete(ctx, tmpName) // clear any leftover from a crashed run
	if _, err := e.RT.Run(ctx, backend.RunSpec{
		Name:       tmpName,
		Image:      image,
		Entrypoint: "/bin/sleep",
		Args:       []string{"300"},
	}); err != nil {
		return "", fmt.Errorf("seed: start extraction container: %w", err)
	}
	defer func() {
		if derr := e.RT.Delete(context.WithoutCancel(ctx), tmpName); derr != nil && !errors.Is(derr, backend.ErrNotFound) {
			if e.Log != nil {
				e.Log.Warn("failed to remove extraction container", "name", tmpName, "err", derr)
			}
		}
	}()

	tmpFile := binPath + ".partial"
	if err := e.RT.CopyFrom(ctx, tmpName, SupervisorImagePath, tmpFile); err != nil {
		return "", fmt.Errorf("seed: copy supervisor from image: %w", err)
	}
	if err := validateELF(tmpFile); err != nil {
		_ = os.Remove(tmpFile)
		return "", err
	}
	if err := os.Chmod(tmpFile, 0o755); err != nil {
		return "", fmt.Errorf("seed: chmod supervisor: %w", err)
	}
	if err := os.Rename(tmpFile, binPath); err != nil {
		return "", fmt.Errorf("seed: persist supervisor: %w", err)
	}
	if e.Log != nil {
		e.Log.Info("supervisor binary cached", "image", image, "digest", digest, "path", binPath)
	}
	return binPath, nil
}

// ensureImage makes the image available locally and returns its digest.
func (e *Extractor) ensureImage(ctx context.Context, image string) (string, error) {
	find := func() (string, bool, error) {
		images, err := e.RT.ImageList(ctx)
		if err != nil {
			return "", false, err
		}
		for _, img := range images {
			if img.Reference == image {
				return img.Digest, true, nil
			}
		}
		return "", false, nil
	}
	digest, ok, err := find()
	if err != nil {
		return "", fmt.Errorf("seed: list images: %w", err)
	}
	if ok {
		return digest, nil
	}
	if err := e.RT.ImagePull(ctx, image, "linux/arm64"); err != nil {
		return "", fmt.Errorf("seed: pull supervisor image: %w", err)
	}
	digest, ok, err = find()
	if err != nil {
		return "", fmt.Errorf("seed: list images after pull: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("seed: image %q not present after pull", image)
	}
	return digest, nil
}

// validateELF rejects extraction results that are not Linux ELF binaries
// (e.g. a registry error page).
func validateELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("seed: open extracted binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("seed: read extracted binary: %w", err)
	}
	if magic[0] != 0x7f || magic[1] != 'E' || magic[2] != 'L' || magic[3] != 'F' {
		return fmt.Errorf("seed: extracted supervisor is not an ELF binary")
	}
	return nil
}

// Materials is everything that goes into one sandbox's seed directory.
type Materials struct {
	SupervisorPath string // host path of the cached supervisor binary
	CAPath         string // gateway CA certificate
	CertPath       string // shared client certificate
	KeyPath        string // shared client key
	Token          string // per-sandbox JWT; may be empty
}

// Write populates dir (created 0700) with the seed layout:
//
//	openshell-sandbox   supervisor binary (0755)
//	boot.sh             boot shim (0755)
//	tls/ca.crt tls/tls.crt (0644), tls/tls.key (0600)
//	auth/sandbox.jwt    token file (0600), only when a token is present
func Write(dir string, m Materials) error {
	for _, d := range []string{dir, filepath.Join(dir, "tls"), filepath.Join(dir, "auth")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("seed: create %s: %w", d, err)
		}
	}
	if err := copyFile(m.SupervisorPath, filepath.Join(dir, "openshell-sandbox"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "boot.sh"), []byte(bootScript), 0o755); err != nil {
		return fmt.Errorf("seed: write boot.sh: %w", err)
	}
	if err := copyFile(m.CAPath, filepath.Join(dir, "tls", "ca.crt"), 0o644); err != nil {
		return err
	}
	if err := copyFile(m.CertPath, filepath.Join(dir, "tls", "tls.crt"), 0o644); err != nil {
		return err
	}
	if err := copyFile(m.KeyPath, filepath.Join(dir, "tls", "tls.key"), 0o600); err != nil {
		return err
	}
	if m.Token != "" {
		if err := os.WriteFile(filepath.Join(dir, "auth", "sandbox.jwt"), []byte(m.Token+"\n"), 0o600); err != nil {
			return fmt.Errorf("seed: write token: %w", err)
		}
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("seed: read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return fmt.Errorf("seed: write %s: %w", dst, err)
	}
	if err := os.Chmod(dst, perm); err != nil {
		return fmt.Errorf("seed: chmod %s: %w", dst, err)
	}
	return nil
}
