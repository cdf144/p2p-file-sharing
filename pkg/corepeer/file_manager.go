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

type FileManager struct {
	mu              sync.RWMutex
	shareDir        string
	sharedFiles     []protocol.FileMeta
	sharedFilePaths map[string]string
	logger          *log.Logger
}

func NewFileManager(logger *log.Logger) *FileManager {
	return &FileManager{
		logger:          logger,
		sharedFilePaths: make(map[string]string),
	}
}

func (fm *FileManager) GetSharedFiles() []protocol.FileMeta {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	filesCopy := make([]protocol.FileMeta, len(fm.sharedFiles))
	copy(filesCopy, fm.sharedFiles)
	return filesCopy
}

func (fm *FileManager) GetShareDir() string {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	return fm.shareDir
}

func (fm *FileManager) Reset() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.sharedFiles = []protocol.FileMeta{}
	fm.sharedFilePaths = make(map[string]string)
}

func (fm *FileManager) UpdateShareDir(ctx context.Context, shareDir string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if shareDir == "" {
		fm.shareDir = ""
		fm.sharedFiles = []protocol.FileMeta{}
		fm.sharedFilePaths = make(map[string]string)
		fm.logger.Printf("Share directory cleared - not sharing any files")
		return nil
	}

	if _, err := os.Stat(shareDir); os.IsNotExist(err) {
		return fmt.Errorf("share directory %s does not exist", shareDir)
	}
	fm.shareDir = shareDir

	fm.logger.Printf("Scanning directory: %s", fm.shareDir)
	scannedFiles, scannedPaths, err := fm.scanDirectory(ctx, fm.shareDir, fm.logger)
	if err != nil {
		return err
	}

	fm.sharedFiles = scannedFiles
	fm.sharedFilePaths = scannedPaths
	fm.logger.Printf("Scanned %d files from %s", len(fm.sharedFiles), fm.shareDir)
	return nil
}

func (fm *FileManager) GetFilePathByChecksum(checksum string) (string, error) {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	filePath, ok := fm.sharedFilePaths[checksum]
	if !ok {
		return "", fmt.Errorf("file with checksum %s not found in shared file paths", checksum)
	}
	return filePath, nil
}

func (fm *FileManager) GetFileMetaByChecksum(checksum string) (protocol.FileMeta, error) {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	if _, ok := fm.sharedFilePaths[checksum]; !ok {
		return protocol.FileMeta{}, fmt.Errorf("file with checksum %s not found in shared file paths", checksum)
	}

	for _, fm := range fm.sharedFiles {
		if fm.Checksum == checksum {
			return fm, nil
		}
	}
	return protocol.FileMeta{}, fmt.Errorf("file metadata for checksum %s not found in shared files list (inconsistent state)", checksum)
}

func (fm *FileManager) GetLocalFileInfoByChecksum(checksum string) (*LocalFileInfo, error) {
	fm.mu.RLock()
	filePath, ok := fm.sharedFilePaths[checksum]
	fm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file with checksum %s not found in shared files index", checksum)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		fm.logger.Printf("Warning: Error stating file %s (checksum %s): %v. File might be missing.", filePath, checksum, err)
		// NOTE: Trigger a re-scan (or remove from index) here or not?
		return nil, fmt.Errorf("file path %s (checksum %s) found in index but failed to stat: %w", filePath, checksum, err)
	}

	return &LocalFileInfo{
		Checksum: checksum,
		Path:     filePath,
		Name:     info.Name(),
		Size:     info.Size(),
	}, nil
}

// scanDirectory scans a directory for files and computes their metadata.
// It takes a context for potential cancellation if scanning is long.
// It returns []protocol.FileMeta for announcements/display,
// a map[checksum]fullFilePath for local lookups, and an error.
func (fm *FileManager) scanDirectory(
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
					processedFileMeta, err := fm.processFileAndCalcChunks(ctx, path)
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

// processFileAndCalcChunks opens a file, calculates its overall checksum,
// divides it into chunks, calculates checksums for each chunk,
// and returns its metadata. This is done in a single pass over the file.
func (fm *FileManager) processFileAndCalcChunks(ctx context.Context, filePath string) (protocol.FileMeta, error) {
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
