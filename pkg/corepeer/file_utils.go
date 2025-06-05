package corepeer

import (
	"context"
	"fmt"
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
func ScanDirectory(ctx context.Context, dir string, logger *log.Logger) ([]protocol.FileMeta, error) {
	if logger == nil {
		logger = log.Default()
	}

	numWorkers := runtime.NumCPU()
	if numWorkers <= 0 {
		numWorkers = 1
	}

	jobs := make(chan string)
	jobResults := make(chan protocol.FileMeta)
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
					info, err := os.Stat(path)
					if err != nil {
						logger.Printf("Worker %d: failed to stat file %s: %v", workerID, path, err)
						continue
					}
					if info.IsDir() { // Should not happen when WalkDir sends only files, but just in case
						continue
					}

					file, err := os.Open(path)
					if err != nil {
						logger.Printf("Worker %d: failed to open file %s: %v", workerID, path, err)
						continue
					}

					checksum, err := protocol.GenerateChecksum(file)
					file.Close()

					if err != nil {
						logger.Printf("Worker %d: failed to generate checksum for file %s: %v", workerID, path, err)
						continue
					}

					select {
					case jobResults <- protocol.FileMeta{
						Checksum: checksum,
						Name:     info.Name(),
						Size:     info.Size(),
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

	scannedFiles := make([]protocol.FileMeta, 0)
	for file := range jobResults {
		scannedFiles = append(scannedFiles, file)
	}

	if walkErr != nil && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return scannedFiles, walkErr
	}
	if ctx.Err() != nil {
		return scannedFiles, ctx.Err()
	}

	return scannedFiles, nil
}

// FindSharedFileByChecksum searches for a file by its checksum in the shared directory.
func FindSharedFileByChecksum(shareDir, checksum string, logger *log.Logger) (*LocalFileInfo, error) {
	// PERF: This function is not efficient for large directories as it reads every file.
	// A more efficient implementation would use a map of checksums to paths
	// populated during an initial scan or maintained by the CorePeer.
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
