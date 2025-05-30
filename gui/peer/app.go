package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
	mux := http.NewServeMux()
	fileServer := http.FileServer(http.Dir(shareDir))
	mux.Handle("/files/", http.StripPrefix("/files/", fileServer))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	wruntime.LogInfof(a.ctx, "[peer] Starting file server. Serving files from %s on port %d at /files/", shareDir, port)

	go func() {
		<-a.ctx.Done() // Wait for Wails app to signal shutdown
		wruntime.LogInfo(a.ctx, "[peer] Shutting down file server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			wruntime.LogErrorf(a.ctx, "[peer] File server shutdown error: %v", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		wruntime.LogErrorf(a.ctx, "[peer] File server ListenAndServe error on port %d: %v", port, err)
	}
	wruntime.LogInfo(a.ctx, "[peer] File server has stopped.")
}
