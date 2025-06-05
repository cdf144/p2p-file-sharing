package corepeer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
	return &CorePeer{
		config: cfg,
		logger: log.New(log.Writer(), "[corepeer] ", log.LstdFlags|log.Lmsgprefix),
	}
}

// Start initializes and starts the peer's operations.
func (p *CorePeer) Start(ctx context.Context) (string, error) {
	p.mu.RLock()
	if p.isServing {
		p.mu.RUnlock()
		return "Peer is already running", nil
	}
	p.mu.RUnlock()

	if ctx == nil {
		ctx = context.Background()
	}

	var err error
	// 1. Scan files
	if p.config.ShareDir != "" {
		p.mu.Lock()
		absShareDir, err := filepath.Abs(p.config.ShareDir)
		if err != nil {
			p.mu.Unlock()
			return "", fmt.Errorf("failed to get absolute path for share directory %s: %w", p.config.ShareDir, err)
		}
		p.config.ShareDir = absShareDir

		p.logger.Printf("Scanning directory: %s", p.config.ShareDir)
		scannedFiles, scannedPaths, err := ScanDirectory(ctx, p.config.ShareDir, p.logger)
		if err != nil {
			p.logger.Printf(
				"Warning: Failed to scan share directory %s: %v. No files will be shared.",
				p.config.ShareDir, err,
			)
			p.sharedFiles = []protocol.FileMeta{}
			p.sharedFilePaths = make(map[string]string)
		} else {
			p.sharedFiles = scannedFiles
			p.sharedFilePaths = scannedPaths
			p.logger.Printf("Scanned %d files from %s", len(p.sharedFiles), p.config.ShareDir)
		}
	} else {
		p.logger.Println("No share directory specified. No files will be shared.")
		p.sharedFiles = []protocol.FileMeta{}
		p.sharedFilePaths = make(map[string]string)
	}
	p.mu.Unlock()

	// 2. Determine IP address
	announceIP, err := DetermineMachineIP()
	if err != nil {
		return "", fmt.Errorf("failed to determine machine IP address: %w", err)
	}
	p.logger.Printf("Determined machine IP: %s", announceIP.String())

	// 3. Determine and start listening on serve port
	servePort := p.config.ServePort
	if servePort == 0 {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return "", fmt.Errorf("failed to listen on a random port: %w", err)
		}
		p.servePort = uint16(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		p.logger.Printf("No serve port specified. Listening on randomly assigned port: %d", p.servePort)
	} else {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", servePort))
		if err != nil {
			return "", fmt.Errorf("failed to listen on port %d: %w", servePort, err)
		}
		l.Close()
		p.servePort = uint16(servePort)
		p.logger.Printf("Listening on specified port: %d", p.servePort)
	}

	// 4. Determine announced port
	var announcePort uint16
	if p.config.PublicPort != 0 {
		announcePort = uint16(p.config.PublicPort)
	} else {
		announcePort = p.servePort
	}
	p.logger.Printf("Announcing with port: %d", announcePort)

	// 5. Announce to index server
	p.announcedAddr = netip.AddrPortFrom(announceIP, announcePort)
	if err := p.announce(ctx); err != nil {
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}

	// 6. Start serving files in a goroutine
	p.serveCtx, p.serveCancel = context.WithCancel(ctx)
	if p.config.ShareDir != "" {
		go p.serveFiles(p.serveCtx, p.config.ShareDir)
	} else {
		p.logger.Println("No files to share, TCP server not started for file sharing.")
	}

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
func (p *CorePeer) Stop(ctx context.Context) {
	p.mu.Lock()
	if !p.isServing {
		p.mu.Unlock()
		p.logger.Println("Peer is not running.")
		return
	}
	p.isServing = false
	p.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	if p.serveCancel != nil {
		p.serveCancel()
	}
	p.logger.Println("Serving stopped.")

	if err := p.deannounce(ctx); err != nil {
		p.logger.Printf("Warning: Failed to de-announce from index server: %v", err)
	} else {
		p.logger.Println("Successfully de-announced from index server.")
	}
}

// GetConfig returns a copy of the current configuration of the CorePeer.
func (p *CorePeer) GetConfig() CorePeerConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

func (p *CorePeer) UpdateConfig(cfg CorePeerConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isServing {
		p.logger.Println("Warning: Updating configuration while peer is serving. Some changes may require a restart of the peer to take full effect.")
	}

	p.config.IndexURL = cfg.IndexURL
	p.config.ServePort = cfg.ServePort
	p.config.PublicPort = cfg.PublicPort
	// TODO: Probably change to also scan the directory if it changes.
	if p.config.ShareDir != cfg.ShareDir {
		p.config.ShareDir = cfg.ShareDir
		p.logger.Printf(
			"Share directory in config updated to: %s. This will be used on next Start() or manual file scan.",
			p.config.ShareDir,
		)
	}

	p.logger.Printf(
		"CorePeer configuration updated. IndexURL: %s, ServePort: %d, PublicPort: %d",
		p.config.IndexURL, p.config.ServePort, p.config.PublicPort,
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

// SetSharedDirectory sets the directory to be shared and re-scans for files in it.
// If shareDir is empty, it clears the current shared files.
func (p *CorePeer) SetSharedDirectory(ctx context.Context, shareDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	if shareDir == "" {
		p.logger.Println("Clearing shared directory.")
		p.sharedFiles = []protocol.FileMeta{}
		p.sharedFilePaths = make(map[string]string)
		if p.isServing {
			go p.reannounce(context.Background())
		}
		return nil
	}

	absShareDir, err := filepath.Abs(shareDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for share directory %s: %w", shareDir, err)
	}
	p.config.ShareDir = absShareDir

	p.logger.Printf("Setting new share directory: %s", p.config.ShareDir)
	scannedFiles, scannedPaths, scanErr := ScanDirectory(ctx, p.config.ShareDir, p.logger)
	if scanErr != nil {
		p.sharedFiles = []protocol.FileMeta{}
		p.sharedFilePaths = make(map[string]string)
		return fmt.Errorf("failed to scan new share directory %s: %w", p.config.ShareDir, scanErr)
	}
	p.sharedFiles = scannedFiles
	p.sharedFilePaths = scannedPaths
	p.logger.Printf("Updated shared files: %d files found.", len(p.sharedFiles))

	if p.isServing {
		go p.reannounce(context.Background())
	}
	return nil
}

func (p *CorePeer) FetchFilesFromIndex(ctx context.Context) ([]protocol.FileMeta, error) {
	if p.config.IndexURL == "" {
		return nil, fmt.Errorf("index URL is not configured")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	queryURL := p.config.IndexURL + "/files"
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create query request: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned status %s: %s", resp.Status, string(respBody))
	}

	var files []protocol.FileMeta
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	p.logger.Printf("Fetched %d files from index server %s", len(files), p.config.IndexURL)
	return files, nil
}

func (p *CorePeer) QueryPeersForFile(ctx context.Context, checksum string) ([]netip.AddrPort, error) {
	if p.config.IndexURL == "" {
		return nil, fmt.Errorf("index URL is not configured")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	queryURL := fmt.Sprintf("%s/files/%s/peers", p.config.IndexURL, checksum)
	reqCtx, cancelReq := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create query request: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned status %s: %s", resp.Status, string(respBody))
	}

	var peerAddrs []netip.AddrPort
	if err := json.NewDecoder(resp.Body).Decode(&peerAddrs); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	p.logger.Printf("Found %d peers for file with checksum %s", len(peerAddrs), checksum)
	return peerAddrs, nil
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

// UpdateSharedFiles can be called if the shared directory content changes.
// This would typically re-scan the directory.
func (p *CorePeer) UpdateSharedFiles(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	if p.config.ShareDir == "" {
		p.logger.Println("No share directory configured, cannot update shared files.")
		p.sharedFiles = []protocol.FileMeta{}
		p.sharedFilePaths = make(map[string]string)
		if p.isServing {
			go p.reannounce(context.Background())
		}
		return nil
	}

	p.logger.Printf("Re-scanning directory: %s", p.config.ShareDir)
	newFiles, newFilePaths, err := ScanDirectory(ctx, p.config.ShareDir, p.logger)
	if err != nil {
		p.logger.Printf("Error re-scanning directory %s: %v", p.config.ShareDir, err)
		p.sharedFiles = []protocol.FileMeta{}
		p.sharedFilePaths = make(map[string]string)
		return fmt.Errorf("failed to re-scan directory: %w", err)
	}
	p.sharedFiles = newFiles
	p.sharedFilePaths = newFilePaths
	p.logger.Printf("Updated shared files: %d files found.", len(p.sharedFiles))

	if p.isServing {
		if err := p.reannounce(ctx); err != nil {
			p.logger.Printf("Failed to re-announce after updating shared files: %v", err)
			return fmt.Errorf("failed to re-announce after updating shared files: %w", err)
		}
		p.logger.Println("Successfully re-announced to index server after updating shared files.")
	}
	return nil
}

// announce sends peer information to the index server.
func (p *CorePeer) announce(ctx context.Context) error {
	if p.config.IndexURL == "" {
		p.logger.Println("IndexURL is not configured. Skipping announce.")
		return nil
	}

	peerInfo := protocol.PeerInfo{
		Address: p.announcedAddr,
		Files:   p.sharedFiles,
	}
	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info: %w", err)
	}

	announceURL := p.config.IndexURL + "/peers/announce"
	reqCtx, cancelReq := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "POST", announceURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned status %s: %s", resp.Status, string(respBody))
	}
	p.logger.Printf("Successfully announced to index server: %s", announceURL)
	return nil
}

func (p *CorePeer) deannounce(ctx context.Context) error {
	if p.config.IndexURL == "" {
		p.logger.Println("IndexURL is not configured. Skipping de-announce.")
		return nil
	}

	peerInfo := protocol.PeerInfo{
		Address: p.announcedAddr,
		Files:   p.sharedFiles,
	}
	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info for de-announce: %w", err)
	}

	deannounceURL := p.config.IndexURL + "/peers/deannounce"
	reqCtx, cancelReq := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "POST", deannounceURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create de-announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("de-announce request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("index server returned status %s for de-announce: %s", resp.Status, string(respBody))
	}
	p.logger.Printf("Successfully de-announced from index server: %s", deannounceURL)
	return nil
}

func (p *CorePeer) reannounce(ctx context.Context) error {
	if err := p.deannounce(ctx); err != nil {
		return fmt.Errorf("failed to de-announce before re-announcing: %w", err)
	}
	if err := p.announce(ctx); err != nil {
		return fmt.Errorf("failed to re-announce after de-announcing: %w", err)
	}
	p.logger.Println("Successfully re-announced to index server.")
	return nil
}

// serveFiles starts the TCP server to listen for file requests.
func (p *CorePeer) serveFiles(ctx context.Context, shareDir string) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", p.servePort))
	if err != nil {
		p.logger.Printf("Failed to start TCP file server on port %d: %v", p.servePort, err)
		return
	}

	p.mu.Lock()
	p.isServing = true
	p.mu.Unlock()

	p.logger.Printf("Starting TCP file server on %s for directory %s", listener.Addr().String(), shareDir)

	go func() {
		<-ctx.Done()
		p.logger.Printf("Shutting down TCP file server on %s", listener.Addr().String())
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				p.logger.Printf("TCP server on %s stopped gracefully.", listener.Addr().String())
				return
			default:
				p.logger.Printf("Failed to accept connection: %v", err)
				if ne, ok := err.(net.Error); ok && !ne.Timeout() {
					p.logger.Printf("Permanent error accepting connections: %v. Stopping server.", err)
					return
				}
				continue
			}
		}
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
		p.logger.Printf("Failed to read request from %s: %v", conn.RemoteAddr(), err)
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
		p.logger.Printf("File not found for checksum %s requested by %s: %v", checksum, conn.RemoteAddr(), err)
		conn.Write(fmt.Appendf(nil, "%s File not found for checksum %s\n", protocol.ERROR.String(), checksum))
		return
	}

	// NOTE: Arbitrary timeout for file transfer.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	_, err = conn.Write(fmt.Appendf(nil, "%s %d\n", protocol.FILE_DATA.String(), localFile.Size))
	if err != nil {
		p.logger.Printf("Failed to send file data header to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}

	fileHandle, err := os.Open(localFile.Path)
	if err != nil {
		p.logger.Printf("Failed to open file %s: %v", localFile.Path, err)
		conn.Write(fmt.Appendf(nil, "%s Failed to open file\n", protocol.ERROR))
		return
	}
	defer fileHandle.Close()

	sent, err := io.Copy(conn, fileHandle)
	if err != nil {
		p.logger.Printf("Failed to send file %s to %s: %v", localFile.Name, conn.RemoteAddr(), err)
		return
	}

	p.logger.Printf("Successfully sent file %s (%d bytes) to %s", localFile.Name, sent, conn.RemoteAddr())
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
		p.logger.Printf("Error stating file %s (checksum %s): %v. File might be missing.", filePath, checksum, err)
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
