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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

// Config holds configuration for the CorePeer.
type Config struct {
	IndexURL    string
	ShareDir    string
	ServePort   int // 0 for random
	PublicPort  int // 0 to use ServePort for announcement
	Logger      *log.Logger
	FileScanCtx context.Context
}

// CorePeer manages the core P2P logic.
type CorePeer struct {
	config          Config
	sharedFiles     []protocol.FileMeta
	isServing       bool
	actualServePort uint16
	announcedIP     net.IP
	announcedPort   uint16
	listener        net.Listener
	serverCtx       context.Context
	serverCancel    context.CancelFunc
	mu              sync.RWMutex
	logger          *log.Logger
}

// NewCorePeer creates a new CorePeer instance.
func NewCorePeer(cfg Config) *CorePeer {
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(os.Stdout, "[corepeer] ", log.LstdFlags)
	}
	return &CorePeer{
		config: cfg,
		logger: logger,
	}
}

// Start initializes and starts the peer's operations.
func (p *CorePeer) Start() (string, error) {
	p.mu.RLock()
	if p.isServing {
		p.mu.RUnlock()
		return "Peer is already running", nil
	}
	p.mu.RUnlock()

	var err error
	// 1. Scan files
	if p.config.ShareDir != "" {
		absShareDir, err := filepath.Abs(p.config.ShareDir)
		if err != nil {
			return "", fmt.Errorf("failed to get absolute path for share directory %s: %w", p.config.ShareDir, err)
		}
		p.config.ShareDir = absShareDir

		p.logger.Printf("Scanning directory: %s", p.config.ShareDir)
		scanCtx := p.config.FileScanCtx
		if scanCtx == nil {
			scanCtx = context.Background()
		}
		p.sharedFiles, err = ScanDirectory(scanCtx, p.config.ShareDir)
		if err != nil {
			p.logger.Printf("Warning: Failed to scan share directory %s: %v. No files will be shared.", p.config.ShareDir, err)
			p.sharedFiles = []protocol.FileMeta{}
		} else {
			p.logger.Printf("Scanned %d files from %s", len(p.sharedFiles), p.config.ShareDir)
		}
	} else {
		p.logger.Println("No share directory specified. No files will be shared.")
		p.sharedFiles = []protocol.FileMeta{}
	}

	// 2. Determine IP address
	p.announcedIP, err = DetermineMachineIP()
	if err != nil {
		return "", fmt.Errorf("failed to determine machine IP address: %w", err)
	}
	p.logger.Printf("Determined machine IP: %s", p.announcedIP.String())

	// 3. Determine and start listening on serve port
	listenPort := p.config.ServePort
	if listenPort == 0 {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return "", fmt.Errorf("failed to listen on a random port: %w", err)
		}
		p.actualServePort = uint16(l.Addr().(*net.TCPAddr).Port)
		p.listener = l
		p.logger.Printf("No serve port specified. Listening on randomly assigned port: %d", p.actualServePort)
	} else {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
		if err != nil {
			return "", fmt.Errorf("failed to listen on port %d: %w", listenPort, err)
		}
		p.actualServePort = uint16(listenPort)
		p.listener = l
		p.logger.Printf("Listening on specified port: %d", p.actualServePort)
	}

	// 4. Determine announced port
	if p.config.PublicPort != 0 {
		p.announcedPort = uint16(p.config.PublicPort)
	} else {
		p.announcedPort = p.actualServePort
	}
	p.logger.Printf("Announcing with port: %d", p.announcedPort)

	// 5. Announce to index server
	peerInfo := protocol.PeerInfo{
		IP:    p.announcedIP,
		Port:  p.announcedPort,
		Files: p.sharedFiles,
	}
	if err := p.announce(p.config.IndexURL, peerInfo); err != nil {
		p.listener.Close()
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}

	// 6. Start serving files in a goroutine
	p.serverCtx, p.serverCancel = context.WithCancel(context.Background())
	if p.config.ShareDir != "" {
		go p.serveFiles(p.serverCtx, p.config.ShareDir, p.listener)
	} else {
		// NOTE: We might run other services other than just file sharing. For now, we'll close it if no files are shared.
		p.listener.Close()
		p.logger.Println("No files to share, TCP server not started for file sharing.")
	}

	p.mu.Lock()
	p.isServing = true
	p.mu.Unlock()

	statusMsg := fmt.Sprintf(
		"Peer started. Sharing from: %s. IP: %s, Serving Port: %d, Announced Port: %d. Files shared: %d",
		p.config.ShareDir,
		p.announcedIP.String(),
		p.actualServePort,
		p.announcedPort,
		len(p.sharedFiles),
	)
	p.logger.Println(statusMsg)
	return statusMsg, nil
}

// Stop halts the peer's operations.
func (p *CorePeer) Stop() {
	p.mu.Lock()
	if !p.isServing {
		p.mu.Unlock()
		p.logger.Println("Peer is not running.")
		return
	}
	p.isServing = false
	p.mu.Unlock()

	if p.serverCancel != nil {
		p.serverCancel()
	}
	if p.listener != nil {
		p.listener.Close()
	}
	p.logger.Println("Peer stopped.")
	// TODO: Send a de-announce to the index server (when implemented).
}

