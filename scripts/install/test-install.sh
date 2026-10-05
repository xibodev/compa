#!/usr/bin/env bash
# Tests install.sh against fake releases served from a temporary folder:
#
#   bash scripts/install/test-install.sh
#
# Runs on Linux, as in CI, and needs Go to build testhelper.go, the stand-in
# programs and web server, unless COMPA_TEST_HELPER names a built one. The
# two tests that start Compa need util-linux's script for a terminal.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
installer="$here/install.sh"

case "$(uname -s)/$(uname -m)" in
Linux/x86_64) platform=linux_amd64 ;;
Linux/aarch64) platform=linux_arm64 ;;
*)
	echo "skipped: these tests run on Linux"
	exit 0
	;;
esac

work=$(mktemp -d)
work=$(cd "$work" && pwd -P)
server=''
cleanup() {
	pkill -f "$work/bin/compa" 2>/dev/null || true
	if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; fi
	chmod -R u+w "$work" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

failures=0
pass() { printf 'ok   %s\n' "$1"; }
fail() {
	printf 'FAIL %s\n' "$1"
	sed 's/^/     | /' "$work/out" 2>/dev/null || true
	failures=$((failures + 1))
}

if [ -n "${COMPA_TEST_HELPER:-}" ]; then
	cp "$COMPA_TEST_HELPER" "$work/helper"
else
	go build -o "$work/helper" "$here/testhelper.go"
fi

# release VERSION: a fake release whose programs differ from other versions'.
release() {
	local files="$work/files/$1" dir="$work/releases/v$1"
	mkdir -p "$files" "$dir"
	for program in compa compa-kernel; do
		cp "$work/helper" "$files/$program"
		printf '%s' "$1" >>"$files/$program"
	done
	tar -czf "$dir/compa_$1_$platform.tar.gz" -C "$files" compa compa-kernel
	(cd "$dir" && sha256sum "compa_$1_$platform.tar.gz" >SHA256SUMS)
}
release 1.0.0
release 2.0.0
release 3.0.0
# Corrupted after its checksum was taken.
printf 'x' >>"$work/releases/v3.0.0/compa_3.0.0_$platform.tar.gz"

"$work/helper" serve "$work/releases" "$work/port" &
server=$!
for _ in $(seq 100); do
	if [ -s "$work/port" ]; then break; fi
	sleep 0.1
done
base="http://127.0.0.1:$(cat "$work/port")"

bin="$work/bin"
home="$work/home"
mkdir -p "$work/user"

# run_install VERSION [VAR=VALUE...]: the installer without a desktop or CI,
# its output in $work/out.
run_install() {
	local version=$1
	shift
	env -u CI -u DISPLAY -u WAYLAND_DISPLAY -u SSH_CONNECTION -u SSH_TTY \
		HOME="$work/user" COMPA_HOME="$home" COMPA_INSTALL_DIR="$bin" \
		COMPA_RELEASE_BASE_URL="$base" COMPA_INSTALL_TEST=1 COMPA_VERSION="v$version" \
		"$@" sh "$installer" </dev/null >"$work/out" 2>&1
}

# run_in_terminal VERSION [VAR=VALUE...]: the same in a terminal on a desktop.
run_in_terminal() {
	local version=$1
	shift
	env -u CI -u SSH_CONNECTION -u SSH_TTY DISPLAY=:99 \
		HOME="$work/user" COMPA_HOME="$home" COMPA_INSTALL_DIR="$bin" \
		COMPA_RELEASE_BASE_URL="$base" COMPA_INSTALL_TEST=1 COMPA_VERSION="v$version" \
		"$@" script -qec "sh '$installer'" /dev/null </dev/null >"$work/out" 2>&1
}

installed() {
	cmp -s "$bin/compa" "$work/files/$1/compa" && cmp -s "$bin/compa-kernel" "$work/files/$1/compa-kernel"
}
no_leftovers() {
	for file in "$bin"/.compa*.install-*; do
		if [ -e "$file" ]; then return 1; fi
	done
}
output_has() { grep -qF -- "$1" "$work/out"; }

# running PID: whether PID still runs. A stopped one can linger as a zombie
# until whichever process adopted it reaps it.
running() {
	local state
	state=$(sed 's/^.*) //' "/proc/$1/stat" 2>/dev/null | cut -c1) || return 1
	[ -n "$state" ] && [ "$state" != Z ]
}

# start_fake: run the installed compa as a user would, outside this shell.
# Its PID goes in $fake.
start_fake() {
	("$bin/compa" >/dev/null 2>&1 &
		echo $! >"$work/fake.pid")
	fake=$(cat "$work/fake.pid")
}

if ! run_install 1.0.0 COMPA_INSTALL_TEST= && output_has 'must be an https:// address' && [ ! -e "$bin" ]; then
	pass "refuses an http:// release address"
else fail "refuses an http:// release address"; fi

mkdir -p "$work/root"
printf '#!/bin/sh\necho 0\n' >"$work/root/id"
chmod +x "$work/root/id"
if ! run_install 1.0.0 PATH="$work/root:$PATH" && output_has 'not as root' && [ ! -e "$bin" ]; then
	pass "refuses to install as root"
else fail "refuses to install as root"; fi

if run_install 1.0.0 && installed 1.0.0 && output_has 'Compa was not started' && no_leftovers &&
	! pgrep -f "$bin/compa" >/dev/null; then
	pass "installs, and does not start Compa without a desktop"
else fail "installs, and does not start Compa without a desktop"; fi

start_fake
if run_install 2.0.0 COMPA_NO_START=1 && installed 2.0.0 && no_leftovers && ! running "$fake"; then
	pass "upgrades both programs and stops the running Compa"
else fail "upgrades both programs and stops the running Compa"; fi

if [ "$(id -u)" != 0 ]; then
	start_fake
	chmod a-w "$bin"
	if ! run_install 1.0.0 COMPA_NO_START=1 && output_has "could not write to $bin" &&
		installed 2.0.0 && running "$fake"; then
		pass "leaves a folder it can't write, and the running Compa, alone"
	else fail "leaves a folder it can't write, and the running Compa, alone"; fi
	chmod u+w "$bin"
	kill "$fake" 2>/dev/null || true
fi

if ! run_install 3.0.0 && output_has 'checksum mismatch' && installed 2.0.0 && no_leftovers; then
	pass "installs nothing from a download that fails its checksum"
else fail "installs nothing from a download that fails its checksum"; fi

if script --version 2>/dev/null | grep util-linux >/dev/null; then
	mkdir -p "$home"
	printf '{\n  "port": 18888,\n  "public": false\n}\n' >"$home/launcher-config.json"
	if run_in_terminal 2.0.0 && output_has 'open http://localhost:18888' && pgrep -f "$bin/compa" >/dev/null; then
		pass "starts Compa in a terminal on a desktop, and prints its port"
	else fail "starts Compa in a terminal on a desktop, and prints its port"; fi
	pkill -f "$bin/compa" 2>/dev/null || true

	if run_in_terminal 2.0.0 FAKE_COMPA=fail && output_has 'Compa stopped right after starting' &&
		output_has 'address already in use'; then
		pass "shows launcher.log when Compa stops right after starting"
	else fail "shows launcher.log when Compa stops right after starting"; fi
else
	echo "skipped: starting Compa (needs util-linux script)"
fi

if run_install 2.0.0 COMPA_UNINSTALL=1 && [ ! -e "$bin/compa" ] && [ ! -e "$bin/compa-kernel" ] && [ -d "$home" ]; then
	pass "uninstalls the programs and keeps the data"
else fail "uninstalls the programs and keeps the data"; fi

if [ "$failures" -ne 0 ]; then
	echo "$failures installer test(s) failed"
	exit 1
fi
echo "installer tests passed"
