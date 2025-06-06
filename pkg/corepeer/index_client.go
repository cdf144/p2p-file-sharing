package corepeer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

// IndexClient handles communication with the index server.
type IndexClient struct {
	indexURL   string
	httpClient *http.Client
	logger     *log.Logger
}

// NewIndexClient creates a new IndexClient.
func NewIndexClient(indexURL string, logger *log.Logger) *IndexClient {
	return &IndexClient{
		indexURL:   indexURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}
}

// Announce sends peer information to the index server.
func (ic *IndexClient) Announce(peerAddr netip.AddrPort, sharedFiles []protocol.FileMeta) error {
	if ic.indexURL == "" {
		ic.logger.Println("IndexURL is not configured. Skipping announce.")
		return nil
	}

	peerInfo := protocol.PeerInfo{
		Address: peerAddr,
		Files:   sharedFiles,
	}
	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info: %w", err)
	}

	announceURL := ic.indexURL + "/peers/announce"
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "POST", announceURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ic.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned status %s: %s", resp.Status, string(respBody))
	}
	ic.logger.Printf("Successfully announced to index server: %s", announceURL)
	return nil
}

// Deannounce removes the peer's information from the index server.
func (ic *IndexClient) Deannounce(peerAddr netip.AddrPort) error {
	if ic.indexURL == "" {
		ic.logger.Println("IndexURL is not configured. Skipping de-announce.")
		return nil
	}

	peerInfo := protocol.PeerInfo{Address: peerAddr}
	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info for de-announce: %w", err)
	}

	deannounceURL := ic.indexURL + "/peers/deannounce"
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "POST", deannounceURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create de-announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ic.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("de-announce request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("index server returned status %s for de-announce: %s", resp.Status, string(respBody))
	}
	ic.logger.Printf("Successfully de-announced from index server: %s", deannounceURL)
	return nil
}

// Reannounce sends the peer's updated information to the index server.
func (ic *IndexClient) Reannounce(peerAddr netip.AddrPort, sharedFiles []protocol.FileMeta) error {
	if ic.indexURL == "" {
		ic.logger.Println("IndexURL is not configured. Skipping re-announce.")
		return nil
	}
	if !peerAddr.IsValid() {
		return fmt.Errorf("cannot re-announce without a valid announced address")
	}

	peerInfo := protocol.PeerInfo{
		Address: peerAddr,
		Files:   sharedFiles,
	}
	reannounceURL := ic.indexURL + "/peers/reannounce"

	jsonData, err := json.Marshal(peerInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal peer info for re-announce: %w", err)
	}

	reqCtx, cancelReq := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, "POST", reannounceURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create re-announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ic.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("re-announce request to index server failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("index server returned status %s for re-announce: %s", resp.Status, string(respBody))
	}

	ic.logger.Printf("Successfully re-announced to index server: %s", reannounceURL)
	return nil
}

// FetchAllFiles retrieves all available file metadata from the index server.
func (ic *IndexClient) FetchAllFiles(ctx context.Context) ([]protocol.FileMeta, error) {
	if ic.indexURL == "" {
		return nil, fmt.Errorf("index URL is not configured")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	reqCtx, cancelReq := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReq()

	queryURL := ic.indexURL + "/files"
	req, err := http.NewRequestWithContext(reqCtx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create query request: %w", err)
	}

	resp, err := ic.httpClient.Do(req)
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
	ic.logger.Printf("Fetched %d files from index server %s", len(files), ic.indexURL)
	return files, nil
}

// QueryFilePeers retrieves a list of peers that are serving a specific file by its checksum.
func (ic *IndexClient) QueryFilePeers(ctx context.Context, checksum string) ([]netip.AddrPort, error) {
	if ic.indexURL == "" {
		return nil, fmt.Errorf("index URL is not configured")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	reqCtx, cancelReq := context.WithTimeout(ctx, 10*time.Second)
	defer cancelReq()

	queryURL := fmt.Sprintf("%s/files/%s/peers", ic.indexURL, checksum)
	req, err := http.NewRequestWithContext(reqCtx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create query request: %w", err)
	}

	resp, err := ic.httpClient.Do(req)
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
	ic.logger.Printf("Found %d peers for file with checksum %s", len(peerAddrs), checksum)
	return peerAddrs, nil
}
