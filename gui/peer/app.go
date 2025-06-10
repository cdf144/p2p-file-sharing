package main

import (
	"context"
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

type FileInfo struct {
	Checksum string
	Path     string
	Name     string
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
	if a.corePeer != nil && a.corePeer.IsServing() {
		a.corePeer.Stop()
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

func (a *App) SelectShareDirectory() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select Directory to Share",
	})
	if err != nil {
		return "", fmt.Errorf("failed to select directory: %w", err)
	}
	return dir, nil
}

func (a *App) UpdatePeerConfig(indexURL, shareDir string, servePort, publicPort int) (corepeer.CorePeerConfig, error) {
	if a.corePeer == nil {
		return corepeer.CorePeerConfig{}, fmt.Errorf("CorePeer not initialized")
	}

	newConfig := corepeer.CorePeerConfig{
		IndexURL:   indexURL,
		ShareDir:   shareDir,
		ServePort:  servePort,
		PublicPort: publicPort,
	}

	var updatedConfig corepeer.CorePeerConfig
	var err error
	if updatedConfig, err = a.corePeer.UpdateConfig(a.ctx, newConfig); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		return updatedConfig, fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	wruntime.LogInfof(
		a.ctx,
		"[peer] Updated CorePeer config with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d",
		indexURL, shareDir, servePort, publicPort,
	)

	return updatedConfig, nil
}

func (a *App) StartPeerLogic(indexURL, shareDir string, servePort, publicPort int) (corepeer.CorePeerConfig, error) {
	if a.corePeer.IsServing() {
		return a.corePeer.GetConfig(), nil
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
		return updatedConfig, fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Starting CorePeer with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d",
		indexURL, shareDir, servePort, publicPort,
	)

	statusMsg, err := a.corePeer.Start(a.ctx)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to start CorePeer: %v", err)
		return updatedConfig, fmt.Errorf("failed to start CorePeer: %w", err)
	}

	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	wruntime.LogInfo(a.ctx, "[peer] "+statusMsg)
	return updatedConfig, nil
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

func (a *App) FetchPeersForFile(checksum string) ([]netip.AddrPort, error) {
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

	err = a.corePeer.DownloadFile(a.ctx, fileMeta, saveDir)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to download file %s: %v", fileMeta.Name, err)
		return "", fmt.Errorf("failed to download file %s: %w", fileMeta.Name, err)
	}

	successMsg := fmt.Sprintf("Successfully downloaded %s to %s", fileMeta.Name, saveDir)
	wruntime.LogInfo(a.ctx, "[peer] "+successMsg)
	return successMsg, nil
}
