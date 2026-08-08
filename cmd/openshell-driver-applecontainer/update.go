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

	"github.com/vyncint/openshell-driver-applecontainer/internal/hostsetup"
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
	targetVersion := fs.String("version", "", "install a specific driver release (e.g. v0.2.4); default: latest")
	noSetup := fs.Bool("no-setup", false, "replace the binary but do not re-run setup")
	all := fs.Bool("all", false, "also update the prerequisites: OpenShell (brew) and apple/container")
	openshellVersion := fs.String("openshell-version", "", "with --all: pin OpenShell to this release (e.g. 0.0.97); default: its latest")
	containerVersion := fs.String("container-version", "", "with --all: pin apple/container to this release (e.g. 1.2.0); default: its latest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*all && (*openshellVersion != "" || *containerVersion != "") {
		slog.Error("update: --openshell-version/--container-version only apply with --all")
		return 2
	}
	log := newLogger("info")

	self, err := currentBinaryPath()
	if err != nil {
		log.Error("update: locate the running binary", "err", err)
		return 1
	}
	// Re-running setup goes through the launch path, not the resolved one: a
	// brew upgrade replaces the Caskroom version directory, so the resolved
	// path is gone by the time setup runs. The symlink in <prefix>/bin is not.
	setupPath := self

	// Replacing a cask's staged binary in place would leave Homebrew believing
	// it still has the version it staged, so let brew do the upgrade.
	if cask, brewManaged := hostsetup.HomebrewCask(self); brewManaged {
		if *targetVersion != "" {
			log.Error("update: this install is managed by Homebrew, which only tracks the tap's latest release. "+
				"To pin a version, remove it (`brew uninstall --cask "+cask+"`) and install with install.sh",
				"requested", *targetVersion)
			return 2
		}
		log.Info("update: this install is managed by Homebrew; upgrading through brew", "cask", cask)
		if err := streamCmd("brew", "upgrade", "--cask", cask); err != nil {
			log.Warn("brew upgrade failed (the cask may already be current)", "cask", cask, "err", err)
		}
		if p, execErr := os.Executable(); execErr == nil {
			setupPath = p
		}
	} else {
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
		} else if err := selfUpdate(log, self, want); err != nil {
			log.Error("update failed", "err", err)
			return 1
		}
	}

	if *all {
		updatePrerequisites(log, *openshellVersion, *containerVersion)
	}

	if *noSetup {
		log.Info("update: skipping setup (--no-setup); run `" + updateBinaryName + " setup` to restart the service")
		return 0
	}
	log.Info("update: re-running setup to restart the service on the new binary")
	if err := streamCmd(setupPath, "setup"); err != nil {
		log.Error("update: setup after update failed; run `"+updateBinaryName+" setup` yourself", "err", err)
		return 1
	}
	return 0
}

// selfUpdate downloads release `version`, verifies its checksum, and replaces
// binPath (the running binary) in place.
func selfUpdate(log *slog.Logger, binPath, version string) error {
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

// updatePrerequisites updates OpenShell and apple/container. Best-effort and
// terminal-attached (brew output, sudo prompts). Empty version strings mean
// "latest"; a pinned version reproduces an exact stack (or rolls one back).
func updatePrerequisites(log *slog.Logger, openshellVersion, containerVersion string) {
	if openshellVersion != "" {
		// brew cannot install an arbitrary tap version, so pinning goes
		// through OpenShell's own installer, which honors OPENSHELL_VERSION.
		log.Info("update: installing the pinned OpenShell release", "version", openshellVersion)
		if err := runOpenShellInstaller(openshellVersion); err != nil {
			log.Warn("pinned OpenShell install failed", "version", openshellVersion, "err", err)
		}
	} else if _, err := exec.LookPath("brew"); err == nil {
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
		acArgs := []string{}
		if containerVersion != "" {
			acArgs = append(acArgs, "-v", containerVersion)
		}
		log.Info("update: updating apple/container (its updater needs sudo)",
			"version", orLatest(containerVersion))
		if err := streamCmd(acUpdater, acArgs...); err != nil {
			log.Warn("apple/container updater failed", "err", err)
		}
		// Bring the runtime back up; the driver needs it. (setup would also
		// start it, but --no-setup must not leave it stopped.)
		if err := streamCmd("container", "system", "start"); err != nil {
			log.Warn("could not restart the container runtime; run `container system start`", "err", err)
		}
	}
}

// openShellInstallURL is OpenShell's official installer; it honors
// OPENSHELL_VERSION to select a release.
const openShellInstallURL = "https://raw.githubusercontent.com/NVIDIA/OpenShell/main/install.sh"

// runOpenShellInstaller installs a specific OpenShell release. The script is
// downloaded to a file and executed with `sh <file>` rather than piped straight
// into a shell, so a truncated download cannot execute as a partial script.
// Its gateway health-check cannot pass until setup runs afterwards, so a
// non-zero exit is expected and not treated as failure — the caller re-runs
// setup, and the version probe afterwards reveals what actually landed.
func runOpenShellInstaller(version string) error {
	tmp, err := os.MkdirTemp("", "oshl-installer-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	script := filepath.Join(tmp, "install.sh")
	if err := downloadTo(openShellInstallURL, script); err != nil {
		return fmt.Errorf("download the OpenShell installer: %w", err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "OPENSHELL_VERSION="+version)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	_ = cmd.Run() // expected non-zero: the gateway has no driver until setup
	if _, err := exec.LookPath("openshell"); err != nil {
		return fmt.Errorf("openshell binary not present after install: %w", err)
	}
	return nil
}

func orLatest(v string) string {
	if v == "" {
		return "latest"
	}
	return v
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
