package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	updateRepo       = "vyncint/openshell-driver-applecontainer"
	updateBinaryName = "openshell-driver-applecontainer"
	// maxArchiveBytes caps how much we read from a release archive, so a
	// corrupt or hostile download cannot exhaust memory/disk.
	maxArchiveBytes = 200 << 20 // 200 MiB
)

// runUpdate implements `openshell-driver-applecontainer update`: replace this
// binary with a newer release (verifying its checksum) and re-run setup so the
// service restarts on it. With --all it also updates the prerequisites.
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	targetVersion := fs.String("version", "", "install a specific release (e.g. v0.2.4); default: latest")
	noSetup := fs.Bool("no-setup", false, "replace the binary but do not re-run setup")
	all := fs.Bool("all", false, "also update the prerequisites: OpenShell (brew) and apple/container")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := newLogger("info")

	want := *targetVersion
	if want == "" {
		latest, err := latestReleaseTag(updateRepo)
		if err != nil {
			log.Error("update: could not determine the latest release; pass --version", "err", err)
			return 1
		}
		want = latest
	}

	log.Info("update: driver", "current", version, "target", want)
	if want == version {
		log.Info("update: already on the requested version; re-applying setup", "version", version)
	} else if err := selfUpdate(log, want); err != nil {
		log.Error("update failed", "err", err)
		return 1
	}

	if *all {
		updatePrerequisites(log)
	}

	if *noSetup {
		log.Info("update: skipping setup (--no-setup); run `" + updateBinaryName + " setup` to restart the service")
		return 0
	}
	self, err := currentBinaryPath()
	if err != nil {
		log.Error("update: locate binary for re-setup", "err", err)
		return 1
	}
	log.Info("update: re-running setup to restart the service on the new binary")
	if err := streamCmd(self, "setup"); err != nil {
		log.Error("update: setup after update failed; run `"+updateBinaryName+" setup` yourself", "err", err)
		return 1
	}
	return 0
}

// selfUpdate downloads release `version`, verifies its checksum, and replaces
// the running binary in place.
func selfUpdate(log *slog.Logger, version string) error {
	tmp, err := os.MkdirTemp("", "oshl-ac-update-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	archive := releaseArchiveName(version)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", updateRepo, version)
	archivePath := filepath.Join(tmp, archive)

	log.Info("update: downloading", "archive", archive)
	if err := downloadTo(base+"/"+archive, archivePath); err != nil {
		return fmt.Errorf("download %s: %w", archive, err)
	}
	sums, err := httpGetBytes(base + "/checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyChecksum(archivePath, sums, archive); err != nil {
		return err
	}
	log.Info("update: checksum verified")

	binPath, err := currentBinaryPath()
	if err != nil {
		return err
	}
	extracted := filepath.Join(tmp, updateBinaryName)
	if err := extractBinaryFromTarGz(archivePath, updateBinaryName, extracted); err != nil {
		return err
	}
	if err := replaceBinary(binPath, extracted); err != nil {
		return fmt.Errorf("replace %s: %w", binPath, err)
	}
	// The release binary is unsigned; clear quarantine so it runs.
	_ = exec.Command("xattr", "-d", "com.apple.quarantine", binPath).Run()
	return nil
}

// releaseArchiveName is the goreleaser asset name for a darwin/arm64 build.
func releaseArchiveName(version string) string {
	return fmt.Sprintf("%s_%s_darwin_arm64.tar.gz", updateBinaryName, strings.TrimPrefix(version, "v"))
}

// verifyChecksum confirms archivePath matches its sha256 line in a
// `sha256sum`-format checksums file (each line "<hex>  <name>").
func verifyChecksum(archivePath string, checksums []byte, archiveName string) error {
	var want string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archiveName {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum listed for %s", archiveName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxArchiveBytes)); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", archiveName, got, want)
	}
	return nil
}

// extractBinaryFromTarGz writes the archive member whose base name is
// binaryName to dest (mode 0755).
func extractBinaryFromTarGz(archivePath, binaryName, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", binaryName)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binaryName {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) // #nosec G302 -- the extracted driver binary must carry the exec bit
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, maxArchiveBytes)); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
}

// replaceBinary atomically swaps target for newBin. It first tries a rename
// within target's directory (works while the old binary is still running on
// macOS); if that directory is not writable it falls back to `sudo install`,
// attached to the terminal so it can prompt for a password.
func replaceBinary(target, newBin string) error {
	dir := filepath.Dir(target)
	if tmp, err := os.CreateTemp(dir, ".oshl-ac-update-*"); err == nil {
		tmpName := tmp.Name()
		_ = tmp.Close()
		if copyErr := copyFile(newBin, tmpName, 0o755); copyErr == nil {
			if renErr := os.Rename(tmpName, target); renErr == nil {
				return nil
			}
		}
		_ = os.Remove(tmpName)
	}
	// Not writable (e.g. a root-owned prefix): use sudo. install(1) sets the
	// mode and works across filesystems.
	return streamCmd("sudo", "install", "-m", "0755", newBin, target)
}

// updatePrerequisites updates OpenShell (brew) and apple/container (via its
// own installed updater). Best-effort and terminal-attached (brew output,
// sudo prompts).
func updatePrerequisites(log *slog.Logger) {
	if _, err := exec.LookPath("brew"); err == nil {
		log.Info("update: upgrading OpenShell (brew)")
		if err := streamCmd("brew", "upgrade", "openshell"); err != nil {
			log.Warn("brew upgrade openshell failed (it may already be current)", "err", err)
		}
	}
	const acUpdater = "/usr/local/bin/update-container.sh"
	if _, err := os.Stat(acUpdater); err == nil {
		// The updater refuses to run while the runtime is up ("`container` is
		// still running"), so stop it first — same as the uninstall path does.
		if err := streamCmd("container", "system", "stop"); err != nil {
			log.Debug("container system stop", "err", err)
		}
		log.Info("update: updating apple/container (its updater needs sudo)")
		if err := streamCmd(acUpdater); err != nil {
			log.Warn("apple/container updater failed", "err", err)
		}
		// Bring the runtime back up; the driver needs it. (setup would also
		// start it, but --no-setup must not leave it stopped.)
		if err := streamCmd("container", "system", "start"); err != nil {
			log.Warn("could not restart the container runtime; run `container system start`", "err", err)
		}
	}
}

// currentBinaryPath is the real (symlink-resolved) path of the running binary.
func currentBinaryPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		return resolved, nil
	}
	return self, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// OpenFile's mode is ignored when dst already exists — and it does here:
	// replaceBinary copies into an os.CreateTemp file (created 0600). Force
	// the mode so the replaced binary keeps its exec bit.
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func streamCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// --- GitHub release lookup / download ---

var httpClient = &http.Client{Timeout: 5 * time.Minute}

func latestReleaseTag(repo string) (string, error) {
	body, err := httpGetBytes("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("parse release metadata: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release")
	}
	return rel.TagName, nil
}

func httpGetBytes(url string) ([]byte, error) {
	resp, err := httpGet(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes))
}

func downloadTo(url, dest string) error {
	resp, err := httpGet(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(resp.Body, maxArchiveBytes)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func httpGet(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", updateBinaryName)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}
