#!/bin/sh
# Writes THIRD_PARTY_NOTICES next to the programs of a release build:
#
#   sh scripts/notices.sh out/linux_amd64
#
# The folder holds compa and compa-kernel (compa.exe and compa-kernel.exe for
# Windows), built after the web UI was built into web/backend/dist. Run it
# from the repository root, with the Go that built them. cmd/notices says
# what the file lists.
set -eu

dir=$1
ext=
if [ -f "$dir/compa.exe" ]; then
	ext=.exe
fi

# notices runs here, whatever platform the programs are for: GOOS and the
# like name the target of the build, not of notices.
#
# github.com/kagisearch/kagi-openapi-golang, the Kagi search client, has no
# license file, and its repository names no license.
env -u GOOS -u GOARCH -u CGO_ENABLED go run ./cmd/notices \
	-o "$dir/THIRD_PARTY_NOTICES" \
	-npm web/backend/dist/.vite/license.json \
	-npm web/backend/dist/.vite/css-licenses.json \
	-unlicensed github.com/kagisearch/kagi-openapi-golang \
	"$dir/compa$ext" "$dir/compa-kernel$ext"
