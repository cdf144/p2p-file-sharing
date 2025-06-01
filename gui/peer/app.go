package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx             context.Context
	isServing       bool
	sharedFiles     []protocol.FileMeta
	currentShareDir string
	announcedIP     net.IP
	announcedPort   uint16
	servePort       uint16
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
}

// shutdown is called when the app terminates.
func (a *App) shutdown(ctx context.Context) {
	wruntime.LogInfo(ctx, "[peer] Application shutting down...")
	a.isServing = false
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
	a.currentShareDir = dir

	files, err := a.scanDirectoryInternal(dir)
	if err != nil {
		wruntime.LogWarningf(a.ctx, "[peer] Failed to scan directory %s: %v", dir, err)
	} else {
		a.sharedFiles = files
		wruntime.EventsEmit(a.ctx, "filesScanned", files)
	}

	return dir, nil
}

func (a *App) StartPeerLogic(indexURL, shareDir string, servePort, publicPort int) (string, error) {
	if a.isServing {
		return "Peer is already running", nil
	}

	files := []protocol.FileMeta{}
	if shareDir == "" {
		wruntime.LogInfof(a.ctx, "[peer] No share directory specified. No files will be shared.")
	} else {
		absShareDir, err := filepath.Abs(shareDir)
		if err != nil {
			wruntime.LogErrorf(a.ctx, "[peer] Failed to get absolute path for share directory %s: %v", shareDir, err)
			return "", fmt.Errorf("failed to get absolute path for share directory %s: %w", shareDir, err)
		}
		shareDir = absShareDir

		if a.currentShareDir == absShareDir {
			files = a.sharedFiles
			wruntime.LogInfof(
				a.ctx,
				"[peer] Files already scanned in share directory %s. Reusing %d files.",
				shareDir,
				len(files),
			)
		} else {
			files, err = a.scanDirectoryInternal(shareDir)
			if err != nil {
				wruntime.LogWarningf(
					a.ctx,
					"[peer] Failed to scan share directory %s: %v. No files will be shared.",
					shareDir,
					err,
				)
			}
			a.sharedFiles = files
			wruntime.EventsEmit(a.ctx, "filesScanned", files)
		}
	}

	ipAddr, err := a.getMachineIPInternal()
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to get machine IP address: %v", err)
		return "", fmt.Errorf("failed to get machine IP address: %w", err)
	}
	a.announcedIP = ipAddr

	actualServePort := servePort
	if actualServePort == 0 {
		listener, errListen := net.Listen("tcp", ":0")
		if errListen != nil {
			wruntime.LogErrorf(a.ctx, "[peer] Failed to listen on a random port: %v", errListen)
			return "", fmt.Errorf("failed to listen on a random port: %w", errListen)
		}
		actualServePort = listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		wruntime.LogInfof(
			a.ctx,
			"[peer] No serve port specified. Using randomly assigned port: %d",
			actualServePort,
		)
	}
	a.servePort = uint16(actualServePort)

	announcedPort := uint16(0)
	if publicPort != 0 {
		announcedPort = uint16(publicPort)
		wruntime.LogInfof(a.ctx, "[peer] Using specified public port for announcement: %d", announcedPort)
	} else {
		announcedPort = uint16(actualServePort)
		wruntime.LogInfof(a.ctx, "[peer] No public port specified. Announcing with internal port: %d", actualServePort)
	}
	a.announcedPort = announcedPort

	peer := protocol.PeerInfo{
		IP:    ipAddr,
		Port:  announcedPort,
		Files: files,
	}

	if err := a.announceToIndexServerInternal(indexURL, peer); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to announce to index server: %v", err)
		return "", fmt.Errorf("failed to announce to index server: %w", err)
	}
	wruntime.LogInfof(a.ctx,
		"[peer] Announced to index server at %s. PeerInfo: {IP: %s, Port: %d, Files: %d}",
		indexURL,
		peer.IP,
		peer.Port,
		len(files),
	)

	if shareDir != "" {
		go a.serveFilesInternal(shareDir, actualServePort)
	}

	a.isServing = true
	successMsg := fmt.Sprintf(
		"Peer started. Sharing from: %s. IP: %s, Serving Port: %d, Announced Port: %d. Files shared: %d",
		shareDir,
		ipAddr,
		actualServePort,
		announcedPort,
		len(files),
	)
	wruntime.LogInfo(a.ctx, successMsg)
	return successMsg, nil
}

func (a *App) QueryIndexServer(indexURL string) ([]protocol.PeerInfo, error) {
	queryURL := indexURL + "/peers"
	req, err := http.NewRequestWithContext(a.ctx, "GET", queryURL, nil)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to create query request to %s: %v", queryURL, err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to send query request to %s: %v", queryURL, err)
		return nil, fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("server returned status %s: %s", resp.Status, string(respBody))
		wruntime.LogErrorf(a.ctx, "[peer] Query failed: %s", errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	var peers []protocol.PeerInfo
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to decode response from index server: %v", err)
		return nil, fmt.Errorf("failed to decode response: %w", err)
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
		return "", fmt.Errorf("failed to choose save location: %w", err)
	}

	if saveDir == "" {
		return "", fmt.Errorf("save cancelled by user")
	}

	err = a.DownloadFile(peerIP, peerPort, fileChecksum, fileName, saveDir)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully downloaded %s to %s", fileName, saveDir), nil
}

