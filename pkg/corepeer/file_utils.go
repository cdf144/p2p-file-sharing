package corepeer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

// LocalFileInfo holds information about a file being shared.
type LocalFileInfo struct {
	Checksum string
	Path     string
	Name     string
	Size     int64
}

// ScanDirectory scans a directory for files and computes their metadata.
// It takes a context for potential cancellation if scanning is long.
// It returns []protocol.FileMeta for announcements/display,
// a map[checksum]fullFilePath for local lookups, and an error.
func ScanDirectory(
	ctx context.Context, dir string, logger *log.Logger,
) ([]protocol.FileMeta, map[string]string, error) {
	if logger == nil {
		logger = log.Default()
	}

	numWorkers := runtime.NumCPU()
	if numWorkers <= 0 {
		numWorkers = 1
	}

	jobs := make(chan string)
	type scanResult struct {
		meta protocol.FileMeta
		path string
	}
	jobResults := make(chan scanResult)
	var wg sync.WaitGroup

	for i := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for path := range jobs {
				select {
				case <-ctx.Done():
					logger.Printf("Worker %d: Scan cancelled.", workerID)
					return
				default:
					processedFileMeta, err := processFileAndCalcChunks(ctx, path)
					if err != nil {
						if err == context.Canceled || err == context.DeadlineExceeded {
							logger.Printf("Worker %d: Scan cancelled while processing %s.", workerID, path)
						} else {
							logger.Printf("Worker %d: Failed to process file %s: %v", workerID, path, err)
						}
						continue
					}

					select {
					case jobResults <- scanResult{
						meta: processedFileMeta,
						path: path,
					}:
						// Successfully sent result to jobResults channel
					case <-ctx.Done():
						logger.Printf("Worker %d: Scan cancelled while sending result for %s.", workerID, path)
						return
					}
				}
			}
		}(i)
	}

	var walkErr error
	var walkOnce sync.Once // For capturing only the first error from filepath.WalkDir
	go func() {
		defer close(jobs)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				logger.Println("Directory walk cancelled.")
				return ctx.Err()
			default:
				if !d.IsDir() {
					select {
					case jobs <- path:
						// Successfully sent path to jobs channel
					case <-ctx.Done():
						logger.Printf("Directory walk cancelled while sending path %s.", path)
						return ctx.Err()
					}
				}
				return nil
			}
		})
		if err != nil {
			walkOnce.Do(func() {
				walkErr = fmt.Errorf("failed to walk directory %s: %w", dir, err)
			})
		}
	}()

	go func() {
		wg.Wait()
		close(jobResults)
	}()

	scannedFileMetas := make([]protocol.FileMeta, 0)
	scannedFilePaths := make(map[string]string)
	for res := range jobResults {
		scannedFileMetas = append(scannedFileMetas, res.meta)
		scannedFilePaths[res.meta.Checksum] = res.path
	}

	if walkErr != nil && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return scannedFileMetas, scannedFilePaths, walkErr
	}
	if ctx.Err() != nil {
		return scannedFileMetas, scannedFilePaths, ctx.Err()
	}
	return scannedFileMetas, scannedFilePaths, nil
}

// FindSharedFileByChecksum searches for a file by its checksum in the shared directory.
func FindSharedFileByChecksum(shareDir, checksum string, logger *log.Logger) (*LocalFileInfo, error) {
	if logger == nil {
		logger = log.Default()
	}

	var result *LocalFileInfo
	err := filepath.WalkDir(shareDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			logger.Printf("Failed to read file %s: %v\n", path, err)
			return nil
		}

		fileChecksum, err := protocol.GenerateChecksum(file)
		file.Close()
		if err != nil {
			logger.Printf("Failed to generate checksum for file %s: %v\n", path, err)
			return nil
		}
		if fileChecksum == checksum {
			info, err := d.Info()
			if err != nil {
				return err
			}
			result = &LocalFileInfo{
				Checksum: fileChecksum,
				Path:     path,
				Name:     d.Name(),
				Size:     info.Size(),
			}
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk share directory %s: %w", shareDir, err)
	}
	if result == nil {
		return nil, fmt.Errorf("file with checksum %s not found in share directory %s", checksum, shareDir)
	}
	return result, nil
}

// processFileAndCalcChunks opens a file, calculates its overall checksum,
// divides it into chunks, calculates checksums for each chunk,
// and returns its metadata. This is done in a single pass over the file.
func processFileAndCalcChunks(ctx context.Context, filePath string) (protocol.FileMeta, error) {
	select {
	case <-ctx.Done():
		return protocol.FileMeta{}, ctx.Err()
	default:
	}

	file, err := os.Open(filePath)
	if err != nil {
		return protocol.FileMeta{}, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return protocol.FileMeta{}, fmt.Errorf("failed to stat file %s: %w", filePath, err)
	}

	if info.IsDir() { // Should not happen when WalkDir sends only files, but just in case
		return protocol.FileMeta{}, fmt.Errorf("path %s is a directory, not a file", filePath)
	}

	fileSize := info.Size()
	numChunks := int((fileSize + protocol.CHUNK_SIZE - 1) / protocol.CHUNK_SIZE)

	fullFileHasher := sha256.New()
	chunkHashes := make([]string, 0, numChunks)
	buf := make([]byte, protocol.CHUNK_SIZE)

	for i := range numChunks {
		select {
		case <-ctx.Done():
			return protocol.FileMeta{}, ctx.Err()
		default:
		}

		bytesToRead := protocol.CHUNK_SIZE
		if i == numChunks-1 {
			if remainder := fileSize % protocol.CHUNK_SIZE; remainder != 0 {
				bytesToRead = int(remainder)
			}
		}

		n, readErr := io.ReadFull(file, buf[:bytesToRead])
		if readErr != nil {
			return protocol.FileMeta{}, fmt.Errorf(
				"failed to read chunk %d for file %s (expected %d bytes): %w",
				i, filePath, bytesToRead, readErr,
			)
		}

		chunkData := buf[:n]

		if _, err := fullFileHasher.Write(chunkData); err != nil {
			return protocol.FileMeta{}, fmt.Errorf("failed to write chunk %d data to overall hasher for %s: %w", i, filePath, err)
		}

		chunkHash, err := protocol.GenerateChecksum(bytes.NewReader(chunkData))
		if err != nil {
			return protocol.FileMeta{}, fmt.Errorf("failed to generate checksum for chunk %d of file %s: %w", i, filePath, err)
		}
		chunkHashes = append(chunkHashes, chunkHash)
	}

	fullFileChecksum := hex.EncodeToString(fullFileHasher.Sum(nil))

	return protocol.FileMeta{
		Checksum:    fullFileChecksum,
		Name:        info.Name(),
		Size:        fileSize,
		ChunkSize:   protocol.CHUNK_SIZE,
		NumChunks:   numChunks,
		ChunkHashes: chunkHashes,
	}, nil
}
