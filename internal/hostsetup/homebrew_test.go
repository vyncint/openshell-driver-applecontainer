package hostsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomebrewCask(t *testing.T) {
	cases := []struct {
		path string
		cask string
		brew bool
	}{
		{"/opt/homebrew/Caskroom/openshell-driver-applecontainer/0.2.8/openshell-driver-applecontainer",
			"openshell-driver-applecontainer", true},
		// Intel prefix, and a version directory carrying a revision suffix.
		{"/usr/local/Caskroom/some-tool/1.2.3_1/some-tool", "some-tool", true},
		// Installed by install.sh: a real file in the brew prefix, not a cask.
		{"/opt/homebrew/bin/openshell-driver-applecontainer", "", false},
		{"/usr/local/bin/openshell-driver-applecontainer", "", false},
		// A directory merely named Caskroom, with nothing under it.
		{"/tmp/Caskroom", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		cask, brew := HomebrewCask(tc.path)
		if brew != tc.brew || cask != tc.cask {
			t.Errorf("HomebrewCask(%q) = (%q, %v), want (%q, %v)", tc.path, cask, brew, tc.cask, tc.brew)
		}
	}
}

// A Homebrew cask's bin symlink must survive into the launchd plist as-is:
// resolving it would pin the plist to a version directory that the next
// `brew upgrade` deletes.
func TestResolveBinPathKeepsHomebrewSymlink(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "Caskroom", "driver", "0.2.8")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(staged, "driver")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil { // #nosec G306 -- stand-in for an executable
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "driver")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if got := resolveBinPath(link); got != link {
		t.Errorf("resolveBinPath(%q) = %q, want the symlink itself", link, got)
	}
}

// Every other symlink is still resolved, so the plist does not depend on one
// staying put.
func TestResolveBinPathResolvesOtherSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real-driver")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil { // #nosec G306 -- stand-in for an executable
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-driver")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveBinPath(link); got != want {
		t.Errorf("resolveBinPath(%q) = %q, want %q", link, got, want)
	}
}

// A path that cannot be resolved (a dangling symlink, say) is returned as-is
// rather than turning setup into a hard failure.
func TestResolveBinPathUnresolvable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := resolveBinPath(missing); got != missing {
		t.Errorf("resolveBinPath(%q) = %q, want it unchanged", missing, got)
	}
}
