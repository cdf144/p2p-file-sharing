package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
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

	http.HandleFunc("/peers", indexServer.PeersHandler)
	// Catches /peers/announce, /peers/deannounce, /peers/{ip}/{port}/files
	http.HandleFunc("/peers/", indexServer.PeersHandler)

	http.HandleFunc("/files", indexServer.FilesHandler)
	// Catches /files/{checksum} and /files/{checksum}/peers
	http.HandleFunc("/files/", indexServer.FilesHandler)

	http.HandleFunc("/search", indexServer.SearchHandler)
	// Catches /search/files?name={name}
	http.HandleFunc("/search/", indexServer.SearchHandler)

	indexServer.logger.Println("Starting server on", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		indexServer.logger.Fatalf("Failed to start server: %v", err)
	}
}

func (s *IndexServer) PeersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		switch r.URL.Path {
		case "/peers":
			s.handleGetAllPeers(w, r)
		default:
			// Handle requests like /peers/{ip}/{port}/files
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/peers/"), "/")
			if len(parts) == 3 && parts[2] == "files" {
				r.SetPathValue("ip", parts[0])
				r.SetPathValue("port", parts[1])
				s.handleGetOnePeerFiles(w, r)
				return
			}

			http.Error(w, "not found", http.StatusNotFound)
			s.logger.Printf("Could not handle request: %s %s", r.Method, r.URL.Path)
		}
	case http.MethodPost:
		switch r.URL.Path {
		case "/peers/announce":
			s.handlePostPeerAnnounce(w, r)
		case "/peers/deannounce":
			s.handlePostPeerDeannounce(w, r)
		case "/peers/reannounce":
			s.handlePostPeerReannounce(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
			s.logger.Printf("Could not handle request: %s %s", r.Method, r.URL.Path)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	peerAddr := peerInfo.Address
	for _, file := range peerInfo.Files {
		s.fileInfo[file.Checksum] = file

		currentPeersForFile := s.files[file.Checksum]
		exist := false
		for i, currentPeer := range currentPeersForFile {
			if currentPeer.Address == peerAddr {
				exist = true
				currentPeersForFile[i] = peerInfo
				break
			}
		}
		if !exist {
			currentPeersForFile = append(currentPeersForFile, peerInfo)
		}
		s.files[file.Checksum] = currentPeersForFile
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

func (s *IndexServer) FilesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		switch r.URL.Path {
		case "/files":
			s.handleGetAllFiles(w, r)
		default:
			// Handle requests like /files/{checksum} or /files/{checksum}/peers
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/files/"), "/")
			if len(parts) == 1 {
				r.SetPathValue("checksum", parts[0])
				s.handleGetOneFile(w, r)
				return
			} else if len(parts) == 2 && parts[1] == "peers" {
				r.SetPathValue("checksum", parts[0])
				s.handleGetOneFilePeers(w, r)
				return
			}

			http.Error(w, "not found", http.StatusNotFound)
			s.logger.Printf("Could not handle request: %s %s", r.Method, r.URL.Path)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *IndexServer) handleGetAllFiles(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var files []protocol.FileMeta
	for _, file := range s.fileInfo {
		files = append(files, file)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
		return
	}
}

func (s *IndexServer) handleGetOneFile(w http.ResponseWriter, r *http.Request) {
	checksum := r.PathValue("checksum")
	if checksum == "" {
		http.Error(w, "missing checksum in path", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.fileInfo[checksum]
	if !ok {
		http.Error(w, fmt.Sprintf("file with checksum %s not found", checksum), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(file); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		s.logger.Printf("Failed to encode response: %v", err)
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

func (s *IndexServer) SearchHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		switch r.URL.Path {
		case "/search":
			http.Error(w, "search endpoint not implemented", http.StatusNotImplemented)
		case "/search/files":
			s.handleSearchFiles(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
			s.logger.Printf("Could not handle request: %s %s", r.Method, r.URL.Path)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
