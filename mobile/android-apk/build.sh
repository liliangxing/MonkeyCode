#!/bin/bash
set -e

export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
export ANDROID_HOME=/data/user/work/android-sdk
export PATH=$JAVA_HOME/bin:$ANDROID_HOME/build-tools/34.0.0:$ANDROID_HOME/platform-tools:$PATH

PROJECT=/data/user/work/MonkeyCode/mobile/android-apk
APP_DIR=$PROJECT/app
BUILD_DIR=$PROJECT/build
ASSETS=$APP_DIR/src/main/assets
RES=$APP_DIR/src/main/res
JAVA_SRC=$APP_DIR/src/main/java
MANIFEST=$APP_DIR/src/main/AndroidManifest.xml
ANDROID_JAR=$ANDROID_HOME/platforms/android-34/android.jar

mkdir -p $ASSETS

echo "=== Step 1: Compile Java sources ==="
mkdir -p $BUILD_DIR/classes
javac -source 11 -target 11 -classpath $ANDROID_JAR \
    -d $BUILD_DIR/classes \
    $JAVA_SRC/com/monkeyCode/ai/MainActivity.java 2>&1

echo "=== Step 2: Package resources with aapt2 ==="
mkdir -p $BUILD_DIR/compiled-res
aapt2 compile --dir $RES -o $BUILD_DIR/compiled-res/

aapt2 link \
    -I $ANDROID_JAR \
    --manifest $MANIFEST \
    -A $ASSETS \
    --java $BUILD_DIR/gen \
    -o $BUILD_DIR/resources.apk \
    --min-sdk-version 24 \
    --target-sdk-version 34 \
    $BUILD_DIR/compiled-res/*.flat 2>&1

echo "=== Step 3: Compile DEX ==="
mkdir -p $BUILD_DIR/dex
find $BUILD_DIR/classes -name "*.class" > $BUILD_DIR/class-list.txt
echo "Classes to compile:"
cat $BUILD_DIR/class-list.txt
d8 \
    --output $BUILD_DIR/dex \
    --lib $ANDROID_JAR \
    --min-api 24 \
    $(cat $BUILD_DIR/class-list.txt | tr '\n' ' ') 2>&1

echo "=== Step 4: Build unsigned APK ==="
cp $BUILD_DIR/resources.apk $BUILD_DIR/unsigned.apk
cd $BUILD_DIR
zip -j unsigned.apk dex/classes.dex 2>&1

echo "=== Step 5: Generate debug keystore ==="
rm -f $BUILD_DIR/debug.keystore
keytool -genkey \
    -keystore $BUILD_DIR/debug.keystore \
    -alias monkeycode \
    -keyalg RSA \
    -keysize 2048 \
    -validity 10000 \
    -storepass android \
    -keypass android \
    -dname "CN=MonkeyCode, OU=Dev, O=MonkeyCode AI, L=Beijing, ST=Beijing, C=CN" 2>&1

echo "=== Step 6: Sign APK ==="
apksigner sign \
    --ks $BUILD_DIR/debug.keystore \
    --ks-key-alias monkeycode \
    --ks-pass pass:android \
    --key-pass pass:android \
    --out $BUILD_DIR/MonkeyCode.apk \
    $BUILD_DIR/unsigned.apk 2>&1

echo "=== Step 7: Verify APK ==="
apksigner verify --verbose $BUILD_DIR/MonkeyCode.apk 2>&1

echo "=== Step 8: Copy to workspace ==="
cp $BUILD_DIR/MonkeyCode.apk /workspace/MonkeyCode.apk
ls -la /workspace/MonkeyCode.apk

echo "=== Build complete! ==="