func (a *App) DownloadFile(
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
		wruntime.LogErrorf(a.ctx, "[peer] Failed to connect to peer %s:%d: %v", peerIP, peerPort, err)
		return fmt.Errorf("failed to connect to peer: %w", err)
	}
	defer conn.Close()

	_, err = conn.Write(fmt.Appendf(nil, "FILE_REQUEST %s\n", fileChecksum))
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to send request to peer: %v", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to read response from peer: %v", err)
	}

	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "ERROR ") {
		errorMsg := strings.TrimPrefix(response, "ERROR ")
		wruntime.LogErrorf(a.ctx, "[peer] Peer returned error: %s", errorMsg)
		return fmt.Errorf("peer error: %s", errorMsg)
	}
	if !strings.HasPrefix(response, "FILE_DATA ") {
		wruntime.LogErrorf(a.ctx, "[peer] Unexpected response from peer: %s", response)
		return fmt.Errorf("unexpected response: %s", response)
	}

	fileSizeStr := strings.TrimPrefix(response, "FILE_DATA ")
	fileSize, err := strconv.ParseInt(fileSizeStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid file size in response: %s", fileSizeStr)
	}

	file, err := os.Create(savePath)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to create file %s: %v", savePath, err)
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	wruntime.LogInfof(
		a.ctx,
		"[peer] Downloading file %s (%d bytes) from %s:%d",
		fileName,
		fileSize,
		peerIP,
		peerPort,
	)

	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(file, hash), reader, fileSize)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to download file: %v", err)
		return fmt.Errorf("failed to download file: %w", err)
	}
	if written != fileSize {
		return fmt.Errorf("incomplete download: expected %d bytes, got %d", fileSize, written)
	}

	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if actualChecksum != fileChecksum {
		wruntime.LogErrorf(
			a.ctx,
			"[peer] Checksum mismatch:\nExpected: %s\nActual: %s",
			fileChecksum,
			actualChecksum,
		)
		os.Remove(savePath)
		return fmt.Errorf("checksum verification failed")
	}

	wruntime.LogInfof(a.ctx, "[peer] Successfully downloaded and verified file %s", fileName)
	return nil
}

func (a *App) scanDirectoryInternal(dir string) ([]protocol.FileMeta, error) {
	var filePaths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			filePaths = append(filePaths, path)
		}
		return nil
	})
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Error walking directory %s: %v", dir, err)
		return nil, fmt.Errorf("failed to walk directory %s: %w", dir, err)
	}
	if len(filePaths) == 0 {
		wruntime.LogInfof(a.ctx, "[peer] No files found to share in directory %s", dir)
		return []protocol.FileMeta{}, nil
	}

	numWorkers := min(runtime.NumCPU(), len(filePaths))
	if numWorkers == 0 {
		numWorkers = 1
	}

	jobs := make(chan string, len(filePaths))
	jobResults := make(chan protocol.FileMeta, len(filePaths))
	var wg sync.WaitGroup

	for i := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for path := range jobs {
				info, err := os.Stat(path)
				if err != nil {
					wruntime.LogWarningf(a.ctx,
						"[peer] Warning: scanDirectory worker %d failed to stat file %s: %v",
						workerID, path, err)
					continue
				}

				data, err := os.ReadFile(path)
				if err != nil {
					wruntime.LogWarningf(a.ctx,
						"[peer] Warning: scanDirectory worker %d failed to read file %s: %v",
						workerID, path, err)
					continue
				}

				jobResults <- protocol.FileMeta{
					Checksum: protocol.GenerateChecksum(data),
					Name:     info.Name(),
					Size:     info.Size(),
				}
			}
		}(i)
	}

	for _, path := range filePaths {
		jobs <- path
	}
	close(jobs)

	wg.Wait()
	close(jobResults)

	scannedFiles := make([]protocol.FileMeta, 0, len(jobResults))
	for file := range jobResults {
		scannedFiles = append(scannedFiles, file)
	}
	wruntime.LogInfof(a.ctx, "[peer] Scanned directory %s and found %d files", dir, len(scannedFiles))
	return scannedFiles, nil
}

func (a *App) getMachineIPInternal() (net.IP, error) {
	ipAddr, err := a.getPublicIPInternal()
	if err != nil {
		wruntime.LogWarningf(a.ctx, "Failed to get public IP: %v. Falling back to local IP.", err)
		ipAddr, err = a.getOutboundIPInternal()
		if err != nil {
			return nil, fmt.Errorf("failed to get public or outbound IP: %v", err)
		}
	}
	return ipAddr, nil
}

