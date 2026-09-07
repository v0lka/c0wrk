#!/usr/bin/env bash
# Guard against drift between c0wrk-git/PKGBUILD and the values it mirrors.
#
# c0wrk-git does not change from release to release — its pkgver() is derived
# from git describe. What does break it is somebody bumping ONNX_VERSION or a
# pinned digest in the Makefile, or WAILS_VERSION in the release workflow,
# without updating the PKGBUILD that copies those values. That failure is
# invisible until a user builds the package, so it is checked here on every
# push instead.
#
# The Makefile computes its digests through ifeq blocks keyed on uname, so the
# values are read by asking make to evaluate them for a given platform rather
# than by pattern-matching the conditionals. Command-line variable assignments
# take precedence over the makefile's own :=, and the conditionals are resolved
# during the parse that follows, so this yields exactly what a real build on
# that platform would use.
#
# Usage: packaging/archlinux/check-pins.sh
# Exits non-zero and prints every mismatch it finds.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
pkgbuild="$repo_root/packaging/archlinux/c0wrk-git/PKGBUILD"
makefile="$repo_root/Makefile"
release_workflow="$repo_root/.github/workflows/release.yml"

for f in "$pkgbuild" "$makefile" "$release_workflow"; do
	[ -f "$f" ] || { echo "check-pins: missing $f" >&2; exit 2; }
done

# --- read the sources of truth ---------------------------------------------

# make_var <UNAME_M> <VAR> — evaluate VAR as a Linux build on that architecture.
make_var() {
	# --no-print-directory: -C otherwise wraps the output in "Entering/Leaving
	# directory" lines, which would land in the captured value. -C itself stays
	# because the Makefile resolves several values through $(shell ...).
	make -f "$makefile" -C "$repo_root" --no-print-directory \
		UNAME_S=Linux UNAME_M="$1" \
		--eval="__print_pin: ; @echo \$($2)" __print_pin
}

mk_onnx_ver=$(make_var x86_64 ONNX_VERSION)
mk_onnx_x86_64=$(make_var x86_64 ONNX_SHA256)
mk_onnx_aarch64=$(make_var aarch64 ONNX_SHA256)
mk_model=$(make_var x86_64 EMBEDDING_MODEL_SHA256)
mk_tokenizer=$(make_var x86_64 EMBEDDING_TOKENIZER_SHA256)

# WAILS_VERSION lives in the release workflow's env block as a quoted scalar.
wf_wails_ver=$(sed -n 's/^[[:space:]]*WAILS_VERSION:[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}[[:space:]]*$/\1/p' \
	"$release_workflow" | head -1)

# --- read the PKGBUILD -----------------------------------------------------

# The PKGBUILD is bash. Sourcing it in a subshell gives the exact values a real
# build would see, including anything computed from other variables, instead of
# a second guess at its syntax. CARCH is what makepkg would export; the
# functions it defines are never called.
# shellcheck disable=SC1090
eval "$(
	CARCH=x86_64
	export CARCH
	# shellcheck source=/dev/null
	source "$pkgbuild" >/dev/null 2>&1
	# These come from the PKGBUILD sourced on the line above.
	# shellcheck disable=SC2154
	declare -p _onnx_ver _wails_ver source sha256sums sha256sums_x86_64 sha256sums_aarch64
)"

# index_of <needle> — position in the source array of the entry containing
# <needle>. The digest at the same index in sha256sums covers that entry, so
# looking the position up by name keeps this correct if the array is reordered.
index_of() {
	local needle=$1 i
	for i in "${!source[@]}"; do
		[[ ${source[$i]} == *"$needle"* ]] && { echo "$i"; return 0; }
	done
	echo "check-pins: no source entry matching '$needle' in $pkgbuild" >&2
	return 1
}

pb_model=${sha256sums[$(index_of '.onnx::')]}
pb_tokenizer=${sha256sums[$(index_of 'tokenizer-')]}

# --- compare ---------------------------------------------------------------

failures=0

compare() {
	local label=$1 want=$2 got=$3 want_src=$4
	if [ "$want" != "$got" ]; then
		printf 'MISMATCH  %s\n          PKGBUILD: %s\n          %s: %s\n\n' \
			"$label" "$got" "$want_src" "$want" >&2
		failures=$((failures + 1))
	else
		printf 'ok        %-28s %s\n' "$label" "$got"
	fi
}

compare 'ONNX version'        "$mk_onnx_ver"     "$_onnx_ver"                "Makefile ONNX_VERSION"
compare 'ONNX sha256 x86_64'  "$mk_onnx_x86_64"  "${sha256sums_x86_64[0]}"   "Makefile ONNX_SHA256 (linux/x86_64)"
compare 'ONNX sha256 aarch64' "$mk_onnx_aarch64" "${sha256sums_aarch64[0]}"  "Makefile ONNX_SHA256 (linux/aarch64)"
compare 'embedding model'     "$mk_model"        "$pb_model"                 "Makefile EMBEDDING_MODEL_SHA256"
compare 'embedding tokenizer' "$mk_tokenizer"    "$pb_tokenizer"             "Makefile EMBEDDING_TOKENIZER_SHA256"
compare 'Wails CLI version'   "$wf_wails_ver"    "$_wails_ver"               "release.yml WAILS_VERSION"

if [ "$failures" -gt 0 ]; then
	cat >&2 <<EOF

$failures pinned value(s) in packaging/archlinux/c0wrk-git/PKGBUILD no longer
match their source of truth. Copy the new values across, then regenerate the
metadata:

    cd packaging/archlinux/c0wrk-git && makepkg --printsrcinfo > .SRCINFO

EOF
	exit 1
fi

echo
echo "All c0wrk-git pins match their sources of truth."
