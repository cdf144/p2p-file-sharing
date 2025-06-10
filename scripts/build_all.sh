#!/bin/bash

echo "Building index-server..."
go build -o dist/index-server-cli ./cmd/index-server/main.go
if [ $? -ne 0 ]; then
    echo "Failed to build index-server."
    exit 1
fi
echo "index-server built successfully: dist/index-server"

echo ""
echo "Building peer CLI..."
go build -o dist/peer-cli ./cmd/peer/main.go
if [ $? -ne 0 ]; then
    echo "Failed to build peer CLI."
    exit 1
fi
echo "peer CLI built successfully: dist/peer-cli"

echo ""
if ! command -v wails &> /dev/null
then
    echo "Wails CLI could not be found. Please install Wails: https://wails.io/docs/gettingstarted/installation"
    exit 1
fi

echo "Building peer GUI..."
(cd gui/peer && wails build -clean)
if [ $? -ne 0 ]; then
    echo "Failed to build peer GUI."
    exit 1
fi

GUI_APP_NAME=$(grep '"name":' gui/peer/wails.json | head -n 1 | sed 's/.*: "\(.*\)",/\1/')
if [ -z "$GUI_APP_NAME" ]; then
    GUI_APP_NAME="peer"
fi

echo ${GUI_APP_NAME}

GUI_APP_PATH_UNIX="gui/peer/build/bin/${GUI_APP_NAME}"
GUI_APP_PATH_WINDOWS="gui/peer/build/bin/${GUI_APP_NAME}.exe"

if [ -f "$GUI_APP_PATH_UNIX" ]; then
    cp "$GUI_APP_PATH_UNIX" "dist/${GUI_APP_NAME}"
    echo "peer GUI built and copied successfully: dist/${GUI_APP_NAME}-gui"
elif [ -f "$GUI_APP_PATH_WINDOWS" ]; then
    cp "$GUI_APP_PATH_WINDOWS" "dist/${GUI_APP_NAME}.exe"
    echo "peer GUI built and copied successfully: dist/${GUI_APP_NAME}-gui.exe"
else
    echo "Could not find peer GUI executable in gui/peer/build/bin/. Please check the Wails build output."
fi

echo ""
echo "All builds finished. Binaries are in the 'dist' directory."
