#!/bin/sh
# Builds release assets into dist/ for VERSION (without the leading v):
#   renfe_VERSION_OS_ARCH.tar.gz        the renfe binary, LICENSE and README.md
#   renfe-skill_VERSION_OS_ARCH.zip     the agent skill folder "renfe/" with bin/renfe
#   checksums.txt                       SHA-256 of every archive
set -eu

version=${1:?usage: scripts/dist.sh VERSION (e.g. 0.1.0)}
cd "$(dirname "$0")/.."

# Every place that names the version must agree, so the skill launcher downloads
# the binary of the release it shipped with.
for file in skills/renfe/scripts/renfe skills/renfe/SKILL.md .claude-plugin/plugin.json .codex-plugin/plugin.json; do
	if ! grep -q "$version" "$file"; then
		echo "dist: $file does not mention version $version" >&2
		exit 1
	fi
done

rm -rf dist
mkdir -p dist
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
	os=${target%/*}
	arch=${target#*/}
	work=$(mktemp -d)
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=v$version" -o "$work/renfe" ./cmd/renfe
	cp LICENSE README.md "$work/"
	tar -czf "dist/renfe_${version}_${os}_${arch}.tar.gz" -C "$work" renfe LICENSE README.md

	mkdir -p "$work/skill"
	cp -R skills/renfe "$work/skill/renfe"
	mkdir -p "$work/skill/renfe/bin"
	cp "$work/renfe" "$work/skill/renfe/bin/renfe"
	(cd "$work/skill" && zip -qr -X "$OLDPWD/dist/renfe-skill_${version}_${os}_${arch}.zip" renfe)
	rm -rf "$work"
done
(cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz ./*.zip; else shasum -a 256 ./*.tar.gz ./*.zip; fi | sed 's#  \./#  #' >checksums.txt)
ls -l dist
