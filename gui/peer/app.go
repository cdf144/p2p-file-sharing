package main

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

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
	a.corePeer = corepeer.NewCorePeer(corepeer.Config{})
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
		IP:    net.IPv4(127, 0, 0, 1),
		Port:  8080,
		Files: []protocol.FileMeta{},
	}
}

func (a *App) SelectShareDirectory() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Select Directory to Share",
	})
	if err != nil {
		return "", fmt.Errorf("failed to select directory: %w", err)
	}

	err = a.corePeer.SetSharedDirectory(dir)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to set shared directory: %v", err)
		return "", fmt.Errorf("failed to set shared directory: %w", err)
	}
	wruntime.EventsEmit(a.ctx, "filesScanned", a.corePeer.GetSharedFiles())

	return dir, nil
}

func (a *App) StartPeerLogic(indexURL, shareDir string, servePort, publicPort int) (string, error) {
	if a.corePeer.IsServing() {
		return "Peer is already running", nil
	}

	absShareDir := ""
	if shareDir != "" {
		var err error
		absShareDir, err = filepath.Abs(shareDir)
		if err != nil {
			wruntime.LogErrorf(a.ctx, "[peer] Failed to get absolute path for share directory %s: %v", shareDir, err)
			return "", fmt.Errorf("failed to get absolute path for share directory %s: %w", shareDir, err)
		}
	}

	newConfig := corepeer.Config{
		IndexURL:    indexURL,
		ShareDir:    absShareDir,
		ServePort:   servePort,
		PublicPort:  publicPort,
		FileScanCtx: a.ctx,
	}

	if err := a.corePeer.UpdateConfig(newConfig); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to update CorePeer config: %v", err)
		return "", fmt.Errorf("failed to update CorePeer config: %w", err)
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Starting CorePeer with IndexURL: %s, ShareDir: %s, ServePort: %d, PublicPort: %d",
		indexURL, absShareDir, servePort, publicPort,
	)

	statusMsg, err := a.corePeer.Start()
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

	a.corePeer.Stop()
	wruntime.LogInfo(a.ctx, "[peer] CorePeer stopped.")
	return nil
}

func (a *App) QueryIndexServer(indexURL string) ([]protocol.PeerInfo, error) {
	wruntime.LogInfof(a.ctx, "[peer] Querying index server: %s", indexURL)
	if a.corePeer == nil { // Should be initialized in startup, but just in case
		return nil, fmt.Errorf("CorePeer not initialized")
	}

	peers, err := a.corePeer.QueryPeersFromIndex(indexURL)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to query index server: %v", err)
		return nil, fmt.Errorf("failed to query index: %w", err)
	}
	wruntime.LogInfof(a.ctx, "[peer] Successfully queried index server: found %d peers", len(peers))
	return peers, nil
}

func (a *App) DownloadFileWithDialog(
	peerIP string,
	peerPort uint16,
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
		return "Save cancelled by user.", nil // Or specific error
	}

	wruntime.LogInfof(a.ctx, "[peer] Attempting to download file %s (checksum: %s) from %s:%d to %s",
		fileName, fileChecksum, peerIP, peerPort, saveDir)

	if a.corePeer == nil {
		return "", fmt.Errorf("CorePeer not initialized")
	}
	err = a.corePeer.DownloadFileFromPeer(peerIP, peerPort, fileChecksum, fileName, saveDir)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to download file: %v", err)
		return "", err
	}

	successMsg := fmt.Sprintf("Successfully downloaded %s to %s", fileName, saveDir)
	wruntime.LogInfo(a.ctx, "[peer] "+successMsg)
	return successMsg, nil
}
