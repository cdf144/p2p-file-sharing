package corepeer

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

const (
	STALE_PEER_THRESHOLD       = 1 * time.Minute
	UNREACHABLE_PEER_THRESHOLD = 3 * time.Minute
	CLEANUP_TICKER_INTERVAL    = 30 * time.Second
)

type PeerStatus int

const (
	PeerStatusConnecting PeerStatus = iota
	PeerStatusConnected
	PeerStatusDisconnected
	PeerStatusUnreachable
)

type PeerEventType int

const (
	PeerEventConnected PeerEventType = iota
	PeerEventDisconnected
	PeerEventUpdated
	PeerEventUnreachable
)

type PeerRegistryInfo struct {
	Address      netip.AddrPort      `json:"address" ts_type:"string"`
	Status       PeerStatus          `json:"status"`
	LastSeen     time.Time           `json:"lastSeen" ts_type:"Date"`
	ConnectedAt  time.Time           `json:"connectedAt" ts_type:"Date"`
	SharedFiles  []protocol.FileMeta `json:"sharedFiles"`
	FailureCount int                 `json:"failureCount"`
}

type PeerRegistry struct {
	mu             sync.RWMutex
	peers          map[netip.AddrPort]*PeerRegistryInfo
	logger         *log.Logger
	eventCh        chan PeerEvent
	internalCtx    context.Context // manages the lifecycle of the registry's own goroutines
	internalCancel context.CancelFunc
	loopsWg        sync.WaitGroup
}

type PeerEvent struct {
	Type    PeerEventType
	Address netip.AddrPort
	Data    any
}

func NewPeerRegistry(logger *log.Logger) *PeerRegistry {
	ctx, cancel := context.WithCancel(context.Background())

	pr := &PeerRegistry{
		peers:          make(map[netip.AddrPort]*PeerRegistryInfo),
		logger:         logger,
		eventCh:        make(chan PeerEvent, 100),
		internalCtx:    ctx,
		internalCancel: cancel,
	}

	pr.logger.Println("PeerRegistry: Initializing and starting internal loops.")
	pr.loopsWg.Add(2)
	go pr.eventLoop()
	go pr.cleanupLoop()

	return pr
}

// Shutdown permanently stops all background activity (eventLoop, cleanupLoop) of the PeerRegistry.
func (pr *PeerRegistry) Shutdown() {
	if pr.internalCancel != nil {
		pr.internalCancel()
	}

	done := make(chan struct{})
	go func() {
		pr.loopsWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		pr.logger.Println("PeerRegistry: All internal loops have finished.")
	case <-time.After(5 * time.Second):
		pr.logger.Println("PeerRegistry: Timeout waiting for internal loops to finish during shutdown.")
	}
}

func (pr *PeerRegistry) AddPeer(addr netip.AddrPort, files []protocol.FileMeta) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	peer, ok := pr.peers[addr]
	if !ok {
		peer = &PeerRegistryInfo{
			Address:     addr,
			Status:      PeerStatusDisconnected,
			SharedFiles: files,
			LastSeen:    time.Now(),
		}
		pr.peers[addr] = peer
		pr.logger.Printf("Added new peer: %s (files: %d)", addr, len(files))
	} else {
		existingFilesMap := make(map[string]protocol.FileMeta)
		for _, f := range peer.SharedFiles {
			existingFilesMap[f.Checksum] = f
		}
		for _, newFile := range files {
			existingFilesMap[newFile.Checksum] = newFile
		}
		updatedFilesList := make([]protocol.FileMeta, 0, len(existingFilesMap))
		for _, f := range existingFilesMap {
			updatedFilesList = append(updatedFilesList, f)
		}
		peer.SharedFiles = updatedFilesList
		peer.LastSeen = time.Now()
		pr.logger.Printf("Updated peer: %s (total files: %d)", addr, len(peer.SharedFiles))
	}
}

