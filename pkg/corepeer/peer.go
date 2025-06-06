package corepeer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
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
	config          CorePeerConfig
	indexClient     *IndexClient
	rootCtx         context.Context // Root context for the peer's lifetime, from Start()
	listener        net.Listener
	isServing       bool
	servePort       uint16
	serveCtx        context.Context    // Own context for managing internal serving lifecycle
	serveCancel     context.CancelFunc // Function to cancel serveCtx
	sharedFiles     []protocol.FileMeta
	sharedFilePaths map[string]string // map[checksum]fullFilePath for quick local lookups
	announcedAddr   netip.AddrPort
	mu              sync.RWMutex
	logger          *log.Logger
}

// NewCorePeer creates a new CorePeer instance.
func NewCorePeer(cfg CorePeerConfig) *CorePeer {
	logger := log.New(log.Writer(), "[corepeer] ", log.LstdFlags|log.Lmsgprefix)
	return &CorePeer{
		config:      cfg,
		logger:      logger,
		indexClient: NewIndexClient(cfg.IndexURL, logger),
	}
}

// Start initializes and starts the peer's operations.
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
	p.servePort = uint16(p.config.ServePort)
	p.startTCPServer(p.rootCtx)

	// 3. Announce to index server
	var announcePort uint16
	if p.config.PublicPort != 0 {
		announcePort = uint16(p.config.PublicPort)
	} else {
		announcePort = p.servePort
	}
	p.logger.Printf("Announcing with port: %d", announcePort)

	p.announcedAddr = netip.AddrPortFrom(announceIP, announcePort)
	if err := p.indexClient.Announce(p.announcedAddr, p.sharedFiles); err != nil {
		if p.listener != nil {
			p.listener.Close()
			p.listener = nil
		}
		p.serveCancel()
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}

	p.isServing = true
	statusMsg := fmt.Sprintf(
		"Peer started. Sharing from: %s. IP: %s, Serving Port: %d, Announced Port: %d. Files shared: %d",
		p.config.ShareDir,
		p.announcedAddr.Addr(),
		p.servePort,
		p.announcedAddr.Port(),
		len(p.sharedFiles),
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

	p.stopTCPServer()
	p.isServing = false
	p.logger.Println("Peer stopped.")
}

// GetConfig returns a copy of the current configuration of the CorePeer.
func (p *CorePeer) GetConfig() CorePeerConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

func (p *CorePeer) UpdateConfig(ctx context.Context, newConfig CorePeerConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Defensive check: if not serving but listener exists, ensure it's stopped.
	if !p.isServing && p.listener != nil {
		p.logger.Println("Warning: Peer is not serving, but listener exists. Stopping listener.")
		p.stopTCPServer()
	}

	if p.isServing {
		p.logger.Println("Warning: Updating configuration while peer is serving. Some changes may require a restart of the peer to take full effect.")
	}

	oldConfig := p.config

	// TODO: If servePort changes, we need to stop the listener and start a new one.
	p.config.IndexURL = newConfig.IndexURL
	p.config.ServePort = newConfig.ServePort
	p.config.PublicPort = newConfig.PublicPort

	if err := p.handleIndexURLChange(oldConfig.IndexURL, newConfig.IndexURL); err != nil {
		return err
	}

	shareDirChanged, err := p.handleShareDirChange(ctx, oldConfig.ShareDir, newConfig.ShareDir)
	if err != nil {
		return err
	}

	if p.isServing && !shareDirChanged {
		if err := p.handleConfigReannouncement(oldConfig, newConfig); err != nil {
			return err
		}
	}

	p.logger.Printf(
		"CorePeer configuration updated. IndexURL: %s, ServePort: %d, PublicPort: %d, ShareDir: %s",
		p.config.IndexURL, p.config.ServePort, p.config.PublicPort, p.config.ShareDir,
	)
	return nil
}

// IsServing returns true if the peer is currently active.
func (p *CorePeer) IsServing() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isServing
}

// GetSharedFiles returns a copy of the currently shared files.
func (p *CorePeer) GetSharedFiles() []protocol.FileMeta {
	p.mu.RLock()
	defer p.mu.RUnlock()
	filesCopy := make([]protocol.FileMeta, len(p.sharedFiles))
	copy(filesCopy, p.sharedFiles)
	return filesCopy
}

func (p *CorePeer) FetchFilesFromIndex(ctx context.Context) ([]protocol.FileMeta, error) {
	return p.indexClient.FetchAllFiles(ctx)
}

