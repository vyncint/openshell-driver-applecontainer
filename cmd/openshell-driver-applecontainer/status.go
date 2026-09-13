package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	"github.com/vyncint/openshell-driver-applecontainer/internal/hostsetup"
)

// runStatus implements `openshell-driver-applecontainer status`: a read-only
// health report of the whole stack, one line per component, exit 0 when
// everything is healthy, 1 when something needs attention.
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the checks as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	defaults, err := config.Parse(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status: resolve defaults:", err)
		return 1
	}
	log := newLogger("error") // findings are the output; keep the log quiet
	defaults.ResolveSupervisorImage(log)

	s, err := hostsetup.New(backend.NewCLI(log), log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	checks := s.Status(ctx, hostsetup.StatusOptions{
		Network:         defaults.Network,
		Socket:          defaults.Socket,
		SupervisorImage: defaults.SupervisorImage,
		DriverVersion:   version,
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Driver string            `json:"driver_version"`
			Status string            `json:"status"`
			Checks []hostsetup.Check `json:"checks"`
		}{version, hostsetup.Worst(checks), checks})
	} else {
		fmt.Printf("openshell-driver-applecontainer %s\n\n", version)
		hostsetup.PrintChecks(os.Stdout, checks)
	}
	if hostsetup.Worst(checks) == "fail" {
		return 1
	}
	return 0
}

// runLogs implements `openshell-driver-applecontainer logs [-n N] [-f]`:
// the driver service's log, which is where every failed sandbox explains
// itself. Saves users from remembering the ~/Library/Logs path.
func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	lines := fs.Int("n", 50, "number of trailing lines to show")
	follow := fs.Bool("f", false, "keep printing new lines as they arrive")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := hostsetup.DriverLogPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "logs:", err)
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logs: %v\n  (the service writes it once `setup` has installed the driver as a launchd agent)\n", err)
		return 1
	}
	defer func() { _ = f.Close() }()

	offset, err := tailLines(f, os.Stdout, *lines)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logs:", err)
		return 1
	}
	if !*follow {
		return 0
	}
	for {
		time.Sleep(500 * time.Millisecond)
		info, err := f.Stat()
		if err != nil {
			return 1
		}
		if info.Size() < offset { // rotated/truncated: start over
			offset = 0
		}
		if info.Size() == offset {
			continue
		}
		n, err := io.Copy(os.Stdout, io.NewSectionReader(f, offset, info.Size()-offset))
		offset += n
		if err != nil {
			return 1
		}
	}
}

// tailLines writes the last n lines of f to w and returns the file size it
// read up to, so a follower can continue from there.
func tailLines(f *os.File, w io.Writer, n int) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	// Read a bounded window from the end; logs lines are short, and a
	// 1 MiB window comfortably covers 50 lines of anything the driver logs.
	const window = 1 << 20
	start := size - window
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0, err
	}
	text := string(buf)
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if start > 0 && len(parts) > 0 {
		parts = parts[1:] // the first line is probably cut in half
	}
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	if len(parts) > 1 || (len(parts) == 1 && parts[0] != "") {
		if _, err := io.WriteString(w, strings.Join(parts, "\n")+"\n"); err != nil {
			return 0, err
		}
	}
	return size, nil
}
