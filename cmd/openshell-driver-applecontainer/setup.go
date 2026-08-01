package main

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	"github.com/vyncint/openshell-driver-applecontainer/internal/hostsetup"
)

// runSetup implements `openshell-driver-applecontainer setup`: one
// idempotent command that leaves the driver and the OpenShell gateway
// running as services, correctly wired, surviving reboots.
func runSetup(args []string) int {
	defaults, err := config.Parse(nil)
	if err != nil {
		slog.Error("resolve defaults", "err", err)
		return 1
	}

	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	network := fs.String("network", defaults.Network, "vmnet network for sandbox VMs")
	socket := fs.String("socket", defaults.Socket, "driver unix socket path")
	tlsDir := fs.String("tls-dir", "", "gateway TLS bundle directory (auto-detected when empty)")
	defaultImage := fs.String("default-image", defaults.DefaultImage, "sandbox image to pre-pull")
	supervisorImage := fs.String("supervisor-image", defaults.SupervisorImage, "supervisor image to pre-pull")
	noPull := fs.Bool("no-pull", false, "skip pre-pulling images (the first create pulls them instead)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	log := newLogger("info")
	s, err := hostsetup.New(backend.NewCLI(log), log)
	if err != nil {
		log.Error("setup failed", "err", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := s.Run(ctx, hostsetup.Options{
		Network:         *network,
		Socket:          *socket,
		TLSDir:          *tlsDir,
		DefaultImage:    *defaultImage,
		SupervisorImage: *supervisorImage,
		PullImages:      !*noPull,
	}); err != nil {
		log.Error("setup failed", "err", err)
		return 1
	}
	return 0
}

// runUninstall implements `openshell-driver-applecontainer uninstall`.
func runUninstall(_ []string) int {
	log := newLogger("info")
	s, err := hostsetup.New(backend.NewCLI(log), log)
	if err != nil {
		log.Error("uninstall failed", "err", err)
		return 1
	}
	if err := s.Uninstall(); err != nil {
		log.Error("uninstall failed", "err", err)
		return 1
	}
	return 0
}
