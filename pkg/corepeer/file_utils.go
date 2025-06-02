package corepeer

import (
	"context"
	"fmt"
	"io/fs"
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
func ScanDirectory(ctx context.Context, dir string) ([]protocol.FileMeta, error) {
	// PERF: Maybe implement a Producer/Consumer pattern for even better performance and resilience.
	var filePaths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			filePaths = append(filePaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", dir, err)
	}
	if len(filePaths) == 0 {
		return []protocol.FileMeta{}, nil
	}

	numWorkers := min(runtime.NumCPU(), len(filePaths))
	if numWorkers == 0 { // Should not happen, but just in case
		numWorkers = 1
	}

	jobs := make(chan string, len(filePaths))
	jobResults := make(chan protocol.FileMeta, len(filePaths))
	var wg sync.WaitGroup

	for i := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for path := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
					info, err := os.Stat(path)
					if err != nil {
						// TODO: Log a warning about the stat error
						continue
					}

					data, err := os.ReadFile(path)
					if err != nil {
						// TODO: Log a warning about the read error
						continue
					}

					jobResults <- protocol.FileMeta{
						Checksum: protocol.GenerateChecksum(data),
						Name:     info.Name(),
						Size:     info.Size(),
					}
				}
			}
		}(i)
	}

	for _, path := range filePaths {
		jobs <- path
	}
	close(jobs)

	wg.Wait()
	close(jobResults)

	scannedFiles := make([]protocol.FileMeta, 0, len(jobResults))
	for file := range jobResults {
		scannedFiles = append(scannedFiles, file)
	}
	return scannedFiles, nil
}

// FindSharedFileByChecksum searches for a file by its checksum in the shared directory.
func FindSharedFileByChecksum(shareDir, checksum string) (*LocalFileInfo, error) {
	// PERF: This function is not efficient for large directories as it reads every file.
	// A more efficient implementation would use a map of checksums to paths
	// populated during an initial scan or maintained by the CorePeer.
	var result *LocalFileInfo
	err := filepath.WalkDir(shareDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// TODO: Log a warning about the read error
			return nil
		}

		fileChecksum := protocol.GenerateChecksum(data)
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
