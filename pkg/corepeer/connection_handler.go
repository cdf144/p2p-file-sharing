package corepeer

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

const (
	PERSISTENT_CONNECTION_IDLE_TIMEOUT = 2 * time.Minute
	ACTIVE_REQUEST_PROCESSING_TIMEOUT  = 90 * time.Second
)

type ConnectionHandler struct {
	logger       *log.Logger
	fileManager  *FileManager
	peerRegistry *PeerRegistry
}

func NewConnectionHandler(logger *log.Logger, fileManager *FileManager, peerRegistry *PeerRegistry) *ConnectionHandler {
	return &ConnectionHandler{
		logger:       logger,
		fileManager:  fileManager,
		peerRegistry: peerRegistry,
	}
}

func (c *ConnectionHandler) HandleConnection(conn net.Conn) {
	defer conn.Close()

	isTLS := false
	if _, ok := conn.(*tls.Conn); ok {
		isTLS = true
		c.logger.Printf("Handling TLS connection from %s", conn.RemoteAddr().String())
	} else {
		c.logger.Printf("Handling TCP connection from %s", conn.RemoteAddr().String())
	}

	if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if addr, err := netip.ParseAddrPort(tcpAddr.String()); err == nil {
			c.peerRegistry.UpdatePeerStatus(addr, PeerStatusConnected)
			c.peerRegistry.UpdatePeerTLS(addr, isTLS)
			defer c.peerRegistry.UpdatePeerStatus(addr, PeerStatusDisconnected)
		}
	}

	c.logger.Printf("Handling new persistent connection from %s", conn.RemoteAddr().String())

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(PERSISTENT_CONNECTION_IDLE_TIMEOUT)); err != nil {
			c.logger.Printf("Error setting read deadline for %s: %v. Closing session.", conn.RemoteAddr(), err)
			return
		}

		msgTypeByte, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				c.logger.Printf("Connection closed by client %s (EOF while reading msg type). Ending session.", conn.RemoteAddr())
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				c.logger.Printf(
					"Connection from %s timed out waiting for message type (idle_timeout: %s). Ending session.",
					conn.RemoteAddr(), PERSISTENT_CONNECTION_IDLE_TIMEOUT,
				)
			} else {
				c.logger.Printf("Failed to read message type from %s: %v. Ending session.", conn.RemoteAddr(), err)
			}
			return
		}

		if err := conn.SetReadDeadline(time.Now().Add(ACTIVE_REQUEST_PROCESSING_TIMEOUT)); err != nil {
			c.logger.Printf("Error setting active request read deadline for %s: %v. Closing session.", conn.RemoteAddr(), err)
			return
		}

		msgType := protocol.MessageType(msgTypeByte)
		c.logger.Printf("Received message type %s (%d) from %s on persistent connection", msgType.String(), msgTypeByte, conn.RemoteAddr())

		var requestHandledSuccessfully bool
		switch msgType {
		case protocol.CHUNK_REQUEST:
			c.handleChunkRequestCommand(conn, reader, writer)
			requestHandledSuccessfully = true
		case protocol.FILE_REQUEST:
			c.handleFileRequestCommand(conn, reader, writer)
			requestHandledSuccessfully = true
		default:
			c.logger.Printf("Warning: Received unknown message type %d from %s on persistent connection", msgTypeByte, conn.RemoteAddr())
			c.sendError(writer, conn.RemoteAddr().String(), "Unknown message type")
			c.logger.Printf("Terminating session with %s due to unknown message type.", conn.RemoteAddr())
			return
		}

		if !requestHandledSuccessfully {
			c.logger.Printf("Request handler indicated an issue, terminating session with %s.", conn.RemoteAddr())
			return
		}
	}
}

