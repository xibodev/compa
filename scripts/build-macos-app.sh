#!/bin/bash
# Build the macOS .app bundle for Compa.
#
# Packages the output of `make product`: the shell the user launches (compa)
# and the kernel it supervises (compa-kernel). The shell locates the kernel
# beside its own executable, so both go into Contents/MacOS.

set -e

SHELL_EXECUTABLE="compa"
KERNEL_EXECUTABLE="compa-kernel"

APP_NAME="Compa"
APP_PATH="./build/${APP_NAME}.app"
APP_CONTENTS="${APP_PATH}/Contents"
APP_MACOS="${APP_CONTENTS}/MacOS"
APP_RESOURCES="${APP_CONTENTS}/Resources"
ICON_SOURCE="./scripts/icon.icns"

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
echo "Creating Info.plist..."
cat > "${APP_CONTENTS}/Info.plist" << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>compa</string>
    <key>CFBundleIdentifier</key>
    <string>com.compa.launcher</string>
    <key>CFBundleName</key>
    <string>Compa</string>
    <key>CFBundleDisplayName</key>
    <string>Compa</string>
    <key>CFBundleIconFile</key>
    <string>icon.icns</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>
    <key>LSUIElement</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>10.11</string>
</dict>
</plist>
EOF

#sips -z 128 128 "$ICON_SOURCE" --out "${ICONSET_PATH}/icon_128x128.png" > /dev/null 2>&1
#
## Create icns file
#iconutil -c icns "$ICONSET_PATH" -o "$ICON_OUTPUT" 2>/dev/null || {
#    echo "Warning: iconutil failed"
#}

cp $ICON_SOURCE "${APP_RESOURCES}/icon.icns"

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
