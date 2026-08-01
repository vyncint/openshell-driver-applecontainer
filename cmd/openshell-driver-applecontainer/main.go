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
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("openshell-driver-applecontainer %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	if err := run(os.Args[1:]); err != nil {
		slog.Error("driver exited with error", "err", err)
		os.Exit(1)
	}
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
	srv := grpcsvc.New(cfg, rt, store, log, version)

	// Fail fast on configuration that can never work; reconcile persisted
	// records against the runtime before serving; then keep conditions
	// fresh with the runtime poller.
	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 30*time.Second)
	err = grpcsvc.Preflight(bootCtx, cfg, rt, log)
	if err == nil {
		err = srv.Bootstrap(bootCtx)
		if err != nil {
			err = fmt.Errorf("startup reconcile: %w", err)
		}
	}
	cancelBoot()
	if err != nil {
		return err
	}
	srv.StartPoller()

	lis, err := listenUnix(cfg.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg.Socket) }()

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

// listenUnix binds the driver socket with owner-only permissions: parent
// directory 0700, socket 0600. A live socket (another driver instance)
// aborts startup; a stale file is removed.
func listenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("socket %s is in use by another driver instance", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat socket: %w", err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("restrict socket: %w", err)
	}
	return lis, nil
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
