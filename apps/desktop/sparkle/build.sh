#!/bin/bash
# Build the upstream Sparkle CLI for our app. Keep source and binary SDK pinned together.
set -euo pipefail
cd "$(dirname "$0")/.."
version=2.10.1
cache="$PWD/dist/sparkle-cache"
mkdir -p "$cache"
work=$(mktemp -d "$cache/build.XXXXXX")
trap 'rm -rf "$work"' EXIT
fetch() {
  local file="$1" digest="$2" url="$3"
  if ! echo "$digest  $cache/$file" | shasum -a 256 --check --status; then
    curl --fail --silent --show-error --location --proto '=https' --max-time 120 --retry 2 "$url" -o "$work/$file"
    echo "$digest  $work/$file" | shasum -a 256 --check --status
    mv "$work/$file" "$cache/$file"
  fi
}
fetch sdk.tar.xz 1a856d2a9edb8e628de88121d7be04cf0e2167f8641650b78fca00b14244cf27 "https://github.com/sparkle-project/Sparkle/releases/download/$version/Sparkle-$version.tar.xz"
fetch source.tar.gz cd6f47568af791c2d2bb49da6a55519b1726ecaad7dad032c9f86590953ff3a9 "https://codeload.github.com/sparkle-project/Sparkle/tar.gz/refs/tags/$version"
tar -xf "$cache/sdk.tar.xz" -C "$work" Sparkle.framework LICENSE
tar -xf "$cache/source.tar.gz" -C "$work" "Sparkle-$version/sparkle-cli"
app="$work/OpenFox Update.app"
mkdir -p "$app/Contents/"{MacOS,Resources,Frameworks}
ditto "$work/Sparkle.framework" "$app/Contents/Frameworks/Sparkle.framework"
cp "$work/LICENSE" "$app/Contents/Resources/Sparkle-LICENSE"
cp sparkle/Info.plist "$app/Contents/Info.plist"
xcrun clang -arch arm64 -mmacosx-version-min=12.0 -fobjc-arc -fmodules \
  '-DSPU_OBJC_DIRECT_MEMBERS=__attribute__((objc_direct_members))' '-DSPU_OBJC_DIRECT=__attribute__((objc_direct))' \
  -F "$work" -framework Sparkle -framework AppKit -Wl,-rpath,@executable_path/../Frameworks \
  "$work/Sparkle-$version/sparkle-cli/"*.m -o "$app/Contents/MacOS/OpenFoxUpdate"
codesign --sign - "$app"
rm -rf "$cache/OpenFox Update.app"
mv "$app" "$cache/"