// QueryPeersForFile retrieves a list of peers that are serving a specific file by its checksum.
func (p *CorePeer) QueryPeersForFile(ctx context.Context, checksum string) ([]netip.AddrPort, error) {
	return p.indexClient.QueryFilePeers(ctx, checksum)
}

// DownloadFileFromPeer downloads a file from another peer.
func (p *CorePeer) DownloadFileFromPeer(
	peerAddr netip.AddrPort,
	fileChecksum, fileName string,
	savePath string,
) error {
	conn, err := net.DialTimeout(
		"tcp",
		peerAddr.String(),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("failed to connect to peer %s: %w", peerAddr, err)
	}
	defer conn.Close()

	_, err = conn.Write(fmt.Appendf(nil, "%s %s\n", protocol.FILE_REQUEST.String(), fileChecksum))
	if err != nil {
		return fmt.Errorf("failed to send request to peer: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read response header from peer: %w", err)
	}

	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, protocol.ERROR.String()+" ") {
		return fmt.Errorf("peer error: %s", strings.TrimPrefix(response, protocol.ERROR.String()+" "))
	}
	if !strings.HasPrefix(response, protocol.FILE_DATA.String()+" ") {
		return fmt.Errorf("unexpected response from peer: %s", response)
	}

	fileSizeStr := strings.TrimPrefix(response, protocol.FILE_DATA.String()+" ")
	fileSize, err := strconv.ParseInt(fileSizeStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid file size in response '%s': %w", fileSizeStr, err)
	}

	// NOTE: Arbitrary timeout for file transfer.
	conn.SetReadDeadline(time.Now().Add(30 * time.Minute))

	file, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", savePath, err)
	}
	defer file.Close()

	p.logger.Printf(
		"Downloading file %s (%d bytes) from %s to %s",
		fileName,
		fileSize,
		peerAddr,
		savePath,
	)

	hash := sha256.New()
	teeReader := io.TeeReader(reader, hash) // Read from connection, also write to hash
	limitedTeeReader := io.LimitReader(teeReader, fileSize)

	written, err := io.Copy(file, limitedTeeReader)
	if err != nil {
		os.Remove(savePath)
		return fmt.Errorf("failed to download file content: %w", err)
	}
	if written != fileSize {
		os.Remove(savePath)
		return fmt.Errorf("incomplete download: expected %d bytes, got %d", fileSize, written)
	}

	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if actualChecksum != fileChecksum {
		os.Remove(savePath)
		return fmt.Errorf("checksum mismatch: expected %s, actual %s", fileChecksum, actualChecksum)
	}

	p.logger.Printf("Successfully downloaded and verified file %s to %s", fileName, savePath)
	return nil
}

// acceptConnections starts the TCP file server and handles incoming connections.
func (p *CorePeer) acceptConnections(ctx context.Context) {
	if p.listener == nil {
		p.logger.Println("Error: acceptConnections called with a nil listener.")
		return
	}
	p.logger.Printf("Accepting connections on %s", p.listener.Addr().String())

	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				p.logger.Println("Context cancelled, server accept loop stopping gracefully.")
				return
			default:
				if strings.Contains(err.Error(), "use of closed network connection") {
					p.logger.Println("Listener closed, server accept loop stopping.")
				} else {
					p.logger.Printf("Error accepting connection: %v. Server accept loop stopping.", err)
				}
				return
			}
		}
		p.logger.Printf("Accepted connection from %s", conn.RemoteAddr().String())
		go p.handleFileRequest(conn)
	}
}

