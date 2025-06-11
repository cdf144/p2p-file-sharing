package corepeer

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

const (
	maxConcurrentDownloadsPerFile   = 5
	maxRetriesPerChunk              = 2
	chunkDownloadTimeout            = 60 * time.Second
	peerConnectTimeout              = 10 * time.Second
	persistentConnectionIdleTimeout = 2 * time.Minute
	activeRequestProcessingTimeout  = 90 * time.Second
)

// TODO: Implement downloading files in chunks from multiple peers, with progress reporting and connections tracking.
// TODO: Implement optional secure connections (TLS) for file transfers.

// CorePeerConfig holds configuration for the CorePeer.
type CorePeerConfig struct {
	IndexURL   string
	ShareDir   string
	ServePort  int // 0 for random
	PublicPort int // 0 to use ServePort for announcement
}

// CorePeer manages the core P2P logic.
type CorePeer struct {
	config            CorePeerConfig
	rootCtx           context.Context // Root context for the peer's lifetime, from Start()
	isServing         bool
	announcedAddr     netip.AddrPort
	mu                sync.RWMutex
	logger            *log.Logger
	fileManager       *FileManager
	indexClient       *IndexClient
	downloadManager   *DownloadManager
	connectionHandler *ConnectionHandler
	tcpServer         *TCPServer
}

// chunkDownloadResult represents the result of a goroutine worker downloading a single chunk of data.
type chunkDownloadResult struct {
	index int
	data  []byte
	err   error
	peer  netip.AddrPort
}

// NewCorePeer creates a new CorePeer instance.
func NewCorePeer(cfg CorePeerConfig) *CorePeer {
	logger := log.New(log.Writer(), "[corepeer] ", log.LstdFlags|log.Lmsgprefix)
	p := &CorePeer{logger: logger}
	p.fileManager = NewFileManager(logger)
	p.indexClient = NewIndexClient(cfg.IndexURL, logger)
	p.downloadManager = NewDownloadManager(logger, p.indexClient)
	p.connectionHandler = NewConnectionHandler(logger, p.fileManager)
	p.tcpServer = NewTCPServer(logger, p.connectionHandler, p.fileManager)
	p.UpdateConfig(context.Background(), cfg)
	return p
}

// Start initializes and starts the CorePeer service, making it ready to share files and participate in the P2P network.
func (p *CorePeer) Start(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isServing {
		return "Peer is already running", nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	p.rootCtx = ctx

	// 1. Determine IP address
	announceIP, err := DetermineMachineIP()
	if err != nil {
		return "", fmt.Errorf("failed to determine machine IP address: %w", err)
	}
	p.logger.Printf("Determined machine IP: %s", announceIP.String())

	// 2. Start serving files
	p.config.ServePort, err = p.processServerPortConfig(p.config.ServePort)
	if err != nil {
		return "", fmt.Errorf("failed to process serve port configuration: %w", err)
	}
	p.logger.Printf("Using serve port: %d", p.config.ServePort)
	if p.config.ShareDir != "" {
		p.tcpServer.Start(ctx, p.config.ServePort)
	}

	// 3. Announce to index server
	var announcePort uint16
	if p.config.PublicPort != 0 {
		announcePort = uint16(p.config.PublicPort)
	} else {
		announcePort = uint16(p.config.ServePort)
	}
	p.logger.Printf("Announcing with port: %d", announcePort)

	p.announcedAddr = netip.AddrPortFrom(announceIP, announcePort)
	if err := p.indexClient.Announce(p.announcedAddr, p.fileManager.GetSharedFiles()); err != nil {
		p.tcpServer.Stop()
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}

	p.isServing = true
	statusMsg := fmt.Sprintf(
		"Peer started. Sharing from: %s. IP: %s, Serving Port: %d, Announced Port: %d. Files shared: %d",
		p.config.ShareDir, p.announcedAddr.Addr(), p.config.ServePort, p.announcedAddr.Port(), len(p.fileManager.GetSharedFiles()),
	)
	p.logger.Println(statusMsg)
	return statusMsg, nil
}

// Stop halts the peer's operations.
func (p *CorePeer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isServing {
		p.logger.Println("Peer is not running.")
		return
	}

	if err := p.indexClient.Deannounce(p.announcedAddr); err != nil {
		p.logger.Printf("Warning: Failed to de-announce from index server: %v", err)
		return
	}

	p.tcpServer.Stop()
	p.isServing = false
	p.logger.Println("Peer stopped.")
}

// GetConfig returns a copy of the current configuration of the CorePeer.
func (p *CorePeer) GetConfig() CorePeerConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// UpdateConfig updates the CorePeer configuration with the provided new configuration.
// It handles changes to share directory, serve port, public port, and index URL (in that order).
// The method is not fully atomic - partial updates may occur if an error happens during processing.
func (p *CorePeer) UpdateConfig(ctx context.Context, newConfig CorePeerConfig) (CorePeerConfig, error) {
	// NOTE: It may be better if this method is atomic, i.e. it should not allow partial updates if an error occur.
	// PERF: If this method is called with changes to multiple fields, redundant deannounce/reannounce are performed.
	p.mu.Lock()
	defer p.mu.Unlock()

	// Defensive check: if not serving but listener exists, ensure it's stopped.
	if !p.isServing && p.tcpServer.IsRunning() {
		p.logger.Println("Warning: Peer is not serving, but listener exists. Stopping listener.")
		p.tcpServer.Stop()
	}

	if p.isServing {
		p.logger.Println("Warning: Updating configuration while peer is serving. Some changes may require a restart of the peer to take full effect.")
	}

	oldConfig := p.config

	if err := p.handleShareDirChange(ctx, oldConfig.ShareDir, newConfig.ShareDir); err != nil {
		return p.config, fmt.Errorf("failed to handle share directory change: %w", err)
	}

	if err := p.handleServePortChange(oldConfig.ServePort, newConfig.ServePort); err != nil {
		return p.config, fmt.Errorf("failed to handle serve port change: %w", err)
	}

	oldPublicPort := p.config.PublicPort
	var newPublicPort int
	if newConfig.PublicPort != 0 {
		newPublicPort = newConfig.PublicPort
	} else {
		newPublicPort = p.config.ServePort
	}
	if err := p.handlePublicPortChange(oldPublicPort, newPublicPort); err != nil {
		return p.config, fmt.Errorf("failed to handle public port change: %w", err)
	}

	if err := p.handleIndexURLChange(oldConfig.IndexURL, newConfig.IndexURL); err != nil {
		return p.config, fmt.Errorf("failed to handle index URL change: %w", err)
	}

	p.logger.Printf(
		"CorePeer configuration updated. IndexURL: %s, ServePort: %d, PublicPort: %d, ShareDir: %s",
		p.config.IndexURL, p.config.ServePort, p.config.PublicPort, p.config.ShareDir,
	)
	return p.config, nil
}

// IsServing returns true if the peer is currently active.
func (p *CorePeer) IsServing() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isServing
}

