package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/netip"

	"github.com/cdf144/p2p-file-sharing/pkg/corepeer"
	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx      context.Context
	corePeer *corepeer.CorePeer
}

type DownloadProgressEvent struct {
	FileChecksum     string `json:"fileChecksum"`
	FileName         string `json:"fileName"`
	DownloadedChunks int    `json:"downloadedChunks"`
	TotalChunks      int    `json:"totalChunks"`
	IsComplete       bool   `json:"isComplete"`
	ErrorMessage     string `json:"errorMessage,omitempty"`
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.corePeer = corepeer.NewCorePeer(corepeer.CorePeerConfig{
		IndexURL:   "http://localhost:9090",
		ShareDir:   "",
		ServePort:  0,
		PublicPort: 0,
	})
	wruntime.LogInfo(ctx, "[peer] Application started. CorePeer initialized.")
}

// shutdown is called when the app terminates.
func (a *App) shutdown(ctx context.Context) {
	wruntime.LogInfo(ctx, "[peer] Application shutting down...")
	if a.corePeer != nil {
		a.corePeer.Shutdown()
		wruntime.LogInfo(ctx, "[peer] CorePeer stopped gracefully.")
	}
}

// DummyPeerInfo is a dummy method to satisfy the Wails binding requirements, helping
// Wails generate the protocol.PeerInfo type for the frontend.
// It's not intended to be called by the frontend for any real purpose.
func (a *App) DummyPeerInfo() protocol.PeerInfo {
	return protocol.PeerInfo{
		Address: netip.AddrPort{},
		Files:   []protocol.FileMeta{},
	}
}

// DummyDownloadProgress is a dummy method to satisfy the Wails binding requirements, helping
// Wails generate the main.DownloadProgressEvent type for the frontend.
// It's not intended to be called by the frontend for any real purpose.
func (a *App) DummyDownloadProgress() DownloadProgressEvent {
	return DownloadProgressEvent{
		FileChecksum:     "",
		FileName:         "",
		DownloadedChunks: 0,
		TotalChunks:      0,
		IsComplete:       false,
		ErrorMessage:     "",
	}
}

// GetCurrentConfig returns the current CorePeer configuration
func (a *App) GetCurrentConfig() corepeer.CorePeerConfig {
	if a.corePeer == nil {
		return corepeer.CorePeerConfig{
			IndexURL:   "http://localhost:9090",
			ShareDir:   "",
			ServePort:  0,
			PublicPort: 0,
		}
	}
	return a.corePeer.GetConfig()
}

// GetCurrentSharedFiles returns the currently shared files
func (a *App) GetCurrentSharedFiles() []protocol.FileMeta {
	if a.corePeer == nil {
		return []protocol.FileMeta{}
	}
	return a.corePeer.GetSharedFiles()
}

func (a *App) SelectShareDirectory() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select Directory to Share",
	})
	if err != nil {
		return "", fmt.Errorf("failed to select directory: %w", err)
	}
	return dir, nil
}

func (a *App) SelectCertificateFile() (string, error) {
	file, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select TLS Certificate File",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Certificate Files (*.crt, *.pem)", Pattern: "*.crt;*.pem"},
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to select certificate file: %w", err)
	}
	return file, nil
}

func (a *App) SelectPrivateKeyFile() (string, error) {
	file, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select TLS Private Key File",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Private Key Files (*.key, *.pem)", Pattern: "*.key;*.pem"},
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to select private key file: %w", err)
	}
	return file, nil
}

func (a *App) UpdatePeerConfig(indexURL, shareDir string, servePort, publicPort int, enableTLS bool, certFile, keyFile string) error {
	if a.corePeer == nil {
		return fmt.Errorf("CorePeer not initialized")
	}

	if enableTLS {
		if certFile == "" || keyFile == "" {
			return fmt.Errorf("TLS enabled but certificate or private key file is missing")
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return fmt.Errorf("failed to load TLS certificate pair: %w", err)
		}
	}

	newConfig := corepeer.CorePeerConfig{
		IndexURL:   indexURL,
		ShareDir:   shareDir,
		ServePort:  servePort,
		PublicPort: publicPort,
		TLS:        enableTLS,
		CertFile:   certFile,
		KeyFile:    keyFile,
	}

	var updatedConfig corepeer.CorePeerConfig
	var err error
	if updatedConfig, err = a.corePeer.UpdateConfig(a.ctx, newConfig); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		wruntime.EventsEmit(a.ctx, "peerConfigUpdated", err.Error())
		return fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.EventsEmit(a.ctx, "peerConfigUpdated", updatedConfig)
	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	wruntime.LogInfof(
		a.ctx,
		"[peer] Updated CorePeer config with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d, TLS: %t",
		indexURL, shareDir, servePort, publicPort, enableTLS,
	)

	return nil
}

