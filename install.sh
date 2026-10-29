#!/bin/sh
# Installs the latest telegram-agent-cli release binary.
#   curl -fsSL https://raw.githubusercontent.com/AlexDobrushskiy/telegram-agent-cli/main/install.sh | sh
# Env: VERSION (e.g. 0.1.3; default latest), INSTALL_DIR (default ~/.local/bin).
set -eu

repo=AlexDobrushskiy/telegram-agent-cli
name=telegram-agent-cli
dir=${INSTALL_DIR:-$HOME/.local/bin}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac

version=${VERSION:-}
if [ -z "$version" ]; then
  # .../releases/latest redirects to .../releases/tag/vX.Y.Z
  version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" | sed 's#.*/tag/v##')
fi
version=${version#v}
case $version in
  '' | */*) echo "could not find the latest version" >&2; exit 1 ;;
esac

archive="${name}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $name $version ($os/$arch)"
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "checksum mismatch for $archive" >&2
  exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp" "$name"
mkdir -p "$dir"
install -m 0755 "$tmp/$name" "$dir/$name"
echo "Installed $dir/$name"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
