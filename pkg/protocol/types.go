package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/netip"
)

// PeerInfo represents information about a peer in the P2P network,
// including its network address and a list of files it shares.
type PeerInfo struct {
	Address netip.AddrPort `json:"address" ts_type:"string"`
	Files   []FileMeta     `json:"files"`
	TLS     bool           `json:"tls"`
}

// PeerInfoSummary provides a summary of a peer's information,
// including its network address and the count of files it shares.
// This is useful for quickly listing peers without exposing their file details.
type PeerInfoSummary struct {
	Address   netip.AddrPort `json:"address" ts_type:"string"`
	FileCount int            `json:"fileCount"`
	TLS       bool           `json:"tls"`
}

// FileMeta represents the metadata of a file shared by a peer in the P2P network.
// It includes the checksum for integrity verification,
// the name of the file, and its size in bytes.
// For chunk-based transfer, it also includes chunk details.
type FileMeta struct {
	Checksum    string   `json:"checksum"`
	Name        string   `json:"name"`
	Size        int64    `json:"size"`
	ChunkSize   int64    `json:"chunkSize"`
	NumChunks   int      `json:"numChunks"`
	ChunkHashes []string `json:"chunkHashes"`
}

// MessageType represents the different types of messages in the P2P protocol
type MessageType uint8

const (
	FILE_REQUEST MessageType = iota
	FILE_DATA
	CHUNK_REQUEST
	CHUNK_DATA
	ERROR
)

const CHUNK_SIZE = 1024 * 1024 // 1 MB

// String returns the string representation of the MessageType
func (mt MessageType) String() string {
	switch mt {
	case FILE_REQUEST:
		return "FILE_REQUEST"
	case FILE_DATA:
		return "FILE_DATA"
	case CHUNK_REQUEST:
		return "CHUNK_REQUEST"
	case CHUNK_DATA:
		return "CHUNK_DATA"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// GenerateChecksum computes the SHA256 checksum from an io.Reader.
// If the reader is nil, it returns an io.ErrUnexpectedEOF error.
func GenerateChecksum(r io.Reader) (string, error) {
	if r == nil {
		return "", io.ErrUnexpectedEOF
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