// handleFileRequest processes an incoming file request.
func (p *CorePeer) handleFileRequest(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)
	request, err := reader.ReadString('\n')
	if err != nil {
		p.logger.Printf("Warning: Failed to read request from %s: %v", conn.RemoteAddr(), err)
		return
	}

	request = strings.TrimSpace(request)
	p.logger.Printf("Received request from %s: %s", conn.RemoteAddr(), request)
	if !strings.HasPrefix(request, protocol.FILE_REQUEST.String()+" ") {
		conn.Write(fmt.Appendf(nil, "%s Invalid request format\n", protocol.ERROR.String()))
		return
	}
	checksum := strings.TrimSpace(strings.TrimPrefix(request, protocol.FILE_REQUEST.String()+" "))

	localFile, err := p.getLocalFileInfoByChecksum(checksum)
	if err != nil {
		p.logger.Printf("Warning: File not found for checksum %s requested by %s: %v", checksum, conn.RemoteAddr(), err)
		conn.Write(fmt.Appendf(nil, "%s File not found for checksum %s\n", protocol.ERROR.String(), checksum))
		return
	}

	// NOTE: Arbitrary timeout for file transfer.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	_, err = conn.Write(fmt.Appendf(nil, "%s %d\n", protocol.FILE_DATA.String(), localFile.Size))
	if err != nil {
		p.logger.Printf("Warning: Failed to send file data header to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}

	fileHandle, err := os.Open(localFile.Path)
	if err != nil {
		p.logger.Printf("Warning: Failed to open file %s: %v", localFile.Path, err)
		conn.Write(fmt.Appendf(nil, "%s Failed to open file\n", protocol.ERROR))
		return
	}
	defer fileHandle.Close()

	sent, err := io.Copy(conn, fileHandle)
	if err != nil {
		p.logger.Printf("Warning: Failed to send file %s to %s: %v", localFile.Name, conn.RemoteAddr(), err)
		return
	}

	p.logger.Printf("Successfully sent file %s (%d bytes) to %s", localFile.Name, sent, conn.RemoteAddr())
}

// handleIndexURLChange manages IndexClient updates when IndexURL changes
func (p *CorePeer) handleIndexURLChange(oldIndexURL, newIndexURL string) error {
	if oldIndexURL == newIndexURL {
		return nil
	}

	if p.isServing && oldIndexURL != "" {
		if err := p.indexClient.Deannounce(p.announcedAddr); err != nil {
			return fmt.Errorf("failed to de-announce from old index URL %s: %w", oldIndexURL, err)
		}
		p.logger.Printf("De-announced from old index URL: %s", oldIndexURL)
	}

	p.indexClient = NewIndexClient(newIndexURL, p.logger)
	p.logger.Printf("IndexClient updated with new URL: %s", newIndexURL)

	if p.isServing && newIndexURL != "" {
		if err := p.indexClient.Announce(p.announcedAddr, p.sharedFiles); err != nil {
			return fmt.Errorf("failed to re-announce to new index URL %s: %w", newIndexURL, err)
		}
	}

	return nil
}

// handleShareDirChange manages share directory updates and returns whether the directory changed
func (p *CorePeer) handleShareDirChange(ctx context.Context, oldShareDir, newShareDir string) (bool, error) {
	newAbsShareDir := ""
	if newShareDir != "" {
		var err error
		newAbsShareDir, err = filepath.Abs(newShareDir)
		if err != nil {
			return false, fmt.Errorf("failed to get absolute path for share directory %s: %w", newShareDir, err)
		}
	}

	shareDirChanged := oldShareDir != newAbsShareDir
	if !shareDirChanged {
		return false, nil
	}

	p.logger.Printf("Share directory changing from '%s' to '%s'", oldShareDir, newAbsShareDir)
	p.config.ShareDir = newAbsShareDir

	p.resetSharedFiles()

	wasPreviouslySharingFromDir := oldShareDir != ""
	willNowShareFromDir := newAbsShareDir != ""

	if willNowShareFromDir {
		if err := p.scanAndUpdateSharedFiles(ctx); err != nil {
			p.logger.Printf("Warning: Failed to scan share directory %s: %v. Shared files will be empty.", p.config.ShareDir, err)
		}
	} else {
		p.logger.Println("Share directory is now empty. No files are shared from a directory.")
	}

	if p.isServing {
		if err := p.updateTCPServerState(willNowShareFromDir, wasPreviouslySharingFromDir); err != nil {
			return true, err
		}

		if err := p.indexClient.Reannounce(p.announcedAddr, p.sharedFiles); err != nil {
			p.logger.Printf("Warning: Failed to re-announce after share directory update: %v", err)
		}
	}

	return true, nil
}

// handleConfigReannouncement handles re-announcement when IndexURL or PublicPort changes
func (p *CorePeer) handleConfigReannouncement(oldConfig, newConfig CorePeerConfig) error {
	oldPublicPort := p.getEffectivePublicPort(oldConfig)
	newPublicPort := p.getEffectivePublicPort(newConfig)

	publicPortChanged := oldPublicPort != newPublicPort
	indexURLChanged := oldConfig.IndexURL != newConfig.IndexURL

	if publicPortChanged || (indexURLChanged && newConfig.IndexURL != "") {
		if publicPortChanged {
			p.announcedAddr = netip.AddrPortFrom(p.announcedAddr.Addr(), uint16(newPublicPort))
			p.logger.Printf("Updated announced port to: %d", newPublicPort)
		}

		p.logger.Printf(
			"Re-announcing due to configuration changes (PublicPort changed: %v, IndexURL changed: %v)",
			publicPortChanged, indexURLChanged,
		)

		if err := p.indexClient.Reannounce(p.announcedAddr, p.sharedFiles); err != nil {
			return fmt.Errorf("failed to re-announce after config change: %w", err)
		}

		p.logger.Printf("Successfully re-announced with updated configuration")
	}

	return nil
}

// stopTCPServer stops the TCP file server
func (p *CorePeer) stopTCPServer() {
	if p.serveCancel != nil {
		p.serveCancel()
	}
	if p.listener != nil {
		err := p.listener.Close()
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			p.logger.Printf("Warning: Error closing listener: %v", err)
		}
		p.listener = nil
		p.logger.Println("TCP file server stopped.")
	}
}

