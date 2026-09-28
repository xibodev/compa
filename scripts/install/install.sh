#!/bin/sh
# Compa installer for macOS and Linux.
#
#   curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
#
# Installs compa, the launcher, and compa-kernel, the harness it runs, into
# ~/.local/bin and starts Compa in the background; open the address it prints
# to finish setting up. Your settings and data live in ~/.compa, which this
# script never touches, and it does not edit your shell startup files.
#
# Options are environment variables; with "curl | sh" put them before sh:
#   curl -fsSL .../install.sh | COMPA_NO_START=1 sh
#
#   COMPA_VERSION=v1.2.3      install that release
#   COMPA_INSTALL_DIR=<dir>   install somewhere else
#   COMPA_NO_START=1          do not start Compa
#   COMPA_UNINSTALL=1         remove the programs, keep your data
#
# For tests only: COMPA_RELEASE_BASE_URL replaces
# https://github.com/xibodev/compa/releases/download, so the installer can run
# against a locally served fake release.

set -eu

# The release workflow replaces this placeholder with the release tag.
STAMPED_TAG='__COMPA_VERSION__'
REPO='xibodev/compa'
URL='http://127.0.0.1:18800'

say() { printf '%s\n' "$*"; }
step() { printf '  %s\n' "$*"; }
die() {
	printf 'Compa installer failed: %s\n' "$*" >&2
	exit 1
}

is_set() {
	case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
	1 | true | yes | on) return 0 ;;
	*) return 1 ;;
	esac
}

detect_platform() {
	case "$(uname -s)" in
	Linux) OS=linux ;;
	Darwin) OS=darwin ;;
	*) die "this installer supports Linux and macOS, not $(uname -s); on Windows use install.ps1." ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	arm64 | aarch64) ARCH=arm64 ;;
	*) die "Compa has no build for $(uname -m) processors, only x86_64 and arm64." ;;
	esac
	# A shell running under Rosetta reports x86_64 on Apple silicon.
	if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ] &&
		[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
		ARCH=arm64
	fi
}

# download URL FILE
download() {
	if command -v curl >/dev/null 2>&1; then
		curl --fail --silent --show-error --location --retry 3 --output "$2" "$1" ||
			die "could not download $1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || die "could not download $1"
	else
		die "curl or wget is needed to download Compa."
	fi
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum <"$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 <"$1" | cut -d ' ' -f 1
	else
		die "sha256sum or shasum is needed to verify the download."
	fi
}

resolve_tag() {
	tag=${COMPA_VERSION:-}
	if [ -z "$tag" ]; then
		case "$STAMPED_TAG" in
		v[0-9]*.[0-9]*.[0-9]*) tag=$STAMPED_TAG ;;
		esac
	fi
	if [ -z "$tag" ]; then
		[ -z "${COMPA_RELEASE_BASE_URL:-}" ] ||
			die "set COMPA_VERSION to the release to install from COMPA_RELEASE_BASE_URL."
		# A copy not stamped by a release, e.g. run from a source checkout.
		download "https://api.github.com/repos/$REPO/releases/latest" "$TMP/latest.json"
		tag=$(tr ',' '\n' <"$TMP/latest.json" |
			sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
		[ -n "$tag" ] || die "could not find the latest Compa release."
	fi
	VERSION=${tag#v}
	case "$VERSION" in
	*[!0-9A-Za-z.-]* | '' | [!0-9]*) die "'$tag' is not a Compa version such as v1.2.3." ;;
	*.*.*) ;;
	*) die "'$tag' is not a Compa version such as v1.2.3." ;;
	esac
	TAG="v$VERSION"
}

# is_compa DIR EXE: whether the executable EXE is compa or compa-kernel in DIR,
# including the backups the in-app updater leaves while an old one runs.
is_compa() {
	is_compa_exe=${2% (deleted)}
	[ "${is_compa_exe%/*}" = "$1" ] || return 1
	case "${is_compa_exe##*/}" in
	compa | compa-kernel | compa.old | compa-kernel.old | compa.*.old | compa-kernel.*.old) return 0 ;;
	esac
	return 1
}

running_pids() {
	if [ "$OS" = linux ] && [ -d /proc/self ]; then
		for proc in /proc/[0-9]*; do
			exe=$(readlink "$proc/exe" 2>/dev/null) || continue
			if is_compa "$1" "$exe"; then printf '%s\n' "${proc#/proc/}"; fi
		done
	else
		ps -axo pid=,comm= 2>/dev/null | while read -r pid comm; do
			if is_compa "$1" "$comm"; then printf '%s\n' "$pid"; fi
		done
	fi
}

# stop_running DIR: stop compa and compa-kernel processes started from DIR,
# and only those.
stop_running() {
	pids=$(running_pids "$1")
	[ -n "$pids" ] || return 0
	step "Stopping the running Compa..."
	# shellcheck disable=SC2086 # one PID per word
	kill $pids 2>/dev/null || true
	tries=0
	while [ "$tries" -lt 15 ]; do
		alive=
		for pid in $pids; do
			if kill -0 "$pid" 2>/dev/null; then alive="$alive $pid"; fi
		done
		[ -n "$alive" ] || return 0
		sleep 1
		tries=$((tries + 1))
	done
	# shellcheck disable=SC2086
	kill -9 $alive 2>/dev/null || true
}