// GetConfig returns a copy of the current configuration of the CorePeer.
func (p *CorePeer) GetConfig() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// UpdateConfig updates the configuration of the CorePeer.
// This method should ideally be called when the peer is not serving.
// Changes to IndexURL, ShareDir, ServePort, PublicPort, and FileScanCtx will be applied.
// The logger is not updated by this method.
func (p *CorePeer) UpdateConfig(cfg Config) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isServing {
		p.logger.Println("Warning: Updating configuration while peer is serving. Some changes may require a restart of the peer to take full effect.")
	}

	p.config.IndexURL = cfg.IndexURL
	p.config.ServePort = cfg.ServePort
	p.config.PublicPort = cfg.PublicPort
	p.config.FileScanCtx = cfg.FileScanCtx

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
func (p *CorePeer) SetSharedDirectory(shareDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if shareDir == "" {
		p.logger.Println("Clearing shared directory.")
		p.sharedFiles = []protocol.FileMeta{}
		return nil
	}

	absShareDir, err := filepath.Abs(shareDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for share directory %s: %w", shareDir, err)
	}
	p.config.ShareDir = absShareDir

	p.logger.Printf("Setting new share directory: %s", p.config.ShareDir)
	scanCtx := p.config.FileScanCtx
	if scanCtx == nil {
		scanCtx = context.Background()
	}
	files, err := ScanDirectory(scanCtx, p.config.ShareDir)
	if err != nil {
		return fmt.Errorf("failed to scan new share directory %s: %w", p.config.ShareDir, err)
	}
	p.sharedFiles = files
	p.logger.Printf("Updated shared files: %d files found.", len(p.sharedFiles))

	return nil
}

// QueryPeersFromIndex fetches peer information from the index server.
func (p *CorePeer) QueryPeersFromIndex(indexURL string) ([]protocol.PeerInfo, error) {
	queryURL := indexURL + "/peers"
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

	var peers []protocol.PeerInfo
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	p.logger.Printf("Successfully queried index server: found %d peers", len(peers))
	return peers, nil
}

// DownloadFileFromPeer downloads a file from another peer.
func (p *CorePeer) DownloadFileFromPeer(
	peerIP string,
	peerPort uint16,
	fileChecksum, fileName string,
	savePath string,
) error {
	conn, err := net.DialTimeout(
		"tcp",
		net.JoinHostPort(peerIP, fmt.Sprintf("%d", peerPort)),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("failed to connect to peer %s:%d: %w", peerIP, peerPort, err)
	}
	defer conn.Close()

	_, err = conn.Write(fmt.Appendf(nil, "FILE_REQUEST %s\n", fileChecksum))
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
	if strings.HasPrefix(response, "ERROR ") {
		return fmt.Errorf("peer error: %s", strings.TrimPrefix(response, "ERROR "))
	}
	if !strings.HasPrefix(response, "FILE_DATA ") {
		return fmt.Errorf("unexpected response from peer: %s", response)
	}

	fileSizeStr := strings.TrimPrefix(response, "FILE_DATA ")
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
		"Downloading file %s (%d bytes) from %s:%d to %s",
		fileName,
		fileSize,
		peerIP,
		peerPort,
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

	if p.config.ShareDir == "" {
		p.logger.Println("No share directory configured, cannot update shared files.")
		p.sharedFiles = []protocol.FileMeta{}
		return nil
	}

	p.logger.Printf("Re-scanning directory: %s", p.config.ShareDir)
	newFiles, err := ScanDirectory(ctx, p.config.ShareDir)
	if err != nil {
		p.logger.Printf("Error re-scanning directory %s: %v", p.config.ShareDir, err)
		return fmt.Errorf("failed to re-scan directory: %w", err)
	}
	p.sharedFiles = newFiles
	p.logger.Printf("Updated shared files: %d files found.", len(p.sharedFiles))

	// TODO: Optionally, re-announce to the index server with updated files.
	return nil
}

// announce sends peer information to the index server.
func (p *CorePeer) announce(indexURL string, peerInfo protocol.PeerInfo) error {
	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info: %w", err)
	}

	announceURL := indexURL + "/announce"
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 10*time.Second)
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

// serveFiles starts the TCP server to listen for file requests.
// It uses the provided listener.
func (p *CorePeer) serveFiles(ctx context.Context, shareDir string, listener net.Listener) {
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
		go p.handleFileRequest(conn, shareDir)
	}
}

// handleFileRequest processes an incoming file request.
func (p *CorePeer) handleFileRequest(conn net.Conn, shareDir string) {
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

	if !strings.HasPrefix(request, "FILE_REQUEST ") {
		conn.Write([]byte("ERROR invalid request format\n"))
		return
	}

	checksum := strings.TrimPrefix(request, "FILE_REQUEST ")
	checksum = strings.TrimSpace(checksum)

	// NOTE: The current FindSharedFileByChecksum re-scans, which is not ideal here.
	// TODO: A better approach: CorePeer maintains an index (map[checksum]LocalFileInfo).
	var localFile *LocalFileInfo
	p.mu.RLock() // Protect access to p.sharedFiles when iterating
	for _, meta := range p.sharedFiles {
		if meta.Checksum == checksum {
			lFile, findErr := FindSharedFileByChecksum(shareDir, checksum)
			if findErr == nil {
				localFile = lFile
			}
			break
		}
	}
	p.mu.RUnlock()

	if localFile == nil {
		p.logger.Printf("File not found for checksum %s requested by %s", checksum, conn.RemoteAddr())
		conn.Write(fmt.Appendf(nil, "ERROR File not found: %s\n", checksum))
		return
	}

	// NOTE: Arbitrary timeout for file transfer.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	_, err = conn.Write(fmt.Appendf(nil, "FILE_DATA %d\n", localFile.Size))
	if err != nil {
		p.logger.Printf("Failed to send file data header to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}

	fileHandle, err := os.Open(localFile.Path)
	if err != nil {
		p.logger.Printf("Failed to open file %s: %v", localFile.Path, err)
		conn.Write([]byte("ERROR Failed to access file\n"))
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
