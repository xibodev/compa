#!/usr/bin/env bash
# Hash the WIRE SHAPE of the v2 proposal, excluding the review appendix.
#
# WHY THIS EXISTS. Twice I published a number about this document that a sibling
# lane re-measured and got something different -- first the freeze diff (91/1 vs
# their 130/8), then "the wire shape is byte-identical" (actually 82 insertions,
# 6 deletions). Both times the CONCLUSION was sound and the arithmetic described
# a narrower thing than the sentence claimed: I was describing the subset I
# changed deliberately and stating it as a property of the whole file.
#
# A reader re-running a whole-file diff cannot tell my imprecision from a clause
# moving underneath them -- which is the doubt the evidence exists to remove. So
# the scope is EXECUTABLE rather than described in prose.
#
# The boundary is the review appendix heading. Everything above it is the shape a
# lane reviews; everything below is the record of who reviewed what. A review
# carries across a revision iff this hash is unchanged.
#
# THE BOUNDARY IS ASSERTED, NOT ASSUMED -- and the first version of this script
# did assume it. A module author ran it and found that 0e58c7c predates the
# appendix entirely, so the awk never matched, never exited, and hashed the
# WHOLE FILE. The published 22a6f6af2fd557d8 was a whole-document hash sitting
# in a column labelled "wire shape".
#
# A missing boundary and a matched boundary produced THE SAME OBSERVABLE: a
# 16-char hex string with nothing saying which scope it covered. The tool built
# to remove that exact doubt reproduced it one level in, silently, for one row.
#
# So a revision without the boundary is now a LOUD REFUSAL rather than a
# plausible number. This is RFC v2 section 9's own layer-2 rule -- confirm the
# mutation landed before trusting the result -- applied to a scope marker
# instead of a mutant. A scoped check needs its scope validated, or it is
# evidence about something it does not name.
# EXIT CODES -- and why a distinct one is worth having.
#
#   0  a scoped hash was computed
#   2  CANNOT READ the document at that revision (bad ref, missing file)
#   3  SCOPE REFUSED: the boundary is absent or ambiguous at that revision
#
# 3 rather than 1 so a caller can branch on "this revision has no wire-shape
# hash" versus "the script itself failed" (bad ref, missing file, not a repo).
# Those need different responses: the first is a fact about the revision, the
# second is a broken invocation.
#
# A module author measured 1 on every path after I documented 3. hash_at
# returned 3 and BOTH CALLERS FLATTENED IT -- `exit 1` on the no-args path, a
# hardcoded `status=1` on the multi-arg path. The value was right inside the
# function and wrong at the boundary the caller reads, so the doc promised a
# signal the script could not send. A distinct code is only worth documenting
# if a caller can branch on it. Fixed by propagating this constant, which is
# now the single source so the two cannot drift again.
readonly SCOPE_REFUSED=3
readonly READ_FAILED=2

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# THE DOCUMENT MOVED. It was docs/WIRE_V2_PROPOSAL.md until the docs layout
# lint was satisfied. Revisions before that move cannot be read at this path and
# the script refuses them with CANNOT READ (exit 2) rather than hashing nothing
# -- which is the guard working, not a break.
#
# To verify the frozen shape at a pre-move revision, read the old path directly:
#   git show <rev>:docs/WIRE_V2_PROPOSAL.md | awk '/^## Review record/{exit} {print}' | sha256sum
# That still yields 031a89d07f6dfd12 at dd56bc5. The content never changed.
doc=docs/architecture/WIRE_V2_PROPOSAL.md
boundary="^## Review record"

read_at() {
  local ref="$1"
  if [[ "$ref" == "WORKING" ]]; then cat "$doc"; else git show "$ref:$doc"; fi
}