// handleChunkRequestCommand processes a CHUNK_REQUEST message.
func (c *ConnectionHandler) handleChunkRequestCommand(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer) {
	fileChecksum, err := reader.ReadString('\n')
	if err != nil {
		c.logger.Printf("Warning: Failed to read file checksum for CHUNK_REQUEST from %s: %v", conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to read file checksum")
		return
	}
	fileChecksum = strings.TrimSpace(fileChecksum)

	chunkIndexStr, err := reader.ReadString('\n')
	if err != nil {
		c.logger.Printf("Warning: Failed to read chunk index for CHUNK_REQUEST from %s: %v", conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to read chunk index")
		return
	}
	chunkIndexStr = strings.TrimSpace(chunkIndexStr)
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		c.logger.Printf("Warning: Invalid chunk index '%s' for CHUNK_REQUEST from %s: %v", chunkIndexStr, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Invalid chunk index format")
		return
	}

	fileMeta, err := c.fileManager.GetFileMetaByChecksum(fileChecksum)
	if err != nil {
		c.logger.Printf("Warning: FileMeta not found for checksum %s (CHUNK_REQUEST by %s): %v", fileChecksum, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), fmt.Sprintf("File not found for checksum %s", fileChecksum))
		return
	}

	if chunkIndex < 0 || (fileMeta.NumChunks > 0 && chunkIndex >= fileMeta.NumChunks) || (fileMeta.NumChunks == 0 && chunkIndex != 0) {
		errMsg := fmt.Sprintf("Invalid chunk index %d for file %s (NumChunks: %d)", chunkIndex, fileMeta.Name, fileMeta.NumChunks)
		c.logger.Printf("Warning: %s, requested by %s", errMsg, conn.RemoteAddr())
		c.sendError(writer, conn.RemoteAddr().String(), errMsg)
		return
	}
	if fileMeta.NumChunks == 0 { // Also implies fileMeta.Size == 0
		errMsg := fmt.Sprintf("File %s is empty, no chunks to request (NumChunks: 0)", fileMeta.Name)
		c.logger.Printf("Warning: %s, requested by %s", errMsg, conn.RemoteAddr())
		c.sendError(writer, conn.RemoteAddr().String(), errMsg)
		return
	}

	filePath, err := c.fileManager.GetFilePathByChecksum(fileChecksum)
	if err != nil {
		c.logger.Printf("Warning: Failed to get file path for checksum %s (CHUNK_REQUEST by %s): %v", fileChecksum, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), fmt.Sprintf("File path not found for checksum %s", fileChecksum))
		return
	}

	fileHandle, err := os.Open(filePath)
	if err != nil {
		c.logger.Printf("Warning: Failed to open file %s for CHUNK_REQUEST by %s: %v", filePath, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to open file for chunk")
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
		c.logger.Printf("Warning: Failed to seek to chunk offset %d for file %s (CHUNK_REQUEST by %s): %v", chunkOffset, filePath, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to seek to chunk")
		return
	}

	n, err := io.ReadFull(fileHandle, chunkData)
	if err != nil {
		c.logger.Printf("Warning: Failed to read chunk %d from file %s (expected %d bytes, got %d) for %s: %v", chunkIndex, filePath, bytesToRead, n, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to read chunk data")
		return
	}
	actualChunkData := chunkData[:n]

	conn.SetWriteDeadline(time.Now().Add(1 * time.Minute))

	if _, err := writer.Write([]byte{byte(protocol.CHUNK_DATA)}); err != nil {
		c.logger.Printf("Warning: Failed to write CHUNK_DATA message type to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(fileChecksum + "\n"); err != nil {
		c.logger.Printf("Warning: Failed to write file checksum to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(strconv.Itoa(chunkIndex) + "\n"); err != nil {
		c.logger.Printf("Warning: Failed to write chunk index to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.WriteString(fmt.Sprintf("%d\n", int64(n))); err != nil {
		c.logger.Printf("Warning: Failed to write chunk length to buffer for %s: %v", conn.RemoteAddr(), err)
		return
	}
	if _, err := writer.Write(actualChunkData); err != nil {
		c.logger.Printf(
			"Warning: Failed to write chunk %d payload for file %s to buffer for %s: %v",
			chunkIndex, fileMeta.Name, conn.RemoteAddr(), err,
		)
		return
	}

	if err := writer.Flush(); err != nil {
		c.logger.Printf(
			"Warning: Failed to flush CHUNK_DATA message for file %s chunk %d to %s: %v",
			fileMeta.Name, chunkIndex, conn.RemoteAddr(), err,
		)
		return
	}

	c.logger.Printf(
		"Successfully sent chunk %d (%d bytes) for file %s (%s) to %s",
		chunkIndex, n, fileMeta.Name, fileChecksum, conn.RemoteAddr(),
	)

	if remoteTCPAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if remoteNetIPAddr, err := netip.ParseAddrPort(remoteTCPAddr.String()); err == nil {
			c.peerRegistry.RecordPeerActivity(remoteNetIPAddr)
		}
	}
}

// handleFileRequestCommand processes a FILE_REQUEST message.
func (c *ConnectionHandler) handleFileRequestCommand(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer) {
	checksum, err := reader.ReadString('\n')
	if err != nil {
		c.logger.Printf("Warning: Failed to read checksum for FILE_REQUEST from %s: %v", conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), "Failed to read checksum")
		return
	}
	checksum = strings.TrimSpace(checksum)

	localFile, err := c.fileManager.GetLocalFileInfoByChecksum(checksum)
	if err != nil {
		c.logger.Printf("Warning: File not found for checksum %s (FILE_REQUEST by %s): %v", checksum, conn.RemoteAddr(), err)
		c.sendError(writer, conn.RemoteAddr().String(), fmt.Sprintf("File not found for checksum %s", checksum))
		return
	}

	if _, err := writer.Write([]byte{byte(protocol.FILE_DATA)}); err != nil {
		c.logger.Printf("Warning: Failed to write FILE_DATA message type to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}
	if _, err := writer.WriteString(fmt.Sprintf("%d\n", localFile.Size)); err != nil {
		c.logger.Printf("Warning: Failed to write file size to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}
	if err := writer.Flush(); err != nil {
		c.logger.Printf("Warning: Failed to flush FILE_DATA header to %s for %s: %v", conn.RemoteAddr(), localFile.Name, err)
		return
	}

	conn.SetWriteDeadline(time.Now().Add(5 * time.Minute))

	fileHandle, err := os.Open(localFile.Path)
	if err != nil {
		c.logger.Printf("Warning: Failed to open file %s for FILE_REQUEST by %s: %v", localFile.Path, conn.RemoteAddr(), err)
		return
	}
	defer fileHandle.Close()

	sent, err := io.Copy(conn, fileHandle)
	if err != nil {
		c.logger.Printf("Warning: Failed to send file %s to %s: %v", localFile.Name, conn.RemoteAddr(), err)
		return
	}
	if sent != localFile.Size {
		c.logger.Printf("Warning: Incomplete file transfer for %s to %s: sent %d, expected %d", localFile.Name, conn.RemoteAddr(), sent, localFile.Size)
		return
	}

	c.logger.Printf("Successfully sent file %s (%d bytes) to %s via FILE_REQUEST", localFile.Name, sent, conn.RemoteAddr())
}

func (c *ConnectionHandler) sendError(writer *bufio.Writer, remoteAddr string, errorMessage string) {
	if _, err := writer.Write([]byte{byte(protocol.ERROR)}); err != nil {
		c.logger.Printf("Warning: Failed to write ERROR message type to %s: %v", remoteAddr, err)
		return
	}
	if _, err := writer.WriteString(errorMessage + "\n"); err != nil {
		c.logger.Printf("Warning: Failed to write error message string to %s: %v", remoteAddr, err)
		return
	}
	if err := writer.Flush(); err != nil {
		c.logger.Printf("Warning: Failed to flush error message to %s: %v", remoteAddr, err)
	}
}
