package corepeer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

func TestProcessFileAndCalcChunksSynthetic(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "file_manager_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name         string
		fileContent  []byte
		expectedSize int64
		description  string
	}{
		{
			name:         "empty_file",
			fileContent:  []byte{},
			expectedSize: 0,
			description:  "Empty file, should have zero chunks",
		},
		{
			name:         "small_file",
			fileContent:  []byte("Hello, World!"),
			expectedSize: int64(len("Hello, World!")),
			description:  "Small file (< 1MB), should have 1 chunk",
		},
		{
			name:         "exactly_one_chunk",
			fileContent:  make([]byte, protocol.CHUNK_SIZE),
			expectedSize: protocol.CHUNK_SIZE,
			description:  "File exactly 1MB, should have 1 chunk",
		},
		{
			name:         "one_chunk_plus_one",
			fileContent:  make([]byte, protocol.CHUNK_SIZE+1),
			expectedSize: protocol.CHUNK_SIZE + 1,
			description:  "File 1MB+1, byte should have 2 chunks",
		},
		{
			name:         "multiple_chunks",
			fileContent:  make([]byte, protocol.CHUNK_SIZE*3+512*1024),
			expectedSize: protocol.CHUNK_SIZE*3 + 512*1024,
			description:  "File 3.5MB, should have 4 chunks",
		},
	}

	fm := NewFileManager(log.New(os.Stdout, "[test] ", log.LstdFlags))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFile := filepath.Join(tempDir, tt.name+".dat")

			if len(tt.fileContent) > 13 {
				for i := range tt.fileContent {
					tt.fileContent[i] = byte(i % 256)
				}
			}

			err := os.WriteFile(testFile, tt.fileContent, 0o644)
			if err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			ctx := context.Background()
			result, err := fm.processFileAndCalcChunks(ctx, testFile)
			if err != nil {
				t.Fatalf("processFileAndCalcChunks failed: %v", err)
			}

			if result.Size != tt.expectedSize {
				t.Errorf("Expected size %d, got %d", tt.expectedSize, result.Size)
			}

			expectedChunks := int((tt.expectedSize + protocol.CHUNK_SIZE - 1) / protocol.CHUNK_SIZE)
			if tt.expectedSize == 0 {
				expectedChunks = 0
			}

			if result.NumChunks != expectedChunks {
				t.Errorf("Expected %d chunks, got %d", expectedChunks, result.NumChunks)
			}

			if len(result.ChunkHashes) != expectedChunks {
				t.Errorf("Expected %d chunk hashes, got %d", expectedChunks, len(result.ChunkHashes))
			}

			expectedFileHash := sha256.Sum256(tt.fileContent)
			expectedFileHashStr := hex.EncodeToString(expectedFileHash[:])
			if result.Checksum != expectedFileHashStr {
				t.Errorf("File checksum mismatch:\nExpected: %s\nGot: %s", expectedFileHashStr, result.Checksum)
			}

			if len(tt.fileContent) > 0 {
				t.Run("chunk_hashes", func(t *testing.T) {
					verifyChunkHashes(t, tt.fileContent, result.ChunkHashes)
				})
			}
		})
	}
}

func verifyChunkHashes(t *testing.T, fileContent []byte, chunkHashes []string) {
	for i, expectedHash := range chunkHashes {
		start := i * protocol.CHUNK_SIZE
		end := min(start+protocol.CHUNK_SIZE, len(fileContent))

		chunkData := fileContent[start:end]
		actualHash := sha256.Sum256(chunkData)
		actualHashStr := hex.EncodeToString(actualHash[:])

		if expectedHash != actualHashStr {
			t.Errorf("Chunk %d hash mismatch:\nExpected: %s\nGot:      %s\nChunk size: %d",
				i, expectedHash, actualHashStr, len(chunkData))
		}
	}
}

