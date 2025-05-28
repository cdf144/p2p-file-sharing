package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
)

// PeerInfo represents information about a peer in the P2P network,
// including its network address and the files it shares.
type PeerInfo struct {
	IP    net.IP
	Port  uint16
	Files []FileMeta
}

// FileMeta represents the metadata of a file shared by a peer in the P2P network.
// It includes the checksum for integrity verification,
// the name of the file, and its size in bytes.
type FileMeta struct {
	Checksum string
	Name     string
	Size     int64
}

// GenerateChecksum calculates the SHA256 checksum of the given file data
// and returns it as a hexadecimal encoded string.
func GenerateChecksum(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
