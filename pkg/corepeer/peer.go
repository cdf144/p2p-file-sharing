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

const (
	maxConcurrentDownloadsPerFile = 5
	chunkDownloadTimeout          = 60 * time.Second
	peerConnectTimeout            = 10 * time.Second
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
	serveCtx        context.Context    // Own context for managing internal serving lifecycle
	serveCancel     context.CancelFunc // Function to cancel serveCtx
	sharedFiles     []protocol.FileMeta
	sharedFilePaths map[string]string // map[checksum]fullFilePath for quick local lookups
	announcedAddr   netip.AddrPort
	mu              sync.RWMutex
	logger          *log.Logger
}

// chunkDownloadResult represents the result of a goroutine worker downloading a single chunk of data.
type chunkDownloadResult struct {
	index int
	data  []byte
	err   error
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
		p.startTCPServer(p.rootCtx)
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
	if err := p.indexClient.Announce(p.announcedAddr, p.sharedFiles); err != nil {
		if p.listener != nil {
			p.listener.Close()
			p.listener = nil
		}
		if p.serveCancel != nil {
			p.serveCancel()
		}
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}

	p.isServing = true
	statusMsg := fmt.Sprintf(
		"Peer started. Sharing from: %s. IP: %s, Serving Port: %d, Announced Port: %d. Files shared: %d",
		p.config.ShareDir, p.announcedAddr.Addr(), p.config.ServePort, p.announcedAddr.Port(), len(p.sharedFiles),
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

// UpdateConfig updates the CorePeer configuration with the provided new configuration.
// It handles changes to share directory, serve port, public port, and index URL (in that order).
// The method is not fully atomic - partial updates may occur if an error happens during processing.
func (p *CorePeer) UpdateConfig(ctx context.Context, newConfig CorePeerConfig) (CorePeerConfig, error) {
	// NOTE: It may be better if this method is atomic, i.e. it should not allow partial updates if an error occur.
	// PERF: If this method is called with changes to multiple fields, redundant deannounce/reannounce are performed.
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
	p.mu.RLock()
	defer p.mu.RUnlock()
	filesCopy := make([]protocol.FileMeta, len(p.sharedFiles))
	copy(filesCopy, p.sharedFiles)
	return filesCopy
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

func (p *CorePeer) DownloadFileFromPeer(ctx context.Context, fileMeta protocol.FileMeta, savePath string) error {
	if fileMeta.NumChunks == 0 && fileMeta.Size > 0 {
		return fmt.Errorf("file metadata indicates non-empty file but zero chunks for %s", fileMeta.Name)
	}
	if fileMeta.NumChunks > 0 && len(fileMeta.ChunkHashes) != fileMeta.NumChunks {
		return fmt.Errorf("inconsistent chunk hash count for %s: expected %d, got %d", fileMeta.Name, fileMeta.NumChunks, len(fileMeta.ChunkHashes))
	}

	peerAddresses, err := p.QueryPeersForFile(ctx, fileMeta.Checksum)
	if err != nil {
		return fmt.Errorf("failed to query peers for file %s: %w", fileMeta.Checksum, err)
	}
	if len(peerAddresses) == 0 {
		return fmt.Errorf("no peers found for file %s (checksum: %s)", fileMeta.Name, fileMeta.Checksum)
	}

	p.logger.Printf(
		"Starting parallel download of %s (%s, %d chunks) from %d available peers to %s",
		fileMeta.Name, fileMeta.Checksum, fileMeta.NumChunks, len(peerAddresses), savePath,
	)

	file, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", savePath, err)
	}
	defer file.Close()

	if fileMeta.Size > 0 {
		if err := file.Truncate(fileMeta.Size); err != nil {
			p.logger.Printf("Warning: Failed to pre-allocate file size for %s: %v", savePath, err)
		}
	} else {
		p.logger.Printf("File %s is empty. Download complete.", fileMeta.Name)
		return nil
	}

	fullFileHasher := sha256.New()

	jobs := make(chan int, fileMeta.NumChunks)
	results := make(chan chunkDownloadResult, fileMeta.NumChunks)

	var wg sync.WaitGroup
	numWorkers := min(fileMeta.NumChunks, len(peerAddresses), maxConcurrentDownloadsPerFile)

	for i := range numWorkers {
		wg.Add(1)
		go p.downloadChunkWorker(ctx, i, fileMeta, peerAddresses, jobs, results, &wg)
	}

	for i := range fileMeta.NumChunks {
		jobs <- i
	}
	close(jobs)

	completedChunks := 0
	downloadErrors := []string{}

	for range fileMeta.NumChunks { // Loop numChunks times because each job must produce a result (data or error)
		select {
		case <-ctx.Done():
			p.logger.Printf("Download context cancelled for %s. Waiting for workers.", fileMeta.Name)
			go func() {
				wg.Wait()
				close(results)
			}()
			for res := range results {
				if res.err != nil {
					p.logger.Printf("Worker error after cancellation: %v", res.err)
				}
			}
			os.Remove(savePath)
			return fmt.Errorf("download cancelled for %s: %w", fileMeta.Name, ctx.Err())
		case res := <-results:
			if res.err != nil {
				downloadErrors = append(downloadErrors, fmt.Sprintf("chunk %d: %v", res.index, res.err))
				// NOTE: For simplicity, first error fails the download. Could implement retries for specific chunks.
				// TODO: To ensure all workers are shut down, we might need a shared cancellation context.
				continue
			}

			currentChunkHasher := sha256.New()
			currentChunkHasher.Write(res.data)
			actualChunkChecksum := hex.EncodeToString(currentChunkHasher.Sum(nil))

			if actualChunkChecksum != fileMeta.ChunkHashes[res.index] {
				errMsg := fmt.Sprintf(
					"checksum mismatch for chunk %d of %s: expected %s, actual %s",
					res.index, fileMeta.Name, fileMeta.ChunkHashes[res.index], actualChunkChecksum,
				)
				downloadErrors = append(downloadErrors, errMsg)
				continue
			}

			offset := int64(res.index) * fileMeta.ChunkSize
			_, err = file.WriteAt(res.data, offset)
			if err != nil {
				downloadErrors = append(downloadErrors, fmt.Sprintf("failed to write chunk %d to file %s: %v", res.index, savePath, err))
				continue
			}

			if _, err := fullFileHasher.Write(res.data); err != nil {
				p.logger.Printf("Error writing chunk %d to overall hasher for %s: %v", res.index, fileMeta.Name, err)
			}
			completedChunks++
			p.logger.Printf("Successfully downloaded, verified, and wrote chunk %d for %s", res.index, fileMeta.Name)
		}
	}

	if len(downloadErrors) > 0 {
		os.Remove(savePath)
		return fmt.Errorf("failed to download file %s due to %d errors: %s", fileMeta.Name, len(downloadErrors), strings.Join(downloadErrors, "; "))
	}

	if completedChunks != fileMeta.NumChunks {
		os.Remove(savePath)
		return fmt.Errorf("download incomplete for %s: expected %d chunks, got %d", fileMeta.Name, fileMeta.NumChunks, completedChunks)
	}

	receivedFullChecksum := hex.EncodeToString(fullFileHasher.Sum(nil))
	if receivedFullChecksum != fileMeta.Checksum {
		os.Remove(savePath)
		return fmt.Errorf(
			"overall checksum mismatch for %s: expected %s, actual %s",
			fileMeta.Name, fileMeta.Checksum, receivedFullChecksum,
		)
	}
	p.logger.Printf("Overall file checksum verified for %s", fileMeta.Name)

	p.logger.Printf(
		"Successfully downloaded and verified all %d chunks for file %s to %s using %d workers",
		fileMeta.NumChunks, fileMeta.Name, savePath, numWorkers,
	)
	return nil
}

func (p *CorePeer) downloadChunkWorker(
	ctx context.Context,
	workerID int,
	fileMeta protocol.FileMeta,
	peerAddresses []netip.AddrPort,
	jobs <-chan int,
	results chan<- chunkDownloadResult,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	p.logger.Printf("Worker %d started for file %s", workerID, fileMeta.Name)

	for chunkIndex := range jobs {
		select {
		case <-ctx.Done():
			p.logger.Printf("Worker %d: context cancelled before processing chunk %d for %s.", workerID, chunkIndex, fileMeta.Name)
			results <- chunkDownloadResult{index: chunkIndex, err: ctx.Err()}
			return
		default:
		}

		p.logger.Printf("Worker %d: attempting to download chunk %d for %s", workerID, chunkIndex, fileMeta.Name)

		var downloadedData []byte
		var downloadErr error

		// NOTE: Current implementation uses simple round-robin peer selection based on chunk index.
		// TODO: Implement retry logic with different peers when chunk download fails from selected peer.
		peerIdx := chunkIndex % len(peerAddresses)
		selectedPeerAddr := peerAddresses[peerIdx]

		p.logger.Printf(
			"Worker %d: chunk %d assigned to peer %s for %s",
			workerID, chunkIndex, selectedPeerAddr.String(), fileMeta.Name,
		)

		conn, err := net.DialTimeout("tcp", selectedPeerAddr.String(), peerConnectTimeout)
		if err != nil {
			downloadErr = fmt.Errorf("worker %d: failed to connect to peer %s for chunk %d of %s: %w", workerID, selectedPeerAddr, chunkIndex, fileMeta.Name, err)
			results <- chunkDownloadResult{index: chunkIndex, data: nil, err: downloadErr}
			continue
		}

		func() { // Anonymous func for scoping defer conn.Close()
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(chunkDownloadTimeout))

			writer := bufio.NewWriter(conn)
			reader := bufio.NewReader(conn)

			if _, err := writer.Write([]byte{byte(protocol.CHUNK_REQUEST)}); err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to write CHUNK_REQUEST type for chunk %d to %s: %w", workerID, chunkIndex, selectedPeerAddr, err)
				return
			}
			if _, err := writer.WriteString(fileMeta.Checksum + "\n"); err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to write file checksum for chunk %d to %s: %w", workerID, chunkIndex, selectedPeerAddr, err)
				return
			}
			if _, err := writer.WriteString(strconv.Itoa(chunkIndex) + "\n"); err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to write chunk index %d to %s: %w", workerID, chunkIndex, selectedPeerAddr, err)
				return
			}
			if err := writer.Flush(); err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to flush CHUNK_REQUEST for chunk %d to %s: %w", workerID, chunkIndex, selectedPeerAddr, err)
				return
			}

			msgTypeByte, err := reader.ReadByte()
			if err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to read response type for chunk %d from %s: %w", workerID, chunkIndex, selectedPeerAddr, err)
				return
			}

			if protocol.MessageType(msgTypeByte) == protocol.ERROR {
				errMsg, _ := reader.ReadString('\n')
				downloadErr = fmt.Errorf("worker %d: peer %s returned error for chunk %d of %s: %s", workerID, selectedPeerAddr, chunkIndex, fileMeta.Name, strings.TrimSpace(errMsg))
				return
			}
			if protocol.MessageType(msgTypeByte) != protocol.CHUNK_DATA {
				downloadErr = fmt.Errorf("worker %d: unexpected response type %d for chunk %d from %s", workerID, msgTypeByte, chunkIndex, selectedPeerAddr)
				return
			}

			respFileChecksum, err := reader.ReadString('\n')
			if err != nil {
				downloadErr = fmt.Errorf(
					"worker %d: failed to read response file checksum for chunk %d from %s: %w",
					workerID, chunkIndex, selectedPeerAddr, err,
				)
				return
			}
			if strings.TrimSpace(respFileChecksum) != fileMeta.Checksum {
				downloadErr = fmt.Errorf(
					"worker %d: checksum mismatch in response for chunk %d from %s: expected %s, got %s",
					workerID, chunkIndex, selectedPeerAddr, fileMeta.Checksum, strings.TrimSpace(respFileChecksum),
				)
				return
			}

			respChunkIndexStr, err := reader.ReadString('\n')
			if err != nil {
				downloadErr = fmt.Errorf(
					"worker %d: failed to read response chunk index for chunk %d from %s: %w",
					workerID, chunkIndex, selectedPeerAddr, err,
				)
				return
			}
			respChunkIndex, err := strconv.Atoi(strings.TrimSpace(respChunkIndexStr))
			if err != nil || respChunkIndex != chunkIndex {
				downloadErr = fmt.Errorf(
					"worker %d: chunk index mismatch in response for chunk %d from %s: expected %d, got %s (%v)",
					workerID, chunkIndex, selectedPeerAddr, chunkIndex, strings.TrimSpace(respChunkIndexStr), err,
				)
				return
			}

			chunkLengthStr, err := reader.ReadString('\n')
			if err != nil {
				downloadErr = fmt.Errorf(
					"worker %d: failed to read chunk length for chunk %d from %s: %w",
					workerID, chunkIndex, selectedPeerAddr, err,
				)
				return
			}
			chunkLength, err := strconv.ParseInt(strings.TrimSpace(chunkLengthStr), 10, 64)
			if err != nil {
				downloadErr = fmt.Errorf(
					"worker %d: invalid chunk length '%s' for chunk %d from %s: %w",
					workerID, strings.TrimSpace(chunkLengthStr), chunkIndex, selectedPeerAddr, err,
				)
				return
			}

			expectedChunkSize := fileMeta.ChunkSize
			if chunkIndex == fileMeta.NumChunks-1 {
				if remainder := fileMeta.Size % fileMeta.ChunkSize; remainder != 0 {
					expectedChunkSize = remainder
				}
			}
			if chunkLength <= 0 || chunkLength > fileMeta.ChunkSize || chunkLength != expectedChunkSize {
				downloadErr = fmt.Errorf(
					"worker %d: invalid chunk data length %d for chunk %d from %s (expected %d, max %d)",
					workerID, chunkLength, chunkIndex, selectedPeerAddr, expectedChunkSize, fileMeta.ChunkSize,
				)
				return
			}

			data := make([]byte, chunkLength)
			n, err := io.ReadFull(reader, data)
			if err != nil {
				downloadErr = fmt.Errorf("worker %d: failed to read chunk %d data (expected %d bytes) from %s: %w", workerID, chunkIndex, chunkLength, selectedPeerAddr, err)
				return
			}
			if int64(n) != chunkLength {
				downloadErr = fmt.Errorf("worker %d: incomplete chunk %d data from %s: read %d, expected %d", workerID, chunkIndex, selectedPeerAddr, n, chunkLength)
				return
			}
			downloadedData = data
		}()

		results <- chunkDownloadResult{index: chunkIndex, data: downloadedData, err: downloadErr}
	}
	p.logger.Printf("Worker %d finished for file %s", workerID, fileMeta.Name)
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
		go p.handleConnection(conn)
	}
}

