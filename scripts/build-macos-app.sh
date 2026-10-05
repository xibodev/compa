#!/bin/bash
# Build the macOS .app bundle for Compa.
#
# Packages the output of `make product`: the shell the user launches (compa)
# and the kernel it supervises (compa-kernel). The shell locates the kernel
# beside its own executable, so both go into Contents/MacOS.
#
# `make build-macos-app` runs it with VERSION set to the version it stamped
# into the programs; the bundle reports that version's numeric part.

set -e

SHELL_EXECUTABLE="compa"
KERNEL_EXECUTABLE="compa-kernel"

APP_NAME="Compa"
APP_PATH="./build/${APP_NAME}.app"
APP_CONTENTS="${APP_PATH}/Contents"
APP_MACOS="${APP_CONTENTS}/MacOS"
APP_RESOURCES="${APP_CONTENTS}/Resources"
ICON_SOURCE="./scripts/icon.icns"
# The same identifier as the launch-at-login LaunchAgent, and the macOS
# version release.yml builds for.
BUNDLE_ID="io.compa.launcher"
MINIMUM_MACOS="12.0"

# CFBundleShortVersionString takes up to three numbers, so 1.2.3-4-gabc is 1.2.3.
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || true)}"
BUNDLE_VERSION=$(printf '%s' "$VERSION" | sed -n 's/^v\{0,1\}\([0-9]\{1,\}\.[0-9]\{1,\}\.[0-9]\{1,\}\)\([-+].*\)\{0,1\}$/\1/p')
BUNDLE_VERSION="${BUNDLE_VERSION:-0.0.0}"

# Clean up existing .app
if [ -d "$APP_PATH" ]; then
    echo "Removing existing ${APP_PATH}"
    rm -rf "$APP_PATH"
fi

# Create directory structure
echo "Creating .app bundle structure..."
mkdir -p "$APP_MACOS"
mkdir -p "$APP_RESOURCES"

# Copy executables
echo "Copying executables..."
for executable in "$SHELL_EXECUTABLE" "$KERNEL_EXECUTABLE"; do
    if [ ! -f "./build/${executable}" ]; then
        echo "Error: ./build/${executable} not found."
        echo "Run: make product"
        exit 1
    fi
    cp "./build/${executable}" "${APP_MACOS}/${executable}"
done
chmod +x "${APP_MACOS}/"*

# Create Info.plist
echo "Creating Info.plist (version ${BUNDLE_VERSION})..."
cat > "${APP_CONTENTS}/Info.plist" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>compa</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleName</key>
    <string>Compa</string>
    <key>CFBundleDisplayName</key>
    <string>Compa</string>
    <key>CFBundleIconFile</key>
    <string>icon.icns</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${BUNDLE_VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${BUNDLE_VERSION}</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>
    <key>LSUIElement</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>${MINIMUM_MACOS}</string>
</dict>
</plist>
EOF

cp "$ICON_SOURCE" "${APP_RESOURCES}/icon.icns"

echo ""
echo "=========================================="
echo "Successfully created: ${APP_PATH}"
echo "=========================================="
echo ""
echo "To launch Compa:"
echo "  1. Double-click ${APP_NAME}.app in Finder"
echo "  2. Or use: open ${APP_PATH}"
echo ""
echo "Note: The app will run in the menu bar (systray) without a terminal window."
echo ""