remove_leftovers() {
	# Files the in-app updater leaves behind while an old program still runs.
	for leftover in "$1"/compa.old "$1"/compa-kernel.old "$1"/compa.*.old "$1"/compa-kernel.*.old \
		"$1"/.compa.new "$1"/.compa-kernel.new "$1"/.compa.old "$1"/.compa-kernel.old; do
		if [ -e "$leftover" ]; then rm -f "$leftover"; fi
	done
}

# install_program FROM DIR NAME: replace DIR/NAME by renaming a finished copy
# over it, which works even while the old program is still running.
install_program() {
	staged="$2/.$3.install.$$"
	cp "$1" "$staged" || die "could not write to $2."
	chmod 755 "$staged"
	if ! mv -f "$staged" "$2/$3"; then
		rm -f "$staged"
		die "could not replace $2/$3."
	fi
}

data_dir() {
	printf '%s\n' "${COMPA_HOME:-$HOME/.compa}"
}

install_compa() {
	detect_platform
	resolve_tag
	archive="compa_${VERSION}_${OS}_${ARCH}.tar.gz"
	base=${COMPA_RELEASE_BASE_URL:-https://github.com/$REPO/releases/download}
	base=${base%/}
	say "Installing Compa $TAG ($OS/$ARCH) into $DIR"

	step "Downloading $archive..."
	download "$base/$TAG/SHA256SUMS" "$TMP/SHA256SUMS"
	download "$base/$TAG/$archive" "$TMP/$archive"
	expected=$(tr -d '\r' <"$TMP/SHA256SUMS" |
		awk -v f="$archive" '$2 == f || $2 == "*" f { print tolower($1); exit }')
	[ -n "$expected" ] || die "SHA256SUMS does not list $archive, so it cannot be verified."
	case "$expected" in *[!0-9a-f]*) die "SHA256SUMS has a malformed entry for $archive." ;; esac
	[ "${#expected}" -eq 64 ] || die "SHA256SUMS has a malformed entry for $archive."
	actual=$(sha256_of "$TMP/$archive")
	[ "$actual" = "$expected" ] ||
		die "checksum mismatch for $archive: SHA256SUMS lists $expected but the download is $actual. Nothing was installed."
	step "Verified its SHA-256 checksum."

	mkdir "$TMP/files"
	tar -xzf "$TMP/$archive" -C "$TMP/files" || die "could not unpack $archive."
	for program in compa-kernel compa; do
		[ -f "$TMP/files/$program" ] || die "$archive does not contain $program."
	done

	mkdir -p "$DIR" || die "could not create $DIR."
	# The real path, which is what running processes report.
	DIR=$(cd "$DIR" && pwd -P)
	stop_running "$DIR"
	remove_leftovers "$DIR"
	install_program "$TMP/files/compa-kernel" "$DIR" compa-kernel
	install_program "$TMP/files/compa" "$DIR" compa
	step "Installed compa and compa-kernel."

	say ""
	say "Compa $TAG is installed."
	case ":${PATH:-}:" in
	*":$DIR:"*) ;;
	*)
		say "$DIR is not on your PATH. To run 'compa' by name, add this line to your shell's startup file:"
		say "  export PATH=\"$DIR:\$PATH\""
		;;
	esac
	if is_set "${COMPA_NO_START:-}"; then
		say "Start it with: $DIR/compa"
	else
		logs="$(data_dir)/logs"
		mkdir -p "$logs"
		nohup "$DIR/compa" >>"$logs/launcher.out" 2>&1 </dev/null &
		started=$!
		sleep 2
		if kill -0 "$started" 2>/dev/null; then
			say "Compa is running in the background: open $URL to finish setting up."
			say "Its output goes to $logs/launcher.out."
		else
			say "Compa stopped right after starting; the end of $logs/launcher.out says:" >&2
			tail -n 5 "$logs/launcher.out" >&2 || true
		fi
	fi
	say "Your settings and data live in $(data_dir)."
}

remove_login_item() {
	# The launch-at-login setting in Compa writes one of these; drop it only
	# when it starts this copy.
	for item in "$HOME/.config/autostart/compa.desktop" "$HOME/Library/LaunchAgents/io.compa.launcher.plist"; do
		if [ -f "$item" ] && grep -F "$1/compa" "$item" >/dev/null 2>&1; then
			rm -f "$item"
			step "Removed Compa from startup at login."
		fi
	done
}

uninstall_compa() {
	detect_platform
	say "Removing Compa from $DIR"
	if [ -d "$DIR" ]; then
		DIR=$(cd "$DIR" && pwd -P)
		stop_running "$DIR"
		for program in compa-kernel compa; do
			if [ -e "$DIR/$program" ]; then rm -f "$DIR/$program" || die "could not remove $DIR/$program."; fi
		done
		remove_leftovers "$DIR"
		step "Removed compa and compa-kernel."
	else
		step "The programs were not there."
	fi
	remove_login_item "$DIR"
	say ""
	say "Compa is uninstalled."
	say "Your settings and data are still in $(data_dir); delete that folder to remove them too."
}

main() {
	[ -n "${HOME:-}" ] || die "HOME is not set."
	DIR=${COMPA_INSTALL_DIR:-$HOME/.local/bin}
	TMP=$(mktemp -d 2>/dev/null || mktemp -d -t compa-install) || die "could not create a temporary directory."
	trap 'rm -rf "$TMP"' EXIT
	trap 'exit 1' HUP INT TERM
	if is_set "${COMPA_UNINSTALL:-}"; then
		uninstall_compa
	else
		install_compa
	fi
}

# Everything above only defines functions, so a download cut short cannot run
# half a script.
main "$@"
