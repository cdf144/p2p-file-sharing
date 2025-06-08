package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

// TODO: replace IndexServer with a persistent storage solution, i.e., a database.
// FIXME: Files of different names can have the same checksum, so we need to handle that.

// IndexServer is a temporary in-memory index server that stores file metadata.
type IndexServer struct {
	files    map[string][]protocol.PeerInfo // Maps file Checksum to a list of PeerInfo
	fileInfo map[string]protocol.FileMeta   // Maps file Checksum to file metadata
	mu       sync.RWMutex
	logger   *log.Logger
}

func main() {
	httpServer := http.Server{
		Addr:           ":9090",
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   5 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}
	indexServer := &IndexServer{
		files:    make(map[string][]protocol.PeerInfo),
		fileInfo: make(map[string]protocol.FileMeta),
		logger:   log.New(log.Writer(), "[index-server] ", log.LstdFlags|log.Lmsgprefix),
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /peers", indexServer.handleGetAllPeers)
	mux.HandleFunc("GET /peers/{ip}/{port}/files", indexServer.handleGetOnePeerFiles)
	mux.HandleFunc("POST /peers/announce", indexServer.handlePostPeerAnnounce)
	mux.HandleFunc("POST /peers/deannounce", indexServer.handlePostPeerDeannounce)
	mux.HandleFunc("POST /peers/reannounce", indexServer.handlePostPeerReannounce)

	mux.HandleFunc("GET /files", indexServer.handleGetAllFiles)
	mux.HandleFunc("GET /files/{checksum}", indexServer.handleGetOneFile)
	mux.HandleFunc("GET /files/{checksum}/peers", indexServer.handleGetOneFilePeers)

	mux.HandleFunc("GET /search/files", indexServer.handleSearchFiles)

	httpServer.Handler = mux

	indexServer.logger.Println("Starting server on", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		indexServer.logger.Fatalf("Could not listen on %s: %v\n", httpServer.Addr, err)
	}
}

func (s *IndexServer) handleGetAllPeers(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peerSet := make(map[protocol.PeerInfoSummary]struct{})
	for _, peers := range s.files {
		for _, peer := range peers {
			peerSet[protocol.PeerInfoSummary{
				Address:   peer.Address,
				FileCount: len(peer.Files),
			}] = struct{}{}
		}
	}

	var allPeers []protocol.PeerInfoSummary
	for peer := range peerSet {
		allPeers = append(allPeers, peer)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allPeers); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
	}
}

