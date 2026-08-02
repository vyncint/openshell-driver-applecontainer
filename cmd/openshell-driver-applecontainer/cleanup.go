package main

import (
	"flag"
	"log/slog"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	"github.com/vyncint/openshell-driver-applecontainer/internal/hostsetup"
)

// runCleanup implements `openshell-driver-applecontainer cleanup`: reverse
// setup with apple/container-style layering. Bare command removes only the
// driver's service and gateway wiring; -d/--delete-data also removes the
// driver's data; --all also removes the OpenShell and apple/container
// prerequisites.
func runCleanup(args []string) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	keep := fs.Bool("keep-data", false, "keep the driver's data — state, network, images (the default)")
	fs.BoolVar(keep, "k", false, "shorthand for --keep-data")
	del := fs.Bool("delete-data", false, "also remove the driver's data: state, socket, vmnet network, pulled images")
	fs.BoolVar(del, "d", false, "shorthand for --delete-data")
	all := fs.Bool("all", false, "also remove the prerequisites: OpenShell (brew) and apple/container (its uninstaller)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *keep && *del {
		slog.Error("cleanup: pass at most one of -k/--keep-data and -d/--delete-data")
		return 2
	}

	defaults, err := config.Parse(nil)
	if err != nil {
		slog.Error("resolve defaults", "err", err)
		return 1
	}
	log := newLogger("info")
	s, err := hostsetup.New(backend.NewCLI(log), log)
	if err != nil {
		log.Error("cleanup failed", "err", err)
		return 1
	}
	if err := s.Cleanup(hostsetup.CleanupOptions{
		DeleteData:      *del,
		All:             *all,
		Network:         defaults.Network,
		Socket:          defaults.Socket,
		StateDir:        defaults.StateDir,
		DefaultImage:    defaults.DefaultImage,
		SupervisorImage: defaults.SupervisorImage,
	}); err != nil {
		log.Error("cleanup failed", "err", err)
		return 1
	}
	return 0
}