func (a *App) getPublicIPInternal() (net.IP, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return nil, fmt.Errorf("HTTP request to api.ipify.org failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api.ipify.org returned status %s", resp.Status)
	}

	ipAddrBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from api.ipify.org: %v", err)
	}

	ipAddr := net.ParseIP(string(ipAddrBytes))
	if ipAddr == nil {
		return nil, fmt.Errorf("failed to parse IP address from api.ipify.org response: %s", string(ipAddrBytes))
	}
	return ipAddr, nil
}

func (a *App) getOutboundIPInternal() (net.IP, error) {
	// This does NOT get the machine's public IP, just the IP on its current network interface.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP, nil
}

func (a *App) announceToIndexServerInternal(indexURL string, peer protocol.PeerInfo) error {
	jsonData, err := json.Marshal(peer)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to marshal peer info to JSON: %v", err)
		return fmt.Errorf("failed to marshal peer info: %w", err)
	}

	announceURL := indexURL + "/announce"
	req, err := http.NewRequestWithContext(a.ctx, "POST", announceURL, bytes.NewBuffer(jsonData)) // Use context
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to create announce request to %s: %v", announceURL, err)
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to send announce request to %s: %v", announceURL, err)
		return fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("server returned status %s: %s", resp.Status, string(respBody))
		wruntime.LogErrorf(a.ctx, "[peer] Announce failed: %s", errMsg)
		return fmt.Errorf("%s", errMsg)
	}
	wruntime.LogInfof(a.ctx, "[peer] Successfully announced to index server: %s", announceURL)
	return nil
}

func (a *App) serveFilesInternal(shareDir string, port int) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to start TCP server on port %d: %v", port, err)
		return
	}

	wruntime.LogInfof(a.ctx, "[peer] Starting TCP file server on port %d", port)

	go func() {
		<-a.ctx.Done()
		wruntime.LogInfof(a.ctx, "[peer] Shutting down TCP file server on port %d", port)
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if a.ctx.Err() != nil {
				wruntime.LogInfo(a.ctx, "[peer] TCP server stopped gracefully")
				return
			}
			wruntime.LogErrorf(a.ctx, "[peer] Failed to accept connection: %v", err)
			continue
		}
		go a.handleTCPConnection(conn, shareDir)
	}
}

func (a *App) handleTCPConnection(conn net.Conn, shareDir string) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetWriteDeadline(time.Now().Add(60 * time.Second))

	reader := bufio.NewReader(conn)
	request, err := reader.ReadString('\n')
	if err != nil {
		wruntime.LogWarningf(a.ctx, "[peer] Failed to read request: %v", err)
		return
	}

	request = strings.TrimSpace(request)
	wruntime.LogInfof(a.ctx, "[peer] Received request: %s", request)

	if !strings.HasPrefix(request, "FILE_REQUEST ") {
		conn.Write([]byte("ERROR invalid request format\n"))
		return
	}

	checksum := strings.TrimPrefix(request, "FILE_REQUEST ")
	checksum = strings.TrimSpace(checksum)
	file, err := a.findFileByChecksum(shareDir, checksum)
	if err != nil {
		wruntime.LogWarningf(a.ctx, "[peer] File not found for checksum %s: %v", checksum, err)
		conn.Write(fmt.Appendf(nil, "ERROR File not found: %s\n", checksum))
		return
	}

	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to stat file %s: %v", file.Path, err)
		conn.Write([]byte("ERROR Failed to access file\n"))
		return
	}

	_, err = conn.Write(fmt.Appendf(nil, "FILE_DATA %d\n", fileInfo.Size()))
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to send file data response header: %v", err)
		return
	}

	fileHandle, err := os.Open(file.Path)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to open file %s: %v", file.Path, err)
		return
	}
	defer fileHandle.Close()

	sent, err := io.Copy(conn, fileHandle)
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Failed to send file: %v", err)
		return
	}

	wruntime.LogInfof(
		a.ctx,
		"[peer] Successfully sent file %s (%d bytes) for checksum %s to %s",
		file.Name,
		sent,
		checksum,
		conn.RemoteAddr().String(),
	)
}

func (a *App) findFileByChecksum(shareDir, checksum string) (*FileInfo, error) {
	var result *FileInfo

	err := filepath.WalkDir(shareDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		fileChecksum := protocol.GenerateChecksum(data)
		if fileChecksum == checksum {
			result = &FileInfo{
				Checksum: fileChecksum,
				Path:     path,
				Name:     d.Name(),
			}
			return fs.SkipAll
		}

		return nil
	})
	if err != nil {
		wruntime.LogErrorf(a.ctx, "[peer] Error walking share directory %s: %v", shareDir, err)
		return nil, fmt.Errorf("failed to walk share directory %s: %w", shareDir, err)
	}

	if result == nil {
		return nil, fmt.Errorf("file with checksum %s not found in share directory %s", checksum, shareDir)
	}
	return result, nil
}
