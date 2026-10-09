#!/bin/sh
# Compa installer for macOS and Linux.
#
#   curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
#
# Installs compa, the launcher, and compa-kernel, the harness it runs, into
# ~/.local/bin, and the release's license files into ~/.local/share/doc/compa.
# Run in a terminal on a desktop, it then starts Compa, which opens your
# browser to finish setting up; anywhere else (CI, Docker, a remote shell) it
# prints how to start Compa instead. Your settings and data live in
# ~/.compa, which this script leaves alone apart from the log of the Compa it
# starts, and it does not edit your shell startup files.
#
# Run it as yourself, not with sudo: Compa runs as the user who installs it.
#
# Options are environment variables; with "curl | sh" put them before sh:
#   curl -fsSL .../install.sh | COMPA_NO_START=1 sh
#
#   COMPA_VERSION=v1.2.3      install that release
#   COMPA_INSTALL_DIR=<dir>   install somewhere else
#   COMPA_NO_START=1          do not start Compa
#   COMPA_UNINSTALL=1         remove the programs, keep your data
#   COMPA_ALLOW_ROOT=1        install even when run as root
#
# COMPA_RELEASE_BASE_URL replaces https://github.com/xibodev/compa/releases/download,
# for example with a mirror. It must be an https:// address unless
# COMPA_INSTALL_TEST=1, which the installer tests set to serve a fake release.

set -eu

# The release workflow replaces this placeholder with the release tag.
STAMPED_TAG='__COMPA_VERSION__'
REPO='xibodev/compa'
DEFAULT_PORT=18800

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

# download URL FILE, over HTTPS only. COMPA_INSTALL_TEST also allows the
# plain HTTP of a local test server.
download() {
	if command -v curl >/dev/null 2>&1; then
		proto='=https'
		if is_set "${COMPA_INSTALL_TEST:-}"; then proto='=http,https'; fi
		curl --proto "$proto" --tlsv1.2 --fail --silent --show-error --location --retry 3 \
			--output "$2" "$1" || die "could not download $1"
	elif command -v wget >/dev/null 2>&1; then
		# BusyBox wget lacks --https-only; it still starts from an https:// URL.
		if ! is_set "${COMPA_INSTALL_TEST:-}" && wget --help 2>&1 | grep -q -e --https-only; then
			wget --https-only -q -O "$2" "$1" || die "could not download $1"
		else
			wget -q -O "$2" "$1" || die "could not download $1"
		fi
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

resolve_base() {
	BASE=${COMPA_RELEASE_BASE_URL:-https://github.com/$REPO/releases/download}
	case "$BASE" in
	https://*) ;;
	*)
		is_set "${COMPA_INSTALL_TEST:-}" ||
			die "COMPA_RELEASE_BASE_URL must be an https:// address, not '$BASE'."
		;;
	esac
	BASE=${BASE%/}
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
	# Files the in-app updater leaves behind while an old program still runs,
	# and those of an installer run that was killed.
	for leftover in "$1"/compa.old "$1"/compa-kernel.old "$1"/compa.*.old "$1"/compa-kernel.*.old \
		"$1"/.compa.new "$1"/.compa-kernel.new "$1"/.compa.old "$1"/.compa-kernel.old \
		"$1"/.compa.install-* "$1"/.compa-kernel.install-*; do
		if [ -e "$leftover" ]; then rm -f "$leftover"; fi
	done
}

# install_programs DIR: replace compa-kernel and compa in DIR together, or
# neither. Both are copied in first, so a folder that can't be written fails
# before the running Compa is stopped. Then each one is renamed over the old
# one, which works even while it runs; cleanup puts the old ones back if that
# is cut short.
install_programs() {
	remove_leftovers "$1"
	STAGE_DIR=$1
	for program in compa-kernel compa; do
		staged="$1/.$program.install-new.$$"
		{ cp "$TMP/files/$program" "$staged" && chmod 755 "$staged"; } 2>/dev/null ||
			die "could not write to $1."
	done
	stop_running "$1"
	SWAP_DIR=$1
	for program in compa-kernel compa; do
		if [ -e "$1/$program" ]; then
			mv -f "$1/$program" "$1/.$program.install-old.$$" || die "could not replace $1/$program."
		fi
		mv -f "$1/.$program.install-new.$$" "$1/$program" || die "could not replace $1/$program."
		SWAPPED="$SWAPPED $program"
	done
	SWAP_DIR=
	rm -f "$1/.compa-kernel.install-old.$$" "$1/.compa.install-old.$$"
}

# restore_programs DIR: undo the part of install_programs that already ran.
restore_programs() {
	for program in compa-kernel compa; do
		if [ -e "$1/.$program.install-old.$$" ]; then
			mv -f "$1/.$program.install-old.$$" "$1/$program" || true
		else
			case " $SWAPPED " in
			*" $program "*) rm -f "$1/$program" ;;
			esac
		fi
	done
}

cleanup() {
	if [ -n "$SWAP_DIR" ]; then restore_programs "$SWAP_DIR"; fi
	if [ -n "$STAGE_DIR" ]; then
		rm -f "$STAGE_DIR/.compa-kernel.install-new.$$" "$STAGE_DIR/.compa.install-new.$$"
	fi
	if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
}

# The license files a release holds, kept where the documentation of the
# programs a user installs goes rather than among the programs.
LICENSE_FILES='LICENSE NOTICE THIRD_PARTY_NOTICES'
license_dir() {
	printf '%s\n' "${XDG_DATA_HOME:-$HOME/.local/share}/doc/compa"
}

