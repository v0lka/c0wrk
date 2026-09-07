#!/usr/bin/env bash
# Point packaging/archlinux/c0wrk-bin at a different upstream release.
#
# Rewrites _tag, resets pkgrel, refreshes every sha256sum, and regenerates
# .SRCINFO. Run by the release workflow on every published tag, and safe to run
# by hand:
#
#     packaging/archlinux/bump-bin.sh v0.8.0
#
# Digests for the release archives are taken from the release's own SHA256SUMS
# asset rather than by downloading 200 MB and hashing it locally. That is both
# faster and a stronger statement: the PKGBUILD asserts the bytes upstream
# published, not merely the bytes this machine happened to fetch. Sources that
# are not covered by SHA256SUMS (LICENSE, the icon) are small, and are fetched
# and hashed directly. Local files are hashed from the working tree.
#
# Regenerating .SRCINFO needs makepkg. Without it the PKGBUILD is still updated
# and the script exits non-zero, so CI cannot silently ship a stale .SRCINFO.
set -euo pipefail

tag=${1-}
if [ -z "$tag" ]; then
	echo "usage: ${0##*/} <release-tag>   e.g. ${0##*/} v0.8.0" >&2
	exit 2
fi
# Accept 0.8.0 as well as v0.8.0; the repository tags releases with the v.
[ "${tag#v}" = "$tag" ] && tag="v$tag"

pkgdir_src=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/c0wrk-bin" && pwd)
pkgbuild="$pkgdir_src/PKGBUILD"
[ -f "$pkgbuild" ] || { echo "bump-bin: missing $pkgbuild" >&2; exit 2; }

repo_url=https://github.com/v0lka/c0wrk

echo "==> Bumping c0wrk-bin to $tag"

# --- the release must exist and publish checksums ---------------------------

sha_asset=$(mktemp)
trap 'rm -f "$sha_asset"' EXIT
if ! curl -fsSL -o "$sha_asset" "$repo_url/releases/download/$tag/SHA256SUMS"; then
	cat >&2 <<EOF
bump-bin: could not fetch SHA256SUMS for $tag.

Either the tag does not exist, or its release has not finished publishing
assets yet. This script must run after the release job, not alongside it.
EOF
	exit 1
fi

# --- rewrite _tag and pkgrel, then re-read the PKGBUILD ---------------------

# _tag drives pkgver and every URL, so it is updated before the source arrays
# are read back: the digests below must describe the NEW release's files.
sed -i "s|^_tag=.*|_tag=$tag|" "$pkgbuild"
sed -i "s|^pkgrel=.*|pkgrel=1|" "$pkgbuild"

# Sourcing the PKGBUILD yields exactly the arrays makepkg would see, including
# every URL already expanded, rather than a second guess at its syntax.
eval "$(
	CARCH=x86_64
	export CARCH
	# shellcheck source=/dev/null
	source "$pkgbuild" >/dev/null 2>&1
	# These come from the PKGBUILD sourced on the line above.
	# shellcheck disable=SC2154
	declare -p pkgver source source_x86_64 source_aarch64
)"

echo "    pkgver=$pkgver  pkgrel=1"

# --- resolve a digest for one source entry ----------------------------------

# digest_for <source-entry>
#   local file        -> hash it from the package directory
#   listed in SHA256SUMS -> reuse the published digest, no download
#   anything else     -> fetch and hash (LICENSE and the icon; both tiny)
digest_for() {
	local entry=$1 name url base published
	if [[ $entry == *"::"* ]]; then
		name=${entry%%::*}
		url=${entry#*::}
	else
		name=$entry
		url=$entry
	fi

	if [[ $url != *://* ]]; then
		sha256sum "$pkgdir_src/$name" | cut -d' ' -f1
		return
	fi

	base=${url##*/}
	published=$(awk -v f="$base" '$2 == f { print $1; exit }' "$sha_asset")
	if [ -n "$published" ]; then
		printf '%s\n' "$published"
		return
	fi

	curl -fsSL "$url" | sha256sum | cut -d' ' -f1
}

declare -a sums=()
for entry in "${source[@]}"; do
	sums+=("$(digest_for "$entry")")
done
sum_x86_64=$(digest_for "${source_x86_64[0]}")
sum_aarch64=$(digest_for "${source_aarch64[0]}")

# --- splice the arrays back -------------------------------------------------

SUMS_JOINED=$(printf '%s\n' "${sums[@]}") \
SUM_X86_64="$sum_x86_64" \
SUM_AARCH64="$sum_aarch64" \
PKGBUILD_PATH="$pkgbuild" \
python3 <<'PY'
import os, re, pathlib

path = pathlib.Path(os.environ["PKGBUILD_PATH"])
text = path.read_text()

def render(name, values):
    pad = " " * (len(name) + 2)
    body = ("'\n" + pad + "'").join(values)
    return f"{name}=('{body}')"

def splice(text, name, values):
    # Digests never contain a parenthesis, so the array ends at the first ')'.
    pattern = re.compile(r"^" + re.escape(name) + r"=\([^)]*\)", re.MULTILINE)
    new, n = pattern.subn(lambda _: render(name, values), text, count=1)
    if n != 1:
        raise SystemExit(f"bump-bin: could not locate {name}=(...) in {path}")
    return new

text = splice(text, "sha256sums", os.environ["SUMS_JOINED"].split("\n"))
text = splice(text, "sha256sums_x86_64", [os.environ["SUM_X86_64"]])
text = splice(text, "sha256sums_aarch64", [os.environ["SUM_AARCH64"]])
path.write_text(text)
PY

bash -n "$pkgbuild"

echo "    sha256sums updated (${#sums[@]} common + 2 per-architecture)"

# --- .SRCINFO ---------------------------------------------------------------

if ! command -v makepkg >/dev/null 2>&1; then
	echo "bump-bin: makepkg not found — PKGBUILD updated but .SRCINFO is now stale." >&2
	echo "          Run 'makepkg --printsrcinfo > .SRCINFO' in $pkgdir_src on an Arch host." >&2
	exit 1
fi

( cd "$pkgdir_src" && makepkg --printsrcinfo > .SRCINFO )
echo "    .SRCINFO regenerated"
echo "==> c0wrk-bin now tracks $tag"