// GetSharedFiles returns a copy of the currently shared files.
func (p *CorePeer) GetSharedFiles() []protocol.FileMeta {
	return p.fileManager.GetSharedFiles()
}

// FetchFilesFromIndex retrieves all available file metadata from the index server.
func (p *CorePeer) FetchFilesFromIndex(ctx context.Context) ([]protocol.FileMeta, error) {
	return p.indexClient.FetchAllFiles(ctx)
}

// QueryPeersForFile retrieves a list of peers that are serving a specific file by its checksum.
func (p *CorePeer) QueryPeersForFile(ctx context.Context, checksum string) ([]netip.AddrPort, error) {
	return p.indexClient.QueryFilePeers(ctx, checksum)
}

// FetchFileFromIndex retrieves the metadata for a specific file from the index server.
func (p *CorePeer) FetchFileFromIndex(ctx context.Context, fileChecksum string) (protocol.FileMeta, error) {
	return p.indexClient.FetchOneFile(ctx, fileChecksum)
}

// DownloadFile orchestrates the download of a file in chunks from multiple peers.
// It manages sessions with peers and retries failed chunks.
func (p *CorePeer) DownloadFile(ctx context.Context, fileMeta protocol.FileMeta, savePath string) error {
	return p.downloadManager.DownloadFile(ctx, fileMeta, savePath)
}

// handleIndexURLChange manages the transition from an old index URL to a new one.
// If de-announcement from the old index fails, the configuration is rolled back
// and an error is returned. The method is safe to call with identical old and
// new URLs (no-op) or empty URLs.
func (p *CorePeer) handleIndexURLChange(oldIndexURL, newIndexURL string) error {
	if oldIndexURL == newIndexURL {
		return nil
	}
	p.config.IndexURL = newIndexURL

	if p.isServing && oldIndexURL != "" {
		if err := p.indexClient.Deannounce(p.announcedAddr); err != nil {
			p.config.IndexURL = oldIndexURL
			return fmt.Errorf("failed to de-announce from old index URL %s: %w", oldIndexURL, err)
		}
		p.logger.Printf("De-announced from old index URL: %s", oldIndexURL)
	}

	p.indexClient.SetIndexURL(newIndexURL)
	p.logger.Printf("IndexClient updated with new URL: %s", newIndexURL)

	if p.isServing && newIndexURL != "" {
		if err := p.indexClient.Announce(p.announcedAddr, p.fileManager.GetSharedFiles()); err != nil {
			return fmt.Errorf("failed to re-announce to new index URL %s: %w", newIndexURL, err)
		}
	}

	return nil
}

