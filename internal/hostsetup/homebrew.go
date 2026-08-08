package hostsetup

import (
	"path/filepath"
	"strings"
)

// HomebrewCask reports whether binPath is a binary staged by a Homebrew cask,
// and the cask's token. Cask artifacts live at
// <brew-prefix>/Caskroom/<token>/<version>/<binary> and are symlinked into
// <brew-prefix>/bin, so the staged path is pinned to one version while the
// symlink is not.
func HomebrewCask(binPath string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(binPath), "/")
	for i, p := range parts {
		if p == "Caskroom" && i+1 < len(parts) && parts[i+1] != "" {
			return parts[i+1], true
		}
	}
	return "", false
}

// resolveBinPath canonicalises the running binary's path for the launchd plist.
// Symlinks are resolved so the plist does not depend on one staying put — with
// one exception: a Homebrew cask's symlink, which resolves into a version
// directory that the next `brew upgrade` deletes. Recording that would leave
// the service pointing at a path that no longer exists; the <prefix>/bin
// symlink outlives every upgrade, so keep it.
func resolveBinPath(exe string) string {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	if _, isCask := HomebrewCask(resolved); isCask {
		return exe
	}
	return resolved
}
