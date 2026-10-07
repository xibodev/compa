#!/bin/sh
# Checks that the relative links in the Markdown docs point at files that
# exist, for `make lint-docs`:
#
#   sh scripts/check-doc-links.sh
#
# Web links and #anchors aren't checked.
set -eu

cd "$(dirname "$0")/.."
broken=$(
	for doc in *.md docs/*.md; do
		dir=$(dirname "$doc")
		grep -o '](\([^)]*\))' "$doc" | sed 's/^](//; s/)$//; s/#.*//; s/[[:space:]].*//' |
			while read -r link; do
				case "$link" in
				'' | *://* | mailto:*) continue ;;
				esac
				if [ ! -e "$dir/$link" ]; then printf '%s: no file at %s\n' "$doc" "$link"; fi
			done
	done
)
if [ -n "$broken" ]; then
	printf '%s\n' "$broken" >&2
	exit 1
fi
echo "doc links: OK"
