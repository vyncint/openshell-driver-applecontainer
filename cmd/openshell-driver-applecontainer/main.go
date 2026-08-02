// openshell-driver-applecontainer is an out-of-tree OpenShell compute driver
// that provisions each sandbox as an apple/container micro-VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/grpcsvc"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

// Injected via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "--version", "version":
			fmt.Printf("openshell-driver-applecontainer %s (commit %s, built %s)\n", version, commit, date)
			return
		case "setup":
			os.Exit(runSetup(args[1:]))
		case "uninstall":
			os.Exit(runUninstall(args[1:]))
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}
	if err := run(args); err != nil {
		slog.Error("driver exited with error", "err", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`openshell-driver-applecontainer — OpenShell compute driver backed by apple/container

Usage:
  openshell-driver-applecontainer setup [--no-pull] [--network NAME]
        One-time host setup: installs the driver as a launchd service,
        configures the OpenShell gateway service to use it, ensures the
        vmnet network and gateway certificate, and pre-pulls images.
        Idempotent — re-run any time to repair the installation.

  openshell-driver-applecontainer uninstall
        Removes the services and configuration that setup installed.

  openshell-driver-applecontainer [flags]
        Runs the driver in the foreground (development). See -h of the
        bare command for flags; with no flags everything is derived:
        the vmnet network is created and the gateway endpoint resolved
        automatically.

  openshell-driver-applecontainer --version
`)
}

func run(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	store, err := state.NewStore(cfg.StateDir)
	if err != nil {
		return err
	}
	rt := backend.NewCLI(log)

	// Fail fast on configuration that can never work and complete the
	// zero-config defaults (derived endpoint, auto-created network) BEFORE
	// the server captures the config; then reconcile persisted records
	// against the runtime and keep conditions fresh with the poller.
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 60*time.Second)
	if err := grpcsvc.Preflight(bootCtx, &cfg, rt, log); err != nil {
		cancelBoot()
		return err
	}
	srv := grpcsvc.New(cfg, rt, store, log, version)
	err = srv.Bootstrap(bootCtx)
	cancelBoot()
	if err != nil {
		return fmt.Errorf("startup reconcile: %w", err)
	}
	srv.StartPoller()

	lis, sockID, err := listenUnix(cfg.Socket)
	if err != nil {
		return err
	}
	defer removeSocketIfOwned(cfg.Socket, sockID, log)

	gs := grpc.NewServer()
	computev1.RegisterComputeDriverServer(gs, srv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- gs.Serve(lis) }()

	log.Info("driver listening",
		"socket", cfg.Socket,
		"version", version,
		"network", cfg.Network,
		"state_dir", cfg.StateDir,
		"default_image", cfg.DefaultImage,
	)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: drain in-flight RPCs, then force-stop stragglers
	// (long-lived watch streams keep GracefulStop from returning).
	log.Info("shutting down, draining in-flight RPCs")
	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn("graceful drain timed out, forcing stop")
		gs.Stop()
	}
	srv.Close()
	return nil
}

// socketIdentity is a bound socket file's device+inode, captured right
// after binding so shutdown can verify the path still refers to the same
// file before unlinking it. A plain comparable struct.
type socketIdentity struct {
	dev uint64
	ino uint64
}

// statSocketIdentity reads path's current device+inode.
func statSocketIdentity(path string) (socketIdentity, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return socketIdentity{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return socketIdentity{}, fmt.Errorf("cannot determine device/inode for %s", path)
	}
	return socketIdentity{dev: uint64(st.Dev), ino: st.Ino}, nil // #nosec G115 -- Dev is platform-signed but only ever used as an opaque identity, never arithmetic
}

// removeSocketIfOwned unlinks path only if it still refers to the socket
// file this process bound (matching device+inode). A canceled/short-lived
// process can be draining in-flight RPCs for up to 10s at shutdown
// (see run()); if a replacement instance bootstraps and binds the same
// path during that window, this process's own deferred cleanup must not
// delete the replacement's live socket out from under it — that would
// leave the path unreachable even though the new process is healthy.
func removeSocketIfOwned(path string, want socketIdentity, log *slog.Logger) {
	got, err := statSocketIdentity(path)
	if err != nil {
		return // already gone or unreadable; nothing more to safely do
	}
	if got != want {
		log.Warn("socket path was replaced by a newer instance; leaving it in place", "socket", path)
		return
	}
	if err := os.Remove(path); err != nil {
		log.Warn("failed to remove socket", "socket", path, "err", err)
	}
}

// listenUnix binds the driver socket with owner-only permissions: parent
// directory 0700, socket 0600. A live socket (another driver instance)
// aborts startup; a stale file is removed. The default socket lives under a
// world-writable /tmp, so the directory is verified to be a real directory
// owned by us and not a symlink before we trust it. The returned identity
// lets the caller safely remove the socket later — see removeSocketIfOwned.
func listenUnix(path string) (net.Listener, socketIdentity, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, socketIdentity{}, fmt.Errorf("create socket dir: %w", err)
	}
	if err := verifyOwnedDir(dir); err != nil {
		return nil, socketIdentity{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- 0700 on a directory: owner needs the traverse/exec bit
		return nil, socketIdentity{}, fmt.Errorf("restrict socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, socketIdentity{}, fmt.Errorf("socket %s is in use by another driver instance", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, socketIdentity{}, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, socketIdentity{}, fmt.Errorf("stat socket: %w", err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, socketIdentity{}, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, socketIdentity{}, fmt.Errorf("restrict socket: %w", err)
	}
	id, err := statSocketIdentity(path)
	if err != nil {
		_ = lis.Close()
		return nil, socketIdentity{}, fmt.Errorf("stat bound socket: %w", err)
	}
	return lis, id, nil
}

// verifyOwnedDir rejects a socket directory that is a symlink or is not a
// directory owned by the current user — the shared-/tmp attack surface
// where another local user could pre-plant it to redirect or DoS the driver.
func verifyOwnedDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat socket dir: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("socket dir %s is a symlink; refusing to use it", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket dir %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("socket dir %s is owned by uid %d, not the current user %d; refusing to use it", dir, st.Uid, os.Getuid())
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