func (pr *PeerRegistry) UpdatePeerStatus(addr netip.AddrPort, status PeerStatus) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	peer, ok := pr.peers[addr]
	if !ok {
		pr.logger.Printf("Peer %s not found in registry. Creating new entry for status update to %v.", addr, status)
		peer = &PeerRegistryInfo{
			Address:     addr,
			LastSeen:    time.Now(),
			SharedFiles: []protocol.FileMeta{},
		}
		pr.peers[addr] = peer
	}

	oldStatus := peer.Status
	peer.Status = status
	peer.LastSeen = time.Now()

	if status == PeerStatusConnected && oldStatus != PeerStatusConnected {
		peer.ConnectedAt = time.Now()
		peer.FailureCount = 0
		pr.notifyEvent(PeerEventConnected, addr, peer)
	} else if status == PeerStatusDisconnected && oldStatus == PeerStatusConnected {
		pr.notifyEvent(PeerEventDisconnected, addr, peer)
	} else if status == PeerStatusUnreachable {
		peer.FailureCount++
		pr.notifyEvent(PeerEventUnreachable, addr, peer)
	}

	pr.logger.Printf("Peer %s status changed: %v -> %v", addr, oldStatus, status)
}

func (pr *PeerRegistry) GetPeers() map[netip.AddrPort]*PeerRegistryInfo {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := make(map[netip.AddrPort]*PeerRegistryInfo, len(pr.peers))
	for addr, peer := range pr.peers {
		peerCopy := *peer
		result[addr] = &peerCopy
	}
	return result
}

func (pr *PeerRegistry) GetConnectedPeers() []netip.AddrPort {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	var connected []netip.AddrPort
	for addr, peer := range pr.peers {
		if peer.Status == PeerStatusConnected {
			connected = append(connected, addr)
		}
	}
	return connected
}

func (pr *PeerRegistry) GetPeersWithFile(checksum string) []netip.AddrPort {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	var peers []netip.AddrPort
	for addr, peer := range pr.peers {
		for _, file := range peer.SharedFiles {
			if file.Checksum == checksum {
				peers = append(peers, addr)
				break
			}
		}
	}
	return peers
}

func (pr *PeerRegistry) RemovePeer(addr netip.AddrPort) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, ok := pr.peers[addr]; ok {
		delete(pr.peers, addr)
		pr.logger.Printf("Removed peer: %s", addr)
	}
}

func (pr *PeerRegistry) notifyEvent(eventType PeerEventType, addr netip.AddrPort, data interface{}) {
	select {
	case pr.eventCh <- PeerEvent{Type: eventType, Address: addr, Data: data}:
	default:
		pr.logger.Printf("Warning: Peer event channel full, dropping event for %s", addr)
	}
}

func (pr *PeerRegistry) eventLoop() {
	defer pr.loopsWg.Done()
	for {
		select {
		case <-pr.internalCtx.Done():
			return
		case event := <-pr.eventCh:
			pr.handleEvent(event)
		}
	}
}

func (pr *PeerRegistry) handleEvent(event PeerEvent) {
	switch event.Type {
	case PeerEventConnected:
		pr.logger.Printf("Peer connected: %s", event.Address)
	case PeerEventDisconnected:
		pr.logger.Printf("Peer disconnected: %s", event.Address)
	case PeerEventUnreachable:
		if peer, ok := event.Data.(*PeerRegistryInfo); ok && peer.FailureCount > 5 {
			pr.logger.Printf("Peer %s marked as unreachable after %d failures", event.Address, peer.FailureCount)
		}
	}
}

func (pr *PeerRegistry) cleanupLoop() {
	defer pr.loopsWg.Done()

	ticker := time.NewTicker(CLEANUP_TICKER_INTERVAL)
	defer ticker.Stop()

	for {
		select {
		case <-pr.internalCtx.Done():
			return
		case <-ticker.C:
			pr.cleanupStaleConnections()
		}
	}
}

func (pr *PeerRegistry) cleanupStaleConnections() {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	now := time.Now()

	var toRemove []netip.AddrPort
	for addr, peer := range pr.peers {
		timeSinceLastSeen := now.Sub(peer.LastSeen)

		if timeSinceLastSeen > UNREACHABLE_PEER_THRESHOLD || peer.FailureCount > 3 {
			toRemove = append(toRemove, addr)
		} else if timeSinceLastSeen > STALE_PEER_THRESHOLD && peer.Status == PeerStatusConnected {
			peer.Status = PeerStatusDisconnected
		}
	}

	for _, addr := range toRemove {
		delete(pr.peers, addr)
	}

	if len(toRemove) > 0 {
		pr.logger.Printf("Cleaned up %d stale peer connections", len(toRemove))
	}
}