func TestProcessFileAndCalcChunksWithRealFile(t *testing.T) {
	// Real file means a file with a variable pattern and size, still technically synthetic but closer to real-world.
	tempDir, err := os.MkdirTemp("", "file_manager_real_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "test_file.dat")

	file, err := os.Create(testFile)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	totalSize := protocol.CHUNK_SIZE*2 + protocol.CHUNK_SIZE/2
	for i := range totalSize {
		var pattern byte
		chunkNum := i / protocol.CHUNK_SIZE
		switch chunkNum {
		case 0:
			pattern = byte(i % 256)
		case 1:
			pattern = byte(255 - (i % 256))
		case 2:
			pattern = byte((i * 17) % 256)
		}
		file.Write([]byte{pattern})
	}
	file.Close()

	fm := NewFileManager(log.New(os.Stdout, "[test] ", log.LstdFlags))
	ctx := context.Background()

	result, err := fm.processFileAndCalcChunks(ctx, testFile)
	if err != nil {
		t.Fatalf("processFileAndCalcChunks failed: %v", err)
	}

	validateAgainstManualChunking(t, testFile, result)
}

func validateAgainstManualChunking(t *testing.T, filePath string, result protocol.FileMeta) {
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatalf("Failed to open file for validation: %v", err)
	}
	defer file.Close()

	file.Seek(0, 0)
	fullHasher := sha256.New()
	_, err = io.Copy(fullHasher, file)
	if err != nil {
		t.Fatalf("Failed to calculate full file hash: %v", err)
	}
	expectedFileHash := hex.EncodeToString(fullHasher.Sum(nil))

	if result.Checksum != expectedFileHash {
		t.Errorf("File hash mismatch:\nExpected: %s\nGot:      %s", expectedFileHash, result.Checksum)
	}

	file.Seek(0, 0)
	buf := make([]byte, protocol.CHUNK_SIZE)
	chunkIndex := 0

	for {
		n, err := file.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Failed to read chunk %d: %v", chunkIndex, err)
		}

		chunkData := buf[:n]
		chunkHash := sha256.Sum256(chunkData)
		expectedChunkHash := hex.EncodeToString(chunkHash[:])

		if chunkIndex >= len(result.ChunkHashes) {
			t.Fatalf("More chunks found than expected. Chunk %d", chunkIndex)
		}

		if result.ChunkHashes[chunkIndex] != expectedChunkHash {
			t.Errorf("Chunk %d hash mismatch:\nExpected: %s\nGot:      %s\nChunk size: %d",
				chunkIndex, expectedChunkHash, result.ChunkHashes[chunkIndex], n)
		}

		chunkIndex++
	}

	if chunkIndex != len(result.ChunkHashes) {
		t.Errorf("Expected %d chunks, but manually calculated %d", len(result.ChunkHashes), chunkIndex)
	}
}

func TestProcessFileAndCalcChunksContextCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "file_manager_cancel_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Sufficiently large file to ensure it doesn't complete immediately and allows for cancellation
	testFile := filepath.Join(tempDir, "large_file.dat")
	largeData := make([]byte, protocol.CHUNK_SIZE*5)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	err = os.WriteFile(testFile, largeData, 0o644)
	if err != nil {
		t.Fatalf("Failed to create large test file: %v", err)
	}

	fm := NewFileManager(log.New(os.Stdout, "[test] ", log.LstdFlags))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = fm.processFileAndCalcChunks(ctx, testFile)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got: %v", err)
	}
}

