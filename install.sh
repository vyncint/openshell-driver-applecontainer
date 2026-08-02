#!/bin/sh
# Install openshell-driver-applecontainer — the OpenShell compute driver
# backed by apple/container.
#
#   curl -LsSf https://raw.githubusercontent.com/vyncint/openshell-driver-applecontainer/main/install.sh | sh
#
# Checks prerequisites (Apple silicon macOS, Homebrew, apple/container,
# OpenShell) and offers to install any that are missing, then downloads the
# driver release (verifying its checksum) and optionally runs `setup`.
#
# Environment / flags:
#   -y, --yes            assume "yes" to every prompt (non-interactive)
#   --no-setup           install the binary but do not run `setup`
#   --version <vX.Y.Z>   install a specific release (default: latest)
#   --prefix <dir>       install prefix (default: /opt/homebrew)
#   OSHL_AC_VERSION, OSHL_AC_PREFIX, OSHL_AC_YES=1 mirror the flags.
set -eu

REPO="vyncint/openshell-driver-applecontainer"
BINARY="openshell-driver-applecontainer"
PREFIX="${OSHL_AC_PREFIX:-/opt/homebrew}"
VERSION="${OSHL_AC_VERSION:-}"
ASSUME_YES="${OSHL_AC_YES:-0}"
RUN_SETUP=1

OPENSHELL_INSTALL_URL="https://raw.githubusercontent.com/NVIDIA/OpenShell/main/install.sh"
CONTAINER_REPO="apple/container"

info() { printf '%s: %s\n' "$BINARY" "$*" >&2; }
warn() { printf '%s: warning: %s\n' "$BINARY" "$*" >&2; }
err() {
	printf '%s: error: %s\n' "$BINARY" "$*" >&2
	exit 1
}

need() { command -v "$1" >/dev/null 2>&1; }

# confirm asks a yes/no question, reading from the controlling terminal so
# it works under `curl … | sh`. Returns non-zero for "no" / no terminal.
confirm() {
	if [ "$ASSUME_YES" = 1 ]; then
		return 0
	fi
	if [ ! -r /dev/tty ]; then
		warn "no terminal available to prompt; re-run with -y to auto-confirm"
		return 1
	fi
	printf '%s [y/N] ' "$1" >/dev/tty
	read -r ans </dev/tty || return 1
	case "$ans" in
	[Yy] | [Yy][Ee][Ss]) return 0 ;;
	*) return 1 ;;
	esac
}

parse_args() {
	while [ "$#" -gt 0 ]; do
		case "$1" in
		-y | --yes) ASSUME_YES=1 ;;
		--no-setup) RUN_SETUP=0 ;;
		--version)
			[ "$#" -ge 2 ] || err "--version needs a value"
			VERSION="$2"
			shift
			;;
		--prefix)
			[ "$#" -ge 2 ] || err "--prefix needs a value"
			PREFIX="$2"
			shift
			;;
		-h | --help)
			sed -n '2,20p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'
			exit 0
			;;
		*) err "unknown option: $1" ;;
		esac
		shift
	done
}

check_platform() {
	[ "$(uname -s)" = Darwin ] || err "this driver runs on macOS only"
	[ "$(uname -m)" = arm64 ] || err "apple/container requires Apple silicon (arm64)"
	major=$(sw_vers -productVersion 2>/dev/null | cut -d. -f1)
	if [ -n "${major:-}" ] && [ "$major" -lt 26 ] 2>/dev/null; then
		warn "macOS 26+ is recommended; found $(sw_vers -productVersion)"
	fi
}

check_homebrew() {
	if need brew; then
		return
	fi
	warn "Homebrew is not installed; it is used to install prerequisites"
	if confirm "Install Homebrew now?"; then
		/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
		# Make brew available on the current PATH for the rest of this run.
		[ -x /opt/homebrew/bin/brew ] && eval "$(/opt/homebrew/bin/brew shellenv)"
	else
		err "Homebrew is required to install prerequisites; see https://brew.sh"
	fi
}