func (p *CorePeer) handleConnection(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	msgTypeByte, err := reader.ReadByte()
	if err != nil {
		if err != io.EOF {
			p.logger.Printf("Warning: Failed to read message type from %s: %v", conn.RemoteAddr(), err)
		}
		return
	}

	msgType := protocol.MessageType(msgTypeByte)
	p.logger.Printf("Received message type %s (%d) from %s", msgType.String(), msgTypeByte, conn.RemoteAddr())

	switch msgType {
	case protocol.CHUNK_REQUEST:
		p.handleChunkRequestCommand(conn, reader, writer)
	case protocol.FILE_REQUEST:
		p.handleFileRequestCommand(conn, reader, writer)
	default:
		p.logger.Printf("Warning: Received unknown message type %d from %s", msgTypeByte, conn.RemoteAddr())
		p.sendError(writer, conn.RemoteAddr().String(), "Unknown message type")
	}
}

// handleChunkRequestCommand processes a CHUNK_REQUEST message.
func (p *CorePeer) handleChunkRequestCommand(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer) {
	fileChecksum, err := reader.ReadString('\n')
	if err != nil {
		p.logger.Printf("Warning: Failed to read file checksum for CHUNK_REQUEST from %s: %v", conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to read file checksum")
		return
	}
	fileChecksum = strings.TrimSpace(fileChecksum)

	chunkIndexStr, err := reader.ReadString('\n')
	if err != nil {
		p.logger.Printf("Warning: Failed to read chunk index for CHUNK_REQUEST from %s: %v", conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to read chunk index")
		return
	}
	chunkIndexStr = strings.TrimSpace(chunkIndexStr)
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		p.logger.Printf("Warning: Invalid chunk index '%s' for CHUNK_REQUEST from %s: %v", chunkIndexStr, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Invalid chunk index format")
		return
	}

	fileMeta, err := p.getSharedFileMetaByChecksum(fileChecksum)
	if err != nil {
		p.logger.Printf("Warning: FileMeta not found for checksum %s (CHUNK_REQUEST by %s): %v", fileChecksum, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), fmt.Sprintf("File not found for checksum %s", fileChecksum))
		return
	}

	if chunkIndex < 0 || (fileMeta.NumChunks > 0 && chunkIndex >= fileMeta.NumChunks) || (fileMeta.NumChunks == 0 && chunkIndex != 0) {
		errMsg := fmt.Sprintf("Invalid chunk index %d for file %s (NumChunks: %d)", chunkIndex, fileMeta.Name, fileMeta.NumChunks)
		p.logger.Printf("Warning: %s, requested by %s", errMsg, conn.RemoteAddr())
		p.sendError(writer, conn.RemoteAddr().String(), errMsg)
		return
	}
	if fileMeta.NumChunks == 0 { // Also implies fileMeta.Size == 0
		errMsg := fmt.Sprintf("File %s is empty, no chunks to request (NumChunks: 0)", fileMeta.Name)
		p.logger.Printf("Warning: %s, requested by %s", errMsg, conn.RemoteAddr())
		p.sendError(writer, conn.RemoteAddr().String(), errMsg)
		return
	}

	p.mu.RLock()
	filePath := p.sharedFilePaths[fileChecksum]
	p.mu.RUnlock()
	if filePath == "" {
		p.logger.Printf("Critical: File path for checksum %s is empty despite FileMeta existing. CHUNK_REQUEST by %s", fileChecksum, conn.RemoteAddr())
		p.sendError(writer, conn.RemoteAddr().String(), "Internal server error: file path missing")
		return
	}

	fileHandle, err := os.Open(filePath)
	if err != nil {
		p.logger.Printf("Warning: Failed to open file %s for CHUNK_REQUEST by %s: %v", filePath, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to open file for chunk")
		return
	}
	defer fileHandle.Close()

	chunkOffset := int64(chunkIndex) * fileMeta.ChunkSize
	bytesToRead := fileMeta.ChunkSize
	if chunkIndex == fileMeta.NumChunks-1 {
		if remainder := fileMeta.Size % fileMeta.ChunkSize; remainder != 0 {
			bytesToRead = remainder
		}
	}

	chunkData := make([]byte, bytesToRead)
	if _, err = fileHandle.Seek(chunkOffset, io.SeekStart); err != nil {
		p.logger.Printf("Warning: Failed to seek to chunk offset %d for file %s (CHUNK_REQUEST by %s): %v", chunkOffset, filePath, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to seek to chunk")
		return
	}

	n, err := io.ReadFull(fileHandle, chunkData)
	if err != nil {
		p.logger.Printf("Warning: Failed to read chunk %d from file %s (expected %d bytes, got %d) for %s: %v", chunkIndex, filePath, bytesToRead, n, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to read chunk data")
		return
	}
	actualChunkData := chunkData[:n]

	conn.SetWriteDeadline(time.Now().Add(1 * time.Minute))

	if _, err := writer.Write([]byte{byte(protocol.CHUNK_DATA)}); err != nil {
		p.logger.Printf("Warning: Failed to write CHUNK_DATA message type to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(fileChecksum + "\n"); err != nil {
		p.logger.Printf("Warning: Failed to write file checksum to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(strconv.Itoa(chunkIndex) + "\n"); err != nil {
		p.logger.Printf("Warning: Failed to write chunk index to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(fmt.Sprintf("%d\n", int64(n))); err != nil {
		p.logger.Printf("Warning: Failed to write chunk length to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.Write(actualChunkData); err != nil {
		p.logger.Printf(
			"Warning: Failed to write chunk %d payload for file %s to buffer for %s: %v",
			chunkIndex, fileMeta.Name, conn.RemoteAddr(), err,
		)
		return
	}

	if err := writer.Flush(); err != nil {
		p.logger.Printf(
			"Warning: Failed to flush CHUNK_DATA message for file %s chunk %d to %s: %v",
			fileMeta.Name, chunkIndex, conn.RemoteAddr(), err,
		)
		return
	}

	p.logger.Printf(
		"Successfully sent chunk %d (%d bytes) for file %s (%s) to %s",
		chunkIndex, n, fileMeta.Name, fileChecksum, conn.RemoteAddr(),
	)
}

// handleFileRequestCommand processes a FILE_REQUEST message.
func (p *CorePeer) handleFileRequestCommand(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer) {
	checksum, err := reader.ReadString('\n')
	if err != nil {
		p.logger.Printf("Warning: Failed to read checksum for FILE_REQUEST from %s: %v", conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), "Failed to read checksum")
		return
	}
	checksum = strings.TrimSpace(checksum)

	localFile, err := p.getLocalFileInfoByChecksum(checksum)
	if err != nil {
		p.logger.Printf("Warning: File not found for checksum %s (FILE_REQUEST by %s): %v", checksum, conn.RemoteAddr(), err)
		p.sendError(writer, conn.RemoteAddr().String(), fmt.Sprintf("File not found for checksum %s", checksum))
		return
	}

	if _, err := writer.Write([]byte{byte(protocol.FILE_DATA)}); err != nil {
		p.logger.Printf("Warning: Failed to write FILE_DATA message type to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}
	if _, err := writer.WriteString(fmt.Sprintf("%d\n", localFile.Size)); err != nil {
		p.logger.Printf("Warning: Failed to write file size to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}
	if err := writer.Flush(); err != nil {
		p.logger.Printf("Warning: Failed to flush FILE_DATA header to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}

	conn.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	fileHandle, err := os.Open(localFile.Path)
	if err != nil {
		p.logger.Printf("Warning: Failed to open file %s for FILE_REQUEST by %s: %v", localFile.Path, conn.RemoteAddr(), err)
		return
	}
	defer fileHandle.Close()

	sent, err := io.Copy(conn, fileHandle)
	if err != nil {
		p.logger.Printf("Warning: Failed to send file %s to %s: %v", localFile.Name, conn.RemoteAddr(), err)
		return
	}
	if sent != localFile.Size {
		p.logger.Printf("Warning: Incomplete file transfer for %s to %s: sent %d, expected %d", localFile.Name, conn.RemoteAddr(), sent, localFile.Size)
		return
	}

	p.logger.Printf("Successfully sent file %s (%d bytes) to %s via FILE_REQUEST", localFile.Name, sent, conn.RemoteAddr())
}

func (p *CorePeer) sendError(writer *bufio.Writer, remoteAddr string, errorMessage string) {
	if _, err := writer.Write([]byte{byte(protocol.ERROR)}); err != nil {
		p.logger.Printf("Warning: Failed to write ERROR message type to %s: %v", remoteAddr, err)
		return
	}
	if _, err := writer.WriteString(errorMessage + "\n"); err != nil {
		p.logger.Printf("Warning: Failed to write error message string to %s: %v", remoteAddr, err)
		return
	}
	if err := writer.Flush(); err != nil {
		p.logger.Printf("Warning: Failed to flush error message to %s: %v", remoteAddr, err)
	}
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

	p.indexClient = NewIndexClient(newIndexURL, p.logger)
	p.logger.Printf("IndexClient updated with new URL: %s", newIndexURL)

	if p.isServing && newIndexURL != "" {
		if err := p.indexClient.Announce(p.announcedAddr, p.sharedFiles); err != nil {
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
	p.resetSharedFiles()
	p.config.ShareDir = newAbsShareDir

	wasPreviouslySharingFromDir := oldShareDir != ""
	willNowShareFromDir := newAbsShareDir != ""

	if willNowShareFromDir {
		if err := p.scanAndUpdateSharedFiles(ctx); err != nil {
			p.logger.Printf("Warning: Failed to scan share directory %s: %v. Shared files will be empty.", newAbsShareDir, err)
		}
	} else {
		p.logger.Println("Share directory is now empty. No files are shared from a directory.")
	}

	if p.isServing {
		if err := p.updateTCPServerState(willNowShareFromDir, wasPreviouslySharingFromDir); err != nil {
			p.config.ShareDir = oldShareDir
			return err
		}
		if err := p.indexClient.Reannounce(p.announcedAddr, p.sharedFiles); err != nil {
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

	p.stopTCPServer()
	if p.config.ShareDir != "" {
		if err := p.startTCPServer(p.rootCtx); err != nil {
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
	if err := p.indexClient.Announce(p.announcedAddr, p.sharedFiles); err != nil {
		return fmt.Errorf("failed to re-announce to index server with new public port %d: %w", newPublicPort, err)
	}
	p.logger.Printf("Re-announced to index server with new public port: %d", newPublicPort)

	return nil
}

// stopTCPServer gracefully shuts down the TCP server by canceling the serve context
// and closing the network listener. After shutdown, the listener is set to nil.
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

// startTCPServer initializes and starts a TCP server on the configured port.
// It creates a TCP listener, stores it in the peer instance, and begins accepting
// incoming connections in a separate goroutine.
// The server will continue running until the context is cancelled or an error occurs.
// If a listener already exists, the method returns early without error.
func (p *CorePeer) startTCPServer(ctx context.Context) error {
	if p.listener != nil {
		p.logger.Println("TCP server already running or listener already exists.")
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return fmt.Errorf("cannot start TCP server, context is not active or nil")
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", p.config.ServePort))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", p.config.ServePort, err)
	}
	p.logger.Printf("TCP server started on port: %d", p.config.ServePort)

	p.listener = l
	p.serveCtx, p.serveCancel = context.WithCancel(ctx)
	go p.acceptConnections(p.serveCtx)

	return nil
}

// updateTCPServerState manages the TCP server lifecycle based on the desired and current state.
// It starts the TCP server if it should be running but isn't currently running or if the listener is nil.
// It stops the TCP server if it shouldn't be running but is currently running.
// Returns an error if starting the TCP server fails.
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

// getLocalFileInfoByChecksum retrieves local file information for a file identified by its checksum.
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

func (p *CorePeer) getSharedFileMetaByChecksum(checksum string) (protocol.FileMeta, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.sharedFilePaths[checksum]; !ok {
		return protocol.FileMeta{}, fmt.Errorf("file with checksum %s not found in shared file paths", checksum)
	}

	for _, fm := range p.sharedFiles {
		if fm.Checksum == checksum {
			return fm, nil
		}
	}
	return protocol.FileMeta{}, fmt.Errorf("file metadata for checksum %s not found in shared files list (inconsistent state)", checksum)
}