// handleShareDirChange processes a change in the peer's shared directory configuration.
// It validates the new directory path, updates internal state, and handles the transition
// between different sharing states (no directory, old directory, new directory).
// It returns an error if path resolution fails or server state update fails.
// On server state update failure, the share directory is reverted to oldShareDir.
// Scanning and re-announcement failures are logged as warnings but don't cause method failure.
func (p *CorePeer) handleShareDirChange(ctx context.Context, oldShareDir, newShareDir string) error {
	newAbsShareDir := ""
	if newShareDir != "" {
		var err error
		newAbsShareDir, err = filepath.Abs(newShareDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for share directory %s: %w", newShareDir, err)
		}
	}

	shareDirChanged := oldShareDir != newAbsShareDir
	if !shareDirChanged {
		return nil
	}

	p.logger.Printf("Share directory changing from '%s' to '%s'", oldShareDir, newAbsShareDir)
	p.fileManager.Reset()
	p.config.ShareDir = newAbsShareDir

	wasPreviouslySharingFromDir := oldShareDir != ""
	willNowShareFromDir := newAbsShareDir != ""

	if willNowShareFromDir {
		if err := p.fileManager.UpdateShareDir(ctx, newAbsShareDir); err != nil {
			p.logger.Printf("Warning: Failed to scan share directory %s: %v. Shared files will be empty.", newAbsShareDir, err)
		}
	} else {
		p.logger.Println("Share directory is now empty. No files are shared from a directory.")
	}

	if p.isServing {
		if err := p.tcpServer.UpdateState(ctx, willNowShareFromDir, wasPreviouslySharingFromDir, p.config.ServePort); err != nil {
			p.config.ShareDir = oldShareDir
			return err
		}
		if err := p.indexClient.Reannounce(p.announcedAddr, p.fileManager.GetSharedFiles()); err != nil {
			p.logger.Printf("Warning: Failed to re-announce after share directory update: %v", err)
		}
	}

	return nil
}

// handleServePortChange updates the peer's serve port configuration and restarts
// the TCP server if necessary. It validates the new port, updates the configuration,
// and gracefully restarts the TCP server if the peer is currently serving files.
// The method handles cases where the port remains unchanged or when the peer is
// not actively serving. Returns an error if port validation fails or if the
// TCP server cannot be restarted on the new port.
func (p *CorePeer) handleServePortChange(oldServePort, newServePort int) error {
	if oldServePort == newServePort && newServePort != 0 {
		p.logger.Printf("Serve port remains unchanged: %d", oldServePort)
		return nil
	}

	var err error
	p.config.ServePort, err = p.processServerPortConfig(newServePort)
	if err != nil {
		return fmt.Errorf("failed to process new serve port %d: %w", newServePort, err)
	}
	p.logger.Printf("Serve port changing from %d to %d", oldServePort, newServePort)

	if !p.isServing {
		p.logger.Println("Peer is not serving, no need to restart TCP server.")
		return nil
	}
	p.logger.Printf("Serve port changing, restarting TCP server")

	p.tcpServer.Stop()
	if p.config.ShareDir != "" {
		if err := p.tcpServer.Start(p.rootCtx, p.config.ServePort); err != nil {
			return fmt.Errorf("failed to restart TCP server on new port %d: %w", newServePort, err)
		}
	}

	return nil
}

// handlePublicPortChange manages the transition from an old public port to a new one.
// It updates the announced address and re-announces to the index server if the peer
// is currently serving. If de-announcement fails, the configuration is rolled back.
func (p *CorePeer) handlePublicPortChange(oldPublicPort, newPublicPort int) error {
	if oldPublicPort == newPublicPort {
		p.logger.Printf("Public port remains unchanged: %d", oldPublicPort)
		return nil
	}
	p.logger.Printf("Public port changing from %d to %d", oldPublicPort, newPublicPort)
	p.config.PublicPort = newPublicPort

	if !p.isServing {
		p.logger.Println("Peer is not serving, no need to re-announce.")
		return nil
	}

	if err := p.indexClient.Deannounce(p.announcedAddr); err != nil {
		p.config.PublicPort = oldPublicPort
		return fmt.Errorf("failed to de-announce from index server: %w", err)
	}
	p.logger.Printf("De-announced from index server due to public port change from %d to %d", oldPublicPort, newPublicPort)

	p.announcedAddr = netip.AddrPortFrom(p.announcedAddr.Addr(), uint16(newPublicPort))
	if err := p.indexClient.Announce(p.announcedAddr, p.fileManager.GetSharedFiles()); err != nil {
		return fmt.Errorf("failed to re-announce to index server with new public port %d: %w", newPublicPort, err)
	}
	p.logger.Printf("Re-announced to index server with new public port: %d", newPublicPort)

	return nil
}

// processServerPortConfig validates and configures the server port for the CorePeer.
// It accepts a port number and returns the actual port to be used along with any error.
func (p *CorePeer) processServerPortConfig(port int) (int, error) {
	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port number: %d. Must be between 0 and 65535", port)
	}

	if port == 0 {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, fmt.Errorf("failed to listen on random port: %w", err)
		}
		defer l.Close()
		port = l.Addr().(*net.TCPAddr).Port
		p.logger.Printf("Assigned random port: %d", port)
	} else {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return 0, fmt.Errorf("failed to listen on port %d: %w", port, err)
		}
		defer l.Close()
		p.logger.Printf("Listening on port: %d", port)
	}

	return port, nil
}
