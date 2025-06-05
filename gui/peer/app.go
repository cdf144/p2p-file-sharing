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
	a.corePeer = corepeer.NewCorePeer(corepeer.CorePeerConfig{})
	wruntime.LogInfo(ctx, "[peer] Application started. CorePeer initialized.")
}

// shutdown is called when the app terminates.
func (a *App) shutdown(ctx context.Context) {
	wruntime.LogInfo(ctx, "[peer] Application shutting down...")
	if a.corePeer != nil && a.corePeer.IsServing() {
		a.corePeer.Stop(a.ctx)
		wruntime.LogInfo(ctx, "[peer] CorePeer stopped gracefully.")
	}
}

// DummyFileMeta is a dummy method to satisfy the Wails binding requirements, helping
// Wails generate the protocol.FileMeta type for the frontend.
// It's not intended to be called by the frontend for any real purpose.
func (a *App) DummyFileMeta() protocol.FileMeta {
	return protocol.FileMeta{
		Checksum: "dummy-checksum",
		Name:     "dummy-file.txt",
		Size:     0,
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

	err = a.corePeer.SetSharedDirectory(a.ctx, dir)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to set shared directory: %v", err)
		return "", fmt.Errorf("failed to set shared directory: %w", err)
	}
	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	return dir, nil
}

func (a *App) UpdatePeerConfig(indexURL, shareDir string, servePort, publicPort int) (string, error) {
	if a.corePeer == nil {
		return "", fmt.Errorf("CorePeer not initialized")
	}

	newConfig := corepeer.CorePeerConfig{
		IndexURL:   indexURL,
		ShareDir:   shareDir,
		ServePort:  servePort,
		PublicPort: publicPort,
	}

	if err := a.corePeer.UpdateConfig(a.ctx, newConfig); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		return "", fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Updated CorePeer config with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d",
		indexURL, shareDir, servePort, publicPort,
	)

	return "CorePeer configuration updated successfully", nil
}

func (a *App) StartPeerLogic(indexURL, shareDir string, servePort, publicPort int) (string, error) {
	if a.corePeer.IsServing() {
		return "Peer is already running", nil
	}

	config := corepeer.CorePeerConfig{
		IndexURL:   indexURL,
		ShareDir:   shareDir,
		ServePort:  servePort,
		PublicPort: publicPort,
	}

	if err := a.corePeer.UpdateConfig(a.ctx, config); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		return "", fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Starting CorePeer with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d",
		indexURL, shareDir, servePort, publicPort,
	)

	statusMsg, err := a.corePeer.Start(a.ctx)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to start CorePeer: %v", err)
		return "", fmt.Errorf("failed to start peer: %w", err)
	}

	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	wruntime.LogInfo(a.ctx, "[peer] "+statusMsg)
	return statusMsg, nil
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

	a.corePeer.Stop(a.ctx)
	wruntime.LogInfo(a.ctx, "[peer] CorePeer stopped.")
	return nil
}

// FetchNetworkFiles retrieves a list of all available file metadata from the index server.
func (a *App) FetchNetworkFiles() ([]protocol.FileMeta, error) {
	wruntime.LogInfof(a.ctx, "[peer] Fetching available files from index server")
	if a.corePeer == nil { // Should be initialized in startup, but just in case
		return nil, fmt.Errorf("CorePeer not initialized")
	}

	// Call the FetchFilesFromIndex method on the corePeer instance
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

func (a *App) DownloadFileWithDialog(
	peerAddrPort netip.AddrPort,
	fileChecksum, fileName string,
) (string, error) {
	saveDir, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save Downloaded File",
		DefaultFilename: fileName,
	})
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to choose save location: %v", err)
		return "", fmt.Errorf("failed to choose save location: %w", err)
	}
	if saveDir == "" {
		wruntime.LogInfo(a.ctx, "[peer] Save cancelled by user.")
		return "Save cancelled by user.", nil
	}

	wruntime.LogInfof(a.ctx, "[peer] Attempting to download file %s (checksum: %s) from %s:%d to %s",
		fileName, fileChecksum, peerAddrPort.Addr(), peerAddrPort.Port(), saveDir)

	if a.corePeer == nil {
		return "", fmt.Errorf("CorePeer not initialized")
	}

	err = a.corePeer.DownloadFileFromPeer(peerAddrPort, fileChecksum, fileName, saveDir)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to download file: %v", err)
		return "", err
	}

	successMsg := fmt.Sprintf("Successfully downloaded %s to %s", fileName, saveDir)
	wruntime.LogInfo(a.ctx, "[peer] "+successMsg)
	return successMsg, nil
}
