#!/usr/bin/env bash
#
# build-variant.sh — Build one MonkeyCode WebView APK variant with a custom
# package name, app label and output APK name.
#
# The original source under ./app is never modified; a pristine copy is staged
# under ./build/variant-<suffix>/, patched there, and compiled in place.
#
# Usage:
#   ./build-variant.sh <pkg_suffix> <app_name> <apk_name>
#
# Examples:
#   ./build-variant.sh ai   MonkeyCode  MonkeyCode.apk
#   ./build-variant.sh ai2  Monkey2     Monkey2.apk
#   ./build-variant.sh ai3  Monkey3     Monkey3.apk
#
# Env overrides:
#   JAVA_HOME    default /usr/lib/jvm/java-17-openjdk-amd64
#   ANDROID_HOME default /data/user/work/android-sdk
#   BT_VERSION   build-tools version, default 34.0.0
#   PLATFORM     android platform, default android-34
#   OUT_DIR      where the final apk is copied, default ./dist
#
set -euo pipefail

PKG_SUFFIX="${1:?ERROR: package suffix required (e.g. ai2)}"
APP_NAME="${2:?ERROR: app name required (e.g. Monkey2)}"
APK_NAME="${3:?ERROR: apk name required (e.g. Monkey2.apk)}"

PACKAGE="com.monkeyCode.${PKG_SUFFIX}"
PKG_PATH="com/monkeyCode/${PKG_SUFFIX}"

export JAVA_HOME="${JAVA_HOME:-/usr/lib/jvm/java-17-openjdk-amd64}"
export ANDROID_HOME="${ANDROID_HOME:-/data/user/work/android-sdk}"
BT_VERSION="${BT_VERSION:-34.0.0}"
PLATFORM="${PLATFORM:-android-34}"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/build-tools/${BT_VERSION}:$ANDROID_HOME/platform-tools:$PATH"
ANDROID_JAR="$ANDROID_HOME/platforms/${PLATFORM}/android.jar"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_APP="$SCRIPT_DIR/app"
WORK_DIR="$SCRIPT_DIR/build/variant-${PKG_SUFFIX}"
STAGE_APP="$WORK_DIR/app"
BUILD_DIR="$WORK_DIR/build"
OUT_DIR="${OUT_DIR:-$SCRIPT_DIR/dist}"

# Allow an optional 4th arg to override OUT_DIR for this invocation.
[ "${4:-}" != "" ] && OUT_DIR="$4"

echo "==> Building variant"
echo "    package : $PACKAGE"
echo "    label   : $APP_NAME"
echo "    apk     : $APK_NAME"
echo "    suffix  : $PKG_SUFFIX"

# Sanity checks ------------------------------------------------------------
command -v javac >/dev/null   || { echo "ERROR: javac not found (set JAVA_HOME)"; exit 1; }
command -v aapt2 >/dev/null   || { echo "ERROR: aapt2 not found (set ANDROID_HOME)"; exit 1; }
command -v d8 >/dev/null      || { echo "ERROR: d8 not found (set ANDROID_HOME)"; exit 1; }
command -v apksigner >/dev/null || { echo "ERROR: apksigner not found (set ANDROID_HOME)"; exit 1; }
[ -f "$ANDROID_JAR" ]         || { echo "ERROR: android.jar not found at $ANDROID_JAR"; exit 1; }
[ -d "$SRC_APP" ]             || { echo "ERROR: app source not found at $SRC_APP"; exit 1; }

# 0. Clean & stage a pristine copy of the app source -----------------------
echo "--- Step 0: stage pristine source copy ---"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"
cp -r "$SRC_APP" "$STAGE_APP"

ASSETS="$STAGE_APP/src/main/assets"
RES="$STAGE_APP/src/main/res"
JAVA_SRC="$STAGE_APP/src/main/java"
MANIFEST="$STAGE_APP/src/main/AndroidManifest.xml"
mkdir -p "$ASSETS"

# 1. Patch identity (manifest, strings, java package, download folder) -----
echo "--- Step 1: patch identity ---"
python3 - "$STAGE_APP" "$PACKAGE" "$PKG_PATH" "$APP_NAME" <<'PY'
import os, re, sys
stage, package, pkg_path, app_name = sys.argv[1:5]

# --- AndroidManifest.xml: package attribute + android:label ---
man = os.path.join(stage, "src/main/AndroidManifest.xml")
s = open(man, encoding="utf-8").read()
s = s.replace('package="com.monkeyCode.ai"', 'package="%s"' % package)
s = s.replace('android:label="MonkeyCode"', 'android:label="%s"' % app_name)
open(man, "w", encoding="utf-8").write(s)

