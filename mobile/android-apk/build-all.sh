#!/usr/bin/env bash
#
# build-all.sh — One-click build of every MonkeyCode WebView APK variant.
#
# Produces, in ./dist (or the directory passed as $1):
#   MonkeyCode.apk   (package com.monkeyCode.ai)
#   Monkey2.apk      (package com.monkeyCode.ai2)
#   Monkey3.apk      (package com.monkeyCode.ai3)
#
# Each variant is a fully independent APK (own package, own app label, own
# signed keystore) wrapping the same MonkeyCode console WebView.
#
# Usage:
#   ./build-all.sh [out_dir]
#
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${1:-$DIR/dist}"
mkdir -p "$OUT_DIR"

echo "###################################################"
echo "# MonkeyCode APK — one-click multi-variant build #"
echo "###################################################"
echo "Output dir: $OUT_DIR"
echo

"$DIR/build-variant.sh" ai  MonkeyCode MonkeyCode.apk "$OUT_DIR"
echo
"$DIR/build-variant.sh" ai2 Monkey2    Monkey2.apk    "$OUT_DIR"
echo
"$DIR/build-variant.sh" ai3 Monkey3    Monkey3.apk    "$OUT_DIR"

echo
echo "###################################################"
echo "# All variants built. Artifacts:                  #"
echo "###################################################"
ls -lh "$OUT_DIR"/*.apk
echo
echo "Done. APKs ready in: $OUT_DIR"
