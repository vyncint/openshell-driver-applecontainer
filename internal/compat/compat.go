// Package compat is the single place that knows which upstream versions this
// driver was verified against and which ones carry known problems. Setup,
// preflight and `status` all consult it, so the advice a user sees is the
// same everywhere.
package compat

import (
	"fmt"
	"strconv"
	"strings"
)

// Verified upstream ranges. The maxima are the newest releases the live
// acceptance suite (docs/acceptance.md) was run against; newer releases are
// expected to work (the contract is additive) but have not been exercised.
const (
	OpenShellMin = "0.0.96"
	OpenShellMax = "0.0.116"

	AppleContainerMin = "1.2.0"
	AppleContainerMax = "1.4.1"

	// AppleContainerMinSafe is the first apple/container release without the
	// Containerization advisories fixed in 1.3.1 (path traversal on
	// container/image ids, unvalidated OCI digests, symlink reads while
	// loading image layouts, unvalidated WWW-Authenticate realm host —
	// GHSA-x7pf-2jmj-pgcq, GHSA-f689-h8m7-3jp2, GHSA-r3h2-rgqf-9hv9,
	// GHSA-mx96-5vvg-x2mg / CVE-2026-65388, and two crashers). The driver
	// pulls and unpacks registry images through that code on every create,
	// so an older runtime is a real exposure, not a paperwork gap.
	AppleContainerMinSafe = "1.3.1"
)

// Version is a parsed X.Y.Z release number.
type Version struct{ Major, Minor, Patch int }

// Parse accepts "1.3.0", "v1.3.0" and the same with surrounding parentheses;
// anything else is an error.
func Parse(s string) (Version, error) {
	s = strings.TrimPrefix(strings.Trim(strings.TrimSpace(s), "()"), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("not an X.Y.Z version: %q", s)
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("not an X.Y.Z version: %q", s)
		}
		out[i] = n
	}
	return Version{out[0], out[1], out[2]}, nil
}

// MustParse is Parse for the package constants.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Compare returns -1, 0 or 1.
func Compare(a, b Version) int {
	switch {
	case a.Major != b.Major:
		return sign(a.Major - b.Major)
	case a.Minor != b.Minor:
		return sign(a.Minor - b.Minor)
	default:
		return sign(a.Patch - b.Patch)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Less reports a < b.
func Less(a, b Version) bool { return Compare(a, b) < 0 }

// Finding is one compatibility observation about an installed component.
type Finding struct {
	// Level is "ok", "warn" or "error".
	Level   string
	Message string
}

// CheckAppleContainer assesses an installed apple/container version string.
// An unparsable version yields a single warn finding.
func CheckAppleContainer(installed string) []Finding {
	v, err := Parse(installed)
	if err != nil {
		return []Finding{{"warn", "apple/container version could not be determined; verified range is " + AppleContainerMin + " – " + AppleContainerMax}}
	}
	var out []Finding
	if Less(v, MustParse(AppleContainerMinSafe)) {
		out = append(out, Finding{"warn", fmt.Sprintf(
			"apple/container %s has published security advisories fixed in %s (container/image path traversal, unvalidated OCI digests and registry auth realm); upgrade: container system stop && sudo /usr/local/bin/update-container.sh -v %s && container system start",
			v, AppleContainerMinSafe, AppleContainerMax)})
	}
	if Less(v, MustParse(AppleContainerMin)) {
		out = append(out, Finding{"error", fmt.Sprintf("apple/container %s is older than the oldest supported release %s", v, AppleContainerMin)})
	} else if Less(MustParse(AppleContainerMax), v) {
		out = append(out, Finding{"warn", fmt.Sprintf("apple/container %s is newer than the last verified release %s; expected to work, not yet exercised", v, AppleContainerMax)})
	}
	if len(out) == 0 {
		out = append(out, Finding{"ok", fmt.Sprintf("apple/container %s is within the verified range %s – %s", v, AppleContainerMin, AppleContainerMax)})
	}
	return out
}

// CheckOpenShell assesses an installed OpenShell gateway version string.
func CheckOpenShell(installed string) []Finding {
	v, err := Parse(installed)
	if err != nil {
		return []Finding{{"warn", "OpenShell gateway version could not be determined; verified range is " + OpenShellMin + " – " + OpenShellMax}}
	}
	switch {
	case Less(v, MustParse(OpenShellMin)):
		return []Finding{{"error", fmt.Sprintf("OpenShell %s predates the compute-driver contract this driver implements (%s)", v, OpenShellMin)}}
	case Less(MustParse(OpenShellMax), v):
		return []Finding{{"warn", fmt.Sprintf("OpenShell %s is newer than the last verified release %s; the contract is additive so this is expected to work, but has not been exercised", v, OpenShellMax)}}
	}
	return []Finding{{"ok", fmt.Sprintf("OpenShell %s is within the verified range %s – %s", v, OpenShellMin, OpenShellMax)}}
}
