#!/bin/bash

set -e

FILE="$1"
CHUNK_SIZE=${2:-$((1 * 1024 * 1024))} # Default 1MB

if [ $# -eq 0 ]; then
    echo "Usage: $0 <file> [chunk_size]"
    echo "Example: $0 testfile.dat 1048576"
    exit 1
fi

if [ ! -f "$FILE" ]; then
    echo "Error: File '$FILE' not found."
    exit 1
fi

echo "File: $FILE"
echo "Chunk size: $CHUNK_SIZE bytes"
echo "---"

echo "Overall file hash:"
sha256sum "$FILE" | cut -d' ' -f1
echo "---"

FILE_SIZE=$(stat -c %s "$FILE" 2>/dev/null || stat -f %z "$FILE" 2>/dev/null)
OFFSET=0
CHUNK_COUNT=0

while [ "$OFFSET" -lt "$FILE_SIZE" ]; do
	CHUNK_COUNT=$((CHUNK_COUNT + 1))
	HASH=$(dd if="$FILE" bs=$CHUNK_SIZE count=1 skip=$((OFFSET / CHUNK_SIZE)) status=none 2>/dev/null | sha256sum | cut -d' ' -f1)
    echo "Chunk $CHUNK_COUNT (offset $OFFSET): $HASH"
    OFFSET=$((OFFSET + CHUNK_SIZE))
done

echo "---"
echo "Total chunks: $CHUNK_COUNT"