func (a *App) StartPeerLogic(indexURL, shareDir string, servePort, publicPort int, enableTLS bool, certFile, keyFile string) error {
	if a.corePeer.IsServing() {
		wruntime.EventsEmit(a.ctx, "peerConfigUpdated", a.corePeer.GetConfig())
		return nil
	}

	if enableTLS {
		if certFile == "" || keyFile == "" {
			return fmt.Errorf("TLS enabled but certificate or private key file is missing")
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return fmt.Errorf("failed to load TLS certificate pair: %w", err)
		}
	}

	config := corepeer.CorePeerConfig{
		IndexURL:   indexURL,
		ShareDir:   shareDir,
		ServePort:  servePort,
		PublicPort: publicPort,
	}

	var updatedConfig corepeer.CorePeerConfig
	var err error
	if updatedConfig, err = a.corePeer.UpdateConfig(a.ctx, config); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		wruntime.EventsEmit(a.ctx, "peerConfigUpdated", err.Error())
		return fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.EventsEmit(a.ctx, "peerConfigUpdated", updatedConfig)
	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())
	wruntime.LogInfof(
		a.ctx,
		"[peer] Starting CorePeer with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d, TLS: %t",
		indexURL, shareDir, servePort, publicPort, enableTLS,
	)

	statusMsg, err := a.corePeer.Start(a.ctx)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to start CorePeer: %v", err)
		return fmt.Errorf("failed to start CorePeer: %w", err)
	}

	wruntime.LogInfo(a.ctx, "[peer] "+statusMsg)
	return nil
}

func (a *App) StopPeerLogic() error {
	wruntime.LogInfo(a.ctx, "[peer] Attempting to stop CorePeer...")
	if a.corePeer == nil {
		wruntime.LogInfo(a.ctx, "[peer] CorePeer not initialized, nothing to stop.")
		return fmt.Errorf("CorePeer not initialized")
	}

	if !a.corePeer.IsServing() {
		wruntime.LogInfo(a.ctx, "[peer] CorePeer is not running, no action taken.")
		return nil
	}

	a.corePeer.Stop()
	wruntime.LogInfo(a.ctx, "[peer] CorePeer stopped.")
	return nil
}

// FetchNetworkFiles retrieves a list of all available file metadata from the index server.
func (a *App) FetchNetworkFiles() ([]protocol.FileMeta, error) {
	wruntime.LogInfof(a.ctx, "[peer] Fetching available files from index server")
	if a.corePeer == nil { // Should be initialized in startup, but just in case
		return nil, fmt.Errorf("CorePeer not initialized")
	}

	files, err := a.corePeer.FetchFilesFromIndex(a.ctx)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to fetch files from index server: %v", err)
		return nil, fmt.Errorf("failed to fetch files from index: %w", err)
	}
	wruntime.LogInfof(a.ctx, "[peer] Successfully fetched files from index server: found %d files", len(files))
	return files, nil
}

func (a *App) FetchPeersForFile(checksum string) ([]protocol.PeerInfoSummary, error) {
	wruntime.LogInfof(a.ctx, "[peer] Fetching peers for file with checksum: %s", checksum)
	if a.corePeer == nil { // Should be initialized in startup, but just in case
		return nil, fmt.Errorf("CorePeer not initialized")
	}

	peers, err := a.corePeer.QueryPeersForFile(a.ctx, checksum)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to fetch peers for file %s: %v", checksum, err)
		return nil, fmt.Errorf("failed to fetch peers for file %s: %w", checksum, err)
	}

	wruntime.LogInfof(a.ctx, "[peer] Successfully fetched %d peers for file with checksum: %s", len(peers), checksum)
	return peers, nil
}

func (a *App) DownloadFileWithDialog(fileChecksum, fileName string) (string, error) {
	if a.corePeer == nil {
		return "", fmt.Errorf("CorePeer not initialized")
	}

	wruntime.LogInfof(a.ctx, "[peer] Fetching metadata for file %s (checksum: %s) before download.", fileName, fileChecksum)
	fileMeta, err := a.corePeer.FetchFileFromIndex(a.ctx, fileChecksum)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to fetch metadata for file %s: %v", fileName, err)
		return "", fmt.Errorf("failed to get metadata for file %s: %w", fileName, err)
	}
	if fileMeta.Name == "" {
		wruntime.LogWarningf(a.ctx, "[peer] Fetched FileMeta for %s has no name, using provided: %s", fileChecksum, fileName)
	}

	saveDir, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save Downloaded File",
		DefaultFilename: fileMeta.Name,
	})
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to choose save location: %v", err)
		return "", fmt.Errorf("failed to choose save location: %w", err)
	}
	if saveDir == "" {
		wruntime.LogInfo(a.ctx, "[peer] Save cancelled by user.")
		return "Save cancelled by user.", nil
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Attempting to download file %s (checksum: %s) to %s",
		fileMeta.Name, fileMeta.Checksum, saveDir,
	)

	progressCallback := func(chksum string, fName string, downloaded int, total int, complete bool, errMsg string) {
		wruntime.EventsEmit(a.ctx, "downloadProgress", DownloadProgressEvent{
			FileChecksum:     chksum,
			FileName:         fName,
			DownloadedChunks: downloaded,
			TotalChunks:      total,
			IsComplete:       complete,
			ErrorMessage:     errMsg,
		})
	}

	err = a.corePeer.DownloadFile(a.ctx, fileMeta, saveDir, progressCallback)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to download file %s: %v", fileMeta.Name, err)
		return "", fmt.Errorf("failed to download file %s: %w", fileMeta.Name, err)
	}

	successMsg := fmt.Sprintf("Successfully downloaded %s to %s", fileMeta.Name, saveDir)
	wruntime.LogInfo(a.ctx, "[peer] "+successMsg)
	return successMsg, nil
}

func (a *App) GetConnectedPeers() ([]corepeer.PeerRegistryInfo, error) {
	if a.corePeer == nil {
		return nil, fmt.Errorf("CorePeer not initialized")
	}

	allPeersMap := a.corePeer.GetConnectedPeers()
	var result []corepeer.PeerRegistryInfo
	for _, peerInfoPtr := range allPeersMap {
		if peerInfoPtr != nil {
			result = append(result, *peerInfoPtr)
		}
	}

	return result, nil
}