# install_container downloads and runs Apple's signed installer package.
install_container() {
	tag=$(curl -sSf "https://api.github.com/repos/$CONTAINER_REPO/releases/latest" |
		grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
	[ -n "$tag" ] || err "could not determine the latest apple/container release"
	pkg="container-${tag}-installer-signed.pkg"
	url="https://github.com/$CONTAINER_REPO/releases/download/$tag/$pkg"
	tmp_pkg=$(mktemp -d)/"$pkg"
	info "downloading apple/container $tag"
	curl -sSfL "$url" -o "$tmp_pkg" || err "failed to download $url"
	info "installing apple/container (requires your password for sudo installer)"
	sudo installer -pkg "$tmp_pkg" -target / || err "apple/container installer failed"
	rm -f "$tmp_pkg"
}

check_container() {
	if need container; then
		:
	else
		info "apple/container is not installed (https://github.com/apple/container)"
		if confirm "Install apple/container now?"; then
			install_container
		else
			err "apple/container is required"
		fi
	fi
	# Make sure the runtime service is up.
	container system status >/dev/null 2>&1 || container system start >/dev/null 2>&1 || true
}

check_openshell() {
	if need openshell; then
		return
	fi
	info "OpenShell is not installed (https://github.com/NVIDIA/OpenShell)"
	if confirm "Install OpenShell now (runs the official installer)?"; then
		# The official installer starts and health-checks the gateway, which
		# cannot pass until this driver's `setup` runs afterwards — so a
		# non-zero exit here is expected. Tolerate it and verify the binary
		# landed instead; `setup` (run later) brings the gateway up.
		curl -LsSf "$OPENSHELL_INSTALL_URL" | sh || true
		need openshell || err "OpenShell installation failed"
	else
		err "OpenShell is required"
	fi
}

resolve_version() {
	[ -n "$VERSION" ] && return
	VERSION=$(curl -sSf "https://api.github.com/repos/$REPO/releases/latest" |
		grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
	[ -n "$VERSION" ] || err "could not determine the latest release; pass --version"
}

install_driver() {
	need curl || err "curl is required"
	need shasum || err "shasum is required"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	ver_nov=${VERSION#v}
	archive="${BINARY}_${ver_nov}_darwin_arm64.tar.gz"
	base="https://github.com/$REPO/releases/download/$VERSION"

	info "downloading $BINARY $VERSION"
	curl -sSfL "$base/$archive" -o "$tmp/$archive" || err "failed to download $base/$archive"
	curl -sSfL "$base/checksums.txt" -o "$tmp/checksums.txt" || err "failed to download checksums"

	info "verifying checksum"
	(cd "$tmp" && grep " $archive\$" checksums.txt | shasum -a 256 -c -) >/dev/null 2>&1 ||
		err "checksum verification failed for $archive"

	tar -xzf "$tmp/$archive" -C "$tmp" || err "failed to extract $archive"

	bindir="$PREFIX/bin"
	if mkdir -p "$bindir" 2>/dev/null && [ -w "$bindir" ]; then
		install -m 0755 "$tmp/$BINARY" "$bindir/$BINARY"
	else
		info "installing to $bindir (requires sudo)"
		sudo install -d "$bindir"
		sudo install -m 0755 "$tmp/$BINARY" "$bindir/$BINARY"
	fi
	# The binary is unsigned; clear the quarantine flag so it runs.
	xattr -d com.apple.quarantine "$bindir/$BINARY" 2>/dev/null || true
	info "installed $BINARY $VERSION to $bindir"
	DRIVER_BIN="$bindir/$BINARY"
}

maybe_setup() {
	[ "$RUN_SETUP" = 1 ] || {
		info "run '$BINARY setup' when you are ready"
		return
	}
	if confirm "Run '$BINARY setup' now (installs the driver + gateway services)?"; then
		"$DRIVER_BIN" setup
	else
		info "run '$BINARY setup' when you are ready"
	fi
}

main() {
	parse_args "$@"
	check_platform
	check_homebrew
	check_container
	check_openshell
	resolve_version
	install_driver
	maybe_setup
	info "done"
}

main "$@"