# install_licenses: copy in the release's license files, and drop those
# another release left that this one lacks. The programs are installed by
# then, so a failure here only warns.
install_licenses() {
	licenses=$(license_dir)
	if ! mkdir -p "$licenses" 2>/dev/null; then
		step "Could not create $licenses for the license files."
		return 0
	fi
	for file in $LICENSE_FILES; do
		if [ -f "$TMP/files/$file" ]; then
			if ! cp "$TMP/files/$file" "$licenses/$file" 2>/dev/null; then
				step "Could not write the license files to $licenses."
				return 0
			fi
			chmod 644 "$licenses/$file" 2>/dev/null || true
		else
			rm -f "$licenses/$file"
		fi
	done
	step "Kept the license files in $licenses."
}

remove_licenses() {
	licenses=$(license_dir)
	removed=
	for file in $LICENSE_FILES; do
		if [ -e "$licenses/$file" ]; then
			rm -f "$licenses/$file" && removed=1
		fi
	done
	rmdir "$licenses" 2>/dev/null || true
	if [ -n "$removed" ]; then step "Removed the license files from $licenses."; fi
}

data_dir() {
	printf '%s\n' "${COMPA_HOME:-$HOME/.compa}"
}

# launcher_port: the Service Port setting Compa listens on, kept in
# launcher-config.json beside config.json.
launcher_port() {
	settings="$(dirname "${COMPA_CONFIG:-$(data_dir)/config.json}")/launcher-config.json"
	port=
	if [ -f "$settings" ]; then
		port=$(awk -F '[,{}]' '{ for (i = 1; i <= NF; i++) print $i }' "$settings" |
			sed -n 's/^[[:space:]]*"port"[[:space:]]*:[[:space:]]*\([0-9]\{1,5\}\)[[:space:]]*$/\1/p' | head -n 1)
	fi
	if [ -z "$port" ] || [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then port=$DEFAULT_PORT; fi
	printf '%s\n' "$port"
}

# can_start: whether someone at this computer's desktop can finish setting up
# in the browser Compa opens. Not under CI, without a terminal, or in a
# remote shell.
can_start() {
	[ -t 1 ] || return 1
	! is_set "${CI:-}" || return 1
	[ -z "${SSH_CONNECTION:-}${SSH_TTY:-}" ] || return 1
	[ "$OS" = darwin ] || [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]
}

file_size() {
	if [ -f "$1" ]; then wc -c <"$1" | tr -d ' '; else echo 0; fi
}

# show_new_lines FILE SIZE: the last lines FILE gained after it was SIZE bytes.
show_new_lines() {
	if [ "$(file_size "$1")" -gt "$2" ]; then
		say "  $1:" >&2
		tail -c "+$(($2 + 1))" "$1" | tail -n 8 | sed 's/^/    /' >&2
	fi
}

start_compa() {
	logs="$(data_dir)/logs"
	mkdir -p "$logs"
	# Both logs keep earlier runs, so only what this start adds is shown.
	log_size=$(file_size "$logs/launcher.log")
	out_size=$(file_size "$logs/launcher.out")
	nohup "$DIR/compa" >>"$logs/launcher.out" 2>&1 </dev/null &
	started=$!
	sleep 2
	if kill -0 "$started" 2>/dev/null; then
		say "Compa is running in the background and opens your browser to finish setting up."
		say "If it doesn't, open http://localhost:$(launcher_port)"
	else
		say "Compa stopped right after starting. Its logs say:" >&2
		show_new_lines "$logs/launcher.log" "$log_size"
		show_new_lines "$logs/launcher.out" "$out_size"
		say "Start it again with: $DIR/compa" >&2
	fi
}

install_compa() {
	if [ "$(id -u)" = 0 ] && ! is_set "${COMPA_ALLOW_ROOT:-}"; then
		die "run the installer as your own user, not as root or with sudo: Compa would be installed for root and run as root. To do that anyway, set COMPA_ALLOW_ROOT=1."
	fi
	detect_platform
	resolve_base
	resolve_tag
	archive="compa_${VERSION}_${OS}_${ARCH}.tar.gz"
	say "Installing Compa $TAG ($OS/$ARCH) into $DIR"

	step "Downloading $archive..."
	download "$BASE/$TAG/SHA256SUMS" "$TMP/SHA256SUMS"
	download "$BASE/$TAG/$archive" "$TMP/$archive"
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
	install_programs "$DIR"
	step "Installed compa and compa-kernel."
	install_licenses

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
	elif can_start; then
		start_compa
	else
		say "Compa was not started: this looks like CI, a script or a remote shell."
		say "  On this computer's desktop, run: $DIR/compa"
		say "  Without a desktop, set the password, then run Compa in the terminal:"
		say "    $DIR/compa -password 'your-password'"
		say "    $DIR/compa -console -no-browser"
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
	remove_licenses
	remove_login_item "$DIR"
	say ""
	say "Compa is uninstalled."
	say "Your settings and data are still in $(data_dir); delete that folder to remove them too."
}

main() {
	[ -n "${HOME:-}" ] || die "HOME is not set."
	DIR=${COMPA_INSTALL_DIR:-$HOME/.local/bin}
	TMP='' STAGE_DIR='' SWAP_DIR='' SWAPPED=''
	trap cleanup EXIT
	trap 'exit 1' HUP INT TERM
	TMP=$(mktemp -d 2>/dev/null || mktemp -d -t compa-install) || die "could not create a temporary directory."
	if is_set "${COMPA_UNINSTALL:-}"; then
		uninstall_compa
	else
		install_compa
	fi
}

# Everything above only defines functions, so a download cut short cannot run
# half a script.
main "$@"
