#!/bin/bash
set -e

BUILD_DIR="dist"
VERSION=${VERSION:-"dev"}
COMMIT_SHA=${GITHUB_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")}

echo "Building P2P File Sharing - Version: $VERSION, Commit: $COMMIT_SHA"
echo "============================================================"

mkdir -p "$BUILD_DIR"

BUILD_LDFLAGS="-X main.version=$VERSION -X main.commit=$COMMIT_SHA -s -w"
BUILD_FLAGS="-ldflags '$BUILD_LDFLAGS' -trimpath"

echo "Building index-server..."
eval "go build $BUILD_FLAGS -o '$BUILD_DIR/index-server-cli' ./cmd/index-server"
if [ $? -ne 0 ]; then
    echo "❌ Failed to build index-server."
    exit 1
fi
echo "✅ index-server built successfully: $BUILD_DIR/index-server-cli"

echo ""
echo "Building peer CLI..."
eval "go build $BUILD_FLAGS -o '$BUILD_DIR/peer-cli' ./cmd/peer"
if [ $? -ne 0 ]; then
    echo "❌ Failed to build peer CLI."
    exit 1
fi
echo "✅ peer CLI built successfully: $BUILD_DIR/peer-cli"

echo ""
if ! command -v wails &> /dev/null; then
    echo "⚠️  Wails CLI could not be found. Skipping GUI build."
    echo "   Install Wails: https://wails.io/docs/gettingstarted/installation"
else
    echo "Building peer GUI..."
    (cd gui/peer && wails build -clean -ldflags "$BUILD_LDFLAGS")
    if [ $? -ne 0 ]; then
        echo "❌ Failed to build peer GUI."
        exit 1
    fi

    GUI_APP_NAME=$(grep '"name":' gui/peer/wails.json | head -n 1 | sed 's/.*: "\(.*\)",/\1/')
    if [ -z "$GUI_APP_NAME" ]; then
        GUI_APP_NAME="peer"
    fi

    GUI_APP_PATH_UNIX="gui/peer/build/bin/${GUI_APP_NAME}"
    GUI_APP_PATH_WINDOWS="gui/peer/build/bin/${GUI_APP_NAME}.exe"

    if [ -f "$GUI_APP_PATH_UNIX" ]; then
        cp "$GUI_APP_PATH_UNIX" "$BUILD_DIR/${GUI_APP_NAME}-gui"
        echo "✅ peer GUI built and copied successfully: $BUILD_DIR/${GUI_APP_NAME}-gui"
    elif [ -f "$GUI_APP_PATH_WINDOWS" ]; then
        cp "$GUI_APP_PATH_WINDOWS" "$BUILD_DIR/${GUI_APP_NAME}-gui.exe"
        echo "✅ peer GUI built and copied successfully: $BUILD_DIR/${GUI_APP_NAME}-gui.exe"
    else
        echo "⚠️  Could not find peer GUI executable in gui/peer/build/bin/."
    fi
fi

echo ""
echo "🎉 All builds finished. Binaries are in the '$BUILD_DIR' directory."

echo ""
echo "Built files:"
ls -la "$BUILD_DIR/"
