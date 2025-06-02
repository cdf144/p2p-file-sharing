package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

// TODO: replace IndexServer with a persistent storage solution, i.e., a database.

// IndexServer is a temporary in-memory index server that stores file metadata.
type IndexServer struct {
	mu       sync.RWMutex
	files    map[string][]protocol.PeerInfo // Maps file Checksum to a list of PeerInfo
	fileInfo map[string]protocol.FileMeta   // Maps file Checksum to file metadata
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
	}

	http.HandleFunc("/announce", indexServer.handleAnnounce)
	http.HandleFunc("/search", indexServer.handleSearch)
	http.HandleFunc("/peers", indexServer.handleAllPeers)
	http.HandleFunc("/peers/{checksum}", indexServer.handleOnePeers)
	// TODO: Add a de-announce endpoint.

	log.Printf("[index-server] Starting server on %s", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[index-server] Failed to start server: %v", err)
	}
}

func (s *IndexServer) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	var peer protocol.PeerInfo
	if err := json.NewDecoder(r.Body).Decode(&peer); err != nil {
		http.Error(w, "invalid JSON request body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, file := range peer.Files {
		s.files[file.Checksum] = append(s.files[file.Checksum], peer)
		if _, ok := s.fileInfo[file.Checksum]; !ok {
			s.fileInfo[file.Checksum] = file
		}
	}
	log.Printf(
		"[index-server] Registered peer %s:%d with %d files\n",
		peer.IP,
		peer.Port,
		len(peer.Files),
	)
}

func (s *IndexServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q query parameter", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []protocol.FileMeta
	for _, file := range s.fileInfo {
		if file.Name == q || file.Checksum == q {
			res = append(res, file)
		}
	}
	if err := json.NewEncoder(w).Encode(res); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		log.Printf("[index-server] Failed to encode response: %v\n", err)
	}
}

func (s *IndexServer) handleAllPeers(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peerMap := make(map[string]protocol.PeerInfo)
	for _, peers := range s.files {
		for _, peer := range peers {
			key := fmt.Sprintf("%s:%d", peer.IP.String(), peer.Port)
			peerMap[key] = peer
		}
	}

	var allPeers []protocol.PeerInfo
	for _, peer := range peerMap {
		allPeers = append(allPeers, peer)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allPeers); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		log.Printf("[index-server] Failed to encode response: %v\n", err)
	}
}

func (s *IndexServer) handleOnePeers(w http.ResponseWriter, r *http.Request) {
	checksum := r.PathValue("checksum")
	if checksum == "" {
		http.Error(w, "missing checksum query parameter", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers, ok := s.files[checksum]
	if !ok {
		http.Error(w, fmt.Sprintf("file with checksum %s not found", checksum), http.StatusNotFound)
		return
	}
	if err := json.NewEncoder(w).Encode(peers); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		log.Printf("[index-server] Failed to encode response: %v\n", err)
	}
}
