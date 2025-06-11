package corepeer

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

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
	scannedFiles, scannedPaths, err := ScanDirectory(ctx, fm.shareDir, fm.logger)
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