func TestProcessFileAndCalcChunksErrorCases(t *testing.T) {
	fm := NewFileManager(log.New(os.Stdout, "[test] ", log.LstdFlags))
	ctx := context.Background()

	_, err := fm.processFileAndCalcChunks(ctx, "/nonexistent/file.dat")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	tempDir, err := os.MkdirTemp("", "file_manager_error_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	_, err = fm.processFileAndCalcChunks(ctx, tempDir)
	if err == nil {
		t.Error("Expected error when processing directory")
	}
}

func TestProcessFileAndCalcChunksWithBashValidation(t *testing.T) {
	scriptPath := "../../scripts/validate_chunks.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		t.Skip("Bash validation script not found, skipping integration test")
	}

	tempDir, err := os.MkdirTemp("", "integration_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "integration_test.dat")

	size := protocol.CHUNK_SIZE*2 + protocol.CHUNK_SIZE/2
	data := make([]byte, size)
	for i := range size {
		data[i] = byte(i % 256)
	}

	err = os.WriteFile(testFile, data, 0o644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	fm := NewFileManager(log.New(os.Stdout, "[integration] ", log.LstdFlags))
	result, err := fm.processFileAndCalcChunks(context.Background(), testFile)
	if err != nil {
		t.Fatalf("processFileAndCalcChunks failed: %v", err)
	}

	cmd := exec.Command("bash", scriptPath, testFile, strconv.Itoa(protocol.CHUNK_SIZE))
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Bash validation script failed: %v", err)
	}

	lines := strings.Split(string(output), "\n")
	var bashFileHash string
	var bashChunkHashes []string

	for i, line := range lines {
		if strings.Contains(line, "Overall file hash:") && i+1 < len(lines) {
			parts := strings.Fields(lines[i+1])
			if len(parts) > 0 {
				bashFileHash = parts[0]
			}
		}
		if strings.Contains(line, "): ") {
			parts := strings.Split(line, "): ")
			if len(parts) == 2 {
				hashPart := strings.Fields(parts[1])
				if len(hashPart) > 0 {
					bashChunkHashes = append(bashChunkHashes, hashPart[0])
				}
			}
		}
	}

	if result.Checksum != bashFileHash {
		t.Errorf("File hash mismatch:\nGo: %s\nBash: %s", result.Checksum, bashFileHash)
	}

	if len(result.ChunkHashes) != len(bashChunkHashes) {
		t.Fatalf("Chunk count mismatch: Go has %d, Bash has %d",
			len(result.ChunkHashes), len(bashChunkHashes))
	}

	for i, goHash := range result.ChunkHashes {
		if goHash != bashChunkHashes[i] {
			t.Errorf("Chunk %d hash mismatch:\nGo: %s\nBash: %s", i, goHash, bashChunkHashes[i])
		}
	}
}

func BenchmarkProcessFileAndCalcChunks(b *testing.B) {
	// 10 MiB
	tempDir, err := os.MkdirTemp("", "benchmark_test")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "benchmark.dat")
	data := make([]byte, 10*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	err = os.WriteFile(testFile, data, 0o644)
	if err != nil {
		b.Fatalf("Failed to create benchmark file: %v", err)
	}

	fm := NewFileManager(log.New(io.Discard, "", 0))
	ctx := context.Background()

	for b.Loop() {
		_, err := fm.processFileAndCalcChunks(ctx, testFile)
		if err != nil {
			b.Fatalf("Benchmark failed: %v", err)
		}
	}
}

func BenchmarkProcessFileAndCalcChunksSmall(b *testing.B) {
	// 100 KiB
	tempDir, err := os.MkdirTemp("", "benchmark_small_test")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "small_benchmark.dat")
	data := make([]byte, 100*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	err = os.WriteFile(testFile, data, 0o644)
	if err != nil {
		b.Fatalf("Failed to create benchmark file: %v", err)
	}

	fm := NewFileManager(log.New(io.Discard, "", 0))
	ctx := context.Background()

	for b.Loop() {
		_, err := fm.processFileAndCalcChunks(ctx, testFile)
		if err != nil {
			b.Fatalf("Benchmark failed: %v", err)
		}
	}
}

func BenchmarkProcessFileAndCalcChunksLarge(b *testing.B) {
	// 100 MiB
	tempDir, err := os.MkdirTemp("", "benchmark_large_test")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "large_benchmark.dat")
	file, err := os.Create(testFile)
	if err != nil {
		b.Fatalf("Failed to create benchmark file: %v", err)
	}

	chunkSize := 1024 * 1024
	data := make([]byte, chunkSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	for range 100 {
		if _, err := file.Write(data); err != nil {
			file.Close()
			b.Fatalf("Failed to write to benchmark file: %v", err)
		}
	}
	file.Close()

	fm := NewFileManager(log.New(io.Discard, "", 0))
	ctx := context.Background()

	for b.Loop() {
		_, err := fm.processFileAndCalcChunks(ctx, testFile)
		if err != nil {
			b.Fatalf("Benchmark failed: %v", err)
		}
	}
}