hash_at() {
  local ref="$1" body count

  # READ FAILURE IS NOT SCOPE REFUSAL, and separating them is not pedantry.
  # `set -e` does not fire inside this assignment (the call sits in an `if`), so
  # a bad ref left $body EMPTY and the boundary count below came out 0 --
  # reporting "this revision has no review appendix" for a revision that was
  # never read. ZERO BOUNDARIES IN ZERO BYTES looks identical to zero
  # boundaries in a real document: the same two-states-one-observable defect
  # this script exists to prevent, reached through its own error path.
  if ! body="$(read_at "$ref" 2>/dev/null)"; then
    echo "wire-shape-hash: CANNOT READ $ref" >&2
    echo "  reason: $doc could not be read at that revision -- unknown ref," >&2
    echo "          missing file, or not a git repository." >&2
    echo "  note:   this is a BROKEN INVOCATION (exit $READ_FAILED), not a" >&2
    echo "          scope refusal (exit $SCOPE_REFUSED). A revision that has" >&2
    echo "          no wire-shape hash is a fact about the document; a ref" >&2
    echo "          that does not resolve is a fact about the command." >&2
    return "$READ_FAILED"
  fi

  # Assert the scope marker exists BEFORE emitting anything that looks like a
  # scoped hash. Without this the awk below silently falls through to the whole
  # file and reports it in the same format.
  count="$(printf '%s\n' "$body" | grep -c "$boundary" || true)"
  if [[ "$count" -eq 0 ]]; then
    echo "wire-shape-hash: REFUSING $ref" >&2
    echo "  reason: no boundary matching '$boundary' in $doc at that revision," >&2
    echo "          so a scoped hash cannot be computed. Hashing anyway would" >&2
    echo "          report a WHOLE-DOCUMENT hash in a wire-shape column, which" >&2
    echo "          is indistinguishable from a real one." >&2
    echo "  fix:    compare only revisions that contain the review appendix." >&2
    echo "          Revisions predating it (e.g. 0e58c7c) have no wire-shape" >&2
    echo "          hash, and a comparison across that gap cannot tell 'the" >&2
    echo "          shape changed' from 'the scope changed'." >&2
    return "$SCOPE_REFUSED"
  fi
  if [[ "$count" -gt 1 ]]; then
    echo "wire-shape-hash: REFUSING $ref" >&2
    echo "  reason: $count boundaries match '$boundary'; the scope is ambiguous" >&2
    echo "          and awk would cut at whichever comes first." >&2
    echo "  fix:    keep exactly one review-appendix heading." >&2
    return "$SCOPE_REFUSED"
  fi

  printf '%s\n' "$body" \
    | awk -v b="$boundary" '$0 ~ b {exit} {print}' \
    | sha256sum | cut -c1-16
}

# NOTE ON THE NO-ARGS PATH. This is written as an explicit if/else rather than
# `echo "... $(hash_at WORKING)"`, and that is not style. Command substitution
# DISCARDS the callee's exit status: the first fix here printed the refusal to
# stderr, then printed an empty "wire shape (working tree):" line and exited 0.
# A caller testing $? would have read a refused scope as success -- the same
# silent-scope defect one level out, in the very commit that fixed it.
if [[ $# -eq 0 ]]; then
  # `set -e` is disabled inside an `if` condition, so capture explicitly rather
  # than testing it. Writing `exit $?` after the `fi` reads the status of the IF
  # ITSELF, not of hash_at -- measured: that returned 0 for a duplicated
  # boundary, turning a refusal into a success. Third variant of the same
  # swallowed-status defect in one file.
  rc=0
  h="$(hash_at WORKING)" || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    echo "wire shape (working tree): $h"
    exit 0
  fi
  exit "$rc"
fi

status=0
for ref in "$@"; do
  if h="$(hash_at "$ref")"; then
    printf '%-12s %s\n' "$ref" "$h"
  else
    # CAPTURE the callee's code; never name one here. A module author found
    # this loop flattening 3 to a hardcoded 1. My first fix hardcoded 3 instead
    # -- which then swallowed READ_FAILED the same way and reported an unknown
    # ref as a scope refusal, so the distinction I had just built was still not
    # real. A caller-side literal is the defect whatever value it holds.
    rc=$?
    printf '%-12s %s\n' "$ref" "NO WIRE-SHAPE HASH (see message above)"
    # Worsen only, so a genuine refusal is not masked by a later read failure.
    if [[ "$rc" -gt "$status" ]]; then status="$rc"; fi
  fi
done
exit "$status"