// startTCPServer starts the TCP server for the current servePort
func (p *CorePeer) startTCPServer(ctx context.Context) error {
	if p.listener != nil {
		p.logger.Println("TCP server already running or listener already exists.")
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return fmt.Errorf("cannot start TCP server, context is not active or nil")
	}

	var l net.Listener
	var err error

	if p.servePort == 0 {
		l, err = net.Listen("tcp", ":0")
		if err != nil {
			return fmt.Errorf("failed to listen on random port: %w", err)
		}
		p.servePort = uint16(l.Addr().(*net.TCPAddr).Port)
		p.logger.Printf("TCP server started on randomly assigned port: %d", p.servePort)
	} else {
		l, err = net.Listen("tcp", fmt.Sprintf(":%d", p.servePort))
		if err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", p.servePort, err)
		}
		p.logger.Printf("TCP server started on port: %d", p.servePort)
	}

	p.listener = l
	p.serveCtx, p.serveCancel = context.WithCancel(ctx)
	go p.acceptConnections(p.serveCtx)

	return nil
}

// updateTCPServerState manages TCP server state based on sharing requirements
func (p *CorePeer) updateTCPServerState(shouldBeRunning, wasRunning bool) error {
	if shouldBeRunning && (!wasRunning || p.listener == nil) {
		p.logger.Printf("Starting TCP server for sharing directory: %s", p.config.ShareDir)
		if p.rootCtx == nil {
			p.logger.Println("Error: peerRootCtx is nil, cannot start TCP server. Peer might not have been started correctly.")
			return fmt.Errorf("peerRootCtx is nil, cannot start TCP server")
		}
		return p.startTCPServer(p.rootCtx)
	} else if !shouldBeRunning && wasRunning {
		p.logger.Println("Stopping TCP server - no directory to share.")
		p.stopTCPServer()
	}
	return nil
}

// resetSharedFiles clears the shared files state
func (p *CorePeer) resetSharedFiles() {
	p.sharedFiles = []protocol.FileMeta{}
	p.sharedFilePaths = make(map[string]string)
}

// scanAndUpdateSharedFiles scans the current share directory and updates shared files
func (p *CorePeer) scanAndUpdateSharedFiles(ctx context.Context) error {
	if p.config.ShareDir == "" {
		return nil
	}

	p.logger.Printf("Scanning directory: %s", p.config.ShareDir)
	scannedFiles, scannedPaths, err := ScanDirectory(ctx, p.config.ShareDir, p.logger)
	if err != nil {
		return err
	}

	p.sharedFiles = scannedFiles
	p.sharedFilePaths = scannedPaths
	p.logger.Printf("Scanned %d files from %s", len(p.sharedFiles), p.config.ShareDir)
	return nil
}

// getEffectivePublicPort returns the effective public port (PublicPort or ServePort if PublicPort is 0)
func (p *CorePeer) getEffectivePublicPort(config CorePeerConfig) int {
	if config.PublicPort != 0 {
		return config.PublicPort
	}
	return config.ServePort
}

func (p *CorePeer) getLocalFileInfoByChecksum(checksum string) (*LocalFileInfo, error) {
	p.mu.RLock()
	filePath, ok := p.sharedFilePaths[checksum]
	p.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("file with checksum %s not found in shared files index", checksum)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		p.logger.Printf("Warning: Error stating file %s (checksum %s): %v. File might be missing.", filePath, checksum, err)
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