func (s *IndexServer) handleGetOnePeerFiles(w http.ResponseWriter, r *http.Request) {
	ip := r.PathValue("ip")
	port := r.PathValue("port")
	if ip == "" || port == "" {
		http.Error(w, "missing ip or port in path", http.StatusBadRequest)
		return
	}

	peerAddress, err := netip.ParseAddrPort(ip + ":" + port)
	if err != nil {
		http.Error(w, "invalid IP or port format", http.StatusBadRequest)
		s.logger.Printf("Invalid IP or port format: %v", err)
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var files []protocol.FileMeta
	for _, peers := range s.files {
		for _, peer := range peers {
			if peer.Address == peerAddress {
				files = peer.Files
				break
			}
		}
		if len(files) > 0 {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
	}
}

func (s *IndexServer) handlePostPeerAnnounce(w http.ResponseWriter, r *http.Request) {
	var peerToAnnounce protocol.PeerInfo
	if err := json.NewDecoder(r.Body).Decode(&peerToAnnounce); err != nil {
		http.Error(w, "invalid JSON request body for announce", http.StatusBadRequest)
		s.logger.Printf("Error decoding announce request: %v", err)
		return
	}

	if !peerToAnnounce.Address.IsValid() {
		http.Error(w, "invalid announce address", http.StatusBadRequest)
		s.logger.Printf("Invalid announce address: %s", peerToAnnounce.Address.String())
		return
	}

	s.mu.Lock()
	s.upsertPeerAssociations(peerToAnnounce)
	s.mu.Unlock()

	s.logger.Printf(
		"Peer %s announced/updated with %d files.",
		peerToAnnounce.Address.String(), len(peerToAnnounce.Files),
	)
	w.WriteHeader(http.StatusNoContent)
}

func (s *IndexServer) handlePostPeerDeannounce(w http.ResponseWriter, r *http.Request) {
	var peerToDeannounce struct {
		// For manual API testing, supply "address" as a string in the format "<ip>:<port>"
		Address netip.AddrPort `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&peerToDeannounce); err != nil {
		http.Error(w, "invalid JSON request body for deannounce", http.StatusBadRequest)
		return
	}

	if !peerToDeannounce.Address.IsValid() {
		http.Error(w, "invalid deannounce address", http.StatusBadRequest)
		s.logger.Printf("Invalid deannounce address: %s", peerToDeannounce.Address.String())
		return
	}

	s.mu.Lock()
	removedFileCount := len(s.removePeerAssociations(peerToDeannounce.Address))
	s.mu.Unlock()

	s.logger.Printf(
		"Peer %s deannounced. %d unique files removed from index as a result.",
		peerToDeannounce.Address.String(), removedFileCount,
	)
	w.WriteHeader(http.StatusNoContent)
}

func (s *IndexServer) handlePostPeerReannounce(w http.ResponseWriter, r *http.Request) {
	var peerToUpdate protocol.PeerInfo
	if err := json.NewDecoder(r.Body).Decode(&peerToUpdate); err != nil {
		http.Error(w, "invalid JSON request body for reannounce", http.StatusBadRequest)
		s.logger.Printf("Error decoding reannounce request: %v", err)
		return
	}

	if !peerToUpdate.Address.IsValid() {
		http.Error(w, "invalid reannounce address", http.StatusBadRequest)
		s.logger.Printf("Invalid reannounce address: %s", peerToUpdate.Address.String())
		return
	}

	s.mu.Lock()
	s.removePeerAssociations(peerToUpdate.Address)
	s.upsertPeerAssociations(peerToUpdate)
	s.mu.Unlock()

	s.logger.Printf(
		"Peer %s re-announced with %d files.",
		peerToUpdate.Address.String(), len(peerToUpdate.Files),
	)
	w.WriteHeader(http.StatusNoContent)
}

// upsertPeerAssociations adds/updates a peer's presence and its shared files.
// Assumes s.mu is already locked.
func (s *IndexServer) upsertPeerAssociations(peerInfo protocol.PeerInfo) {
	s.removePeerAssociations(peerInfo.Address)

	for _, file := range peerInfo.Files {
		if _, ok := s.fileInfo[file.Checksum]; !ok {
			s.fileInfo[file.Checksum] = file
		}
		s.files[file.Checksum] = append(s.files[file.Checksum], protocol.PeerInfo{Address: peerInfo.Address})
	}
}

// removePeerAssociations removes all records of a peer and its files.
// It returns a list of checksums for files that were completely removed from the index
// (i.e., no other peer was sharing them and their metadata was deleted).
// Assumes s.mu is already locked.
func (s *IndexServer) removePeerAssociations(peerAddr netip.AddrPort) (removedFileChecksums []string) {
	for checksum, currentPeersForFile := range s.files {
		updatedPeersForFile := make([]protocol.PeerInfo, 0, len(currentPeersForFile))
		peerWasPresent := false
		for _, p := range currentPeersForFile {
			if p.Address != peerAddr {
				updatedPeersForFile = append(updatedPeersForFile, p)
			} else {
				peerWasPresent = true
			}
		}

		if peerWasPresent {
			if len(updatedPeersForFile) == 0 {
				delete(s.files, checksum)
				delete(s.fileInfo, checksum)
				removedFileChecksums = append(removedFileChecksums, checksum)
			} else {
				s.files[checksum] = updatedPeersForFile
			}
		}
	}
	return removedFileChecksums
}

func (s *IndexServer) handleGetAllFiles(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	files := make([]protocol.FileMeta, 0, len(s.fileInfo))
	for _, file := range s.fileInfo {
		files = append(files, file)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		s.logger.Printf("Error encoding all file metadata: %v", err)
		http.Error(w, "Failed to encode file metadata list", http.StatusInternalServerError)
	}
}

func (s *IndexServer) handleGetOneFile(w http.ResponseWriter, r *http.Request) {
	checksum := r.PathValue("checksum")
	if checksum == "" {
		http.Error(w, "checksum path parameter is required", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	file, ok := s.fileInfo[checksum]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("file with checksum %s not found", checksum), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(file); err != nil {
		s.logger.Printf("Error encoding file metadata for checksum %s: %v", checksum, err)
		http.Error(w, "failed to encode file metadata", http.StatusInternalServerError)
	}
}

func (s *IndexServer) handleGetOneFilePeers(w http.ResponseWriter, r *http.Request) {
	checksum := r.PathValue("checksum")
	if checksum == "" {
		http.Error(w, "missing checksum in path", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	peers, ok := s.files[checksum]
	if !ok {
		http.Error(w, fmt.Sprintf("no peers found for file with checksum %s", checksum), http.StatusNotFound)
		return
	}

	peerAddresses := make([]netip.AddrPort, 0, len(peers))
	for _, peer := range peers {
		peerAddresses = append(peerAddresses, peer.Address)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(peerAddresses); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
		return
	}
}

func (s *IndexServer) handleSearchFiles(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing 'name' query parameter", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var filesFound []protocol.FileMeta
	for _, file := range s.fileInfo {
		if file.Name == name || file.Checksum == name {
			filesFound = append(filesFound, file)
		}
	}
	if err := json.NewEncoder(w).Encode(filesFound); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
	}
}
