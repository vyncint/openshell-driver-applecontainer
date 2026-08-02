package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortTempDir returns a temp dir with a short absolute path. t.TempDir()
// paths on macOS exceed the ~104-byte sun_path limit and fail to bind —
// the same constraint that keeps the production socket under /tmp/oshl-ac.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "oshl-t-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenUnixRejectsSymlinkedDir(t *testing.T) {
	base := shortTempDir(t)
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	// Socket dir would be the symlink itself: must be refused.
	_, err := listenUnix(filepath.Join(link, "driver.sock"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("want symlink rejection, got %v", err)
	}
}

func TestListenUnixPermissions(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "sockdir")
	path := filepath.Join(dir, "driver.sock")

	lis, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir perms = %o, want 700", perm)
	}
	si, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := si.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %o, want 600", perm)
	}
}

func TestListenUnixRejectsLiveSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "driver.sock")
	first, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()

	// Accept in the background so the probe dial succeeds.
	go func() {
		for {
			conn, err := first.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	if _, err := listenUnix(path); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("want in-use error, got %v", err)
	}
}

func TestListenUnixRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "driver.sock")
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// Close without unlinking: leaves a stale socket file behind.
	if err := lis.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("platform unlinked socket on close; stale case not reproducible")
	}

	second, err := listenUnix(path)
	if err != nil {
		t.Fatalf("stale socket not recovered: %v", err)
	}
	defer func() { _ = second.Close() }()
}