# --- strings.xml: app_name ---
st = os.path.join(stage, "src/main/res/values/strings.xml")
s = open(st, encoding="utf-8").read()
s = re.sub(r'(<string name="app_name">)[^<]*(</string>)',
           r'\g<1>%s\g<2>' % app_name, s)
open(st, "w", encoding="utf-8").write(s)

# --- MainActivity.java: package declaration + download folder name ---
java_dir = os.path.join(stage, "src/main/java/com/monkeyCode/ai")
jf = os.path.join(java_dir, "MainActivity.java")
s = open(jf, encoding="utf-8").read()
s = s.replace("package com.monkeyCode.ai;", "package %s;" % package)
# per-variant download folder (3 distinct spots)
s = s.replace('"/MonkeyCode"', '"/%s"' % app_name)
s = s.replace('"MonkeyCode")', '"%s")' % app_name)
s = s.replace("Download/MonkeyCode/", "Download/%s/" % app_name)
open(jf, "w", encoding="utf-8").write(s)

# move the source file into the new package directory
new_dir = os.path.join(stage, "src/main/java", pkg_path)
os.makedirs(new_dir, exist_ok=True)
os.rename(jf, os.path.join(new_dir, "MainActivity.java"))
try:
    os.rmdir(java_dir)  # remove now-empty com/monkeyCode/ai
except OSError:
    pass
print("    patched: manifest, strings, java package, download folder")
PY

# 2. Compile Java sources --------------------------------------------------
echo "--- Step 2: compile Java ---"
mkdir -p "$BUILD_DIR/classes"
javac -source 11 -target 11 -classpath "$ANDROID_JAR" \
    -d "$BUILD_DIR/classes" \
    "$JAVA_SRC/$PKG_PATH/MainActivity.java"

# 3. Package resources with aapt2 ------------------------------------------
echo "--- Step 3: aapt2 compile + link ---"
mkdir -p "$BUILD_DIR/compiled-res"
aapt2 compile --dir "$RES" -o "$BUILD_DIR/compiled-res/"
aapt2 link \
    -I "$ANDROID_JAR" \
    --manifest "$MANIFEST" \
    -A "$ASSETS" \
    --java "$BUILD_DIR/gen" \
    -o "$BUILD_DIR/resources.apk" \
    --min-sdk-version 24 \
    --target-sdk-version 34 \
    "$BUILD_DIR"/compiled-res/*.flat

# 4. Compile DEX -----------------------------------------------------------
echo "--- Step 4: d8 dex ---"
mkdir -p "$BUILD_DIR/dex"
find "$BUILD_DIR/classes" -name "*.class" > "$BUILD_DIR/class-list.txt"
d8 \
    --output "$BUILD_DIR/dex" \
    --lib "$ANDROID_JAR" \
    --min-api 24 \
    $(cat "$BUILD_DIR/class-list.txt" | tr '\n' ' ')

# 5. Build unsigned APK ----------------------------------------------------
echo "--- Step 5: assemble unsigned apk ---"
cp "$BUILD_DIR/resources.apk" "$BUILD_DIR/unsigned.apk"
( cd "$BUILD_DIR" && zip -j unsigned.apk dex/classes.dex )

# 6. Generate (per-variant) debug keystore ---------------------------------
echo "--- Step 6: keystore ---"
rm -f "$BUILD_DIR/debug.keystore"
keytool -genkey \
    -keystore "$BUILD_DIR/debug.keystore" \
    -alias "${APP_NAME,,}" \
    -keyalg RSA -keysize 2048 -validity 10000 \
    -storepass android -keypass android \
    -dname "CN=${APP_NAME}, OU=Dev, O=MonkeyCode AI, L=Beijing, ST=Beijing, C=CN" >/dev/null 2>&1

# 7. Sign ------------------------------------------------------------------
echo "--- Step 7: sign + verify ---"
apksigner sign \
    --ks "$BUILD_DIR/debug.keystore" \
    --ks-key-alias "${APP_NAME,,}" \
    --ks-pass pass:android \
    --key-pass pass:android \
    --out "$BUILD_DIR/$APK_NAME" \
    "$BUILD_DIR/unsigned.apk"
apksigner verify --verbose "$BUILD_DIR/$APK_NAME" >/dev/null 2>&1 && echo "    signature verified"

# 8. Publish artifact ------------------------------------------------------
echo "--- Step 8: collect artifact ---"
mkdir -p "$OUT_DIR"
cp "$BUILD_DIR/$APK_NAME" "$OUT_DIR/$APK_NAME"
echo "==> DONE: $OUT_DIR/$APK_NAME  ($(du -h "$OUT_DIR/$APK_NAME" | cut -f1))"
