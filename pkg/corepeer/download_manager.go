package corepeer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdf144/p2p-file-sharing/pkg/protocol"
)

type DownloadManager struct {
	logger      *log.Logger
	indexClient *IndexClient
}

func NewDownloadManager(logger *log.Logger, indexClient *IndexClient) *DownloadManager {
	return &DownloadManager{
		logger:      logger,
		indexClient: indexClient,
	}
}

func (dm *DownloadManager) DownloadFile(ctx context.Context, fileMeta protocol.FileMeta, savePath string) error {
	if fileMeta.NumChunks == 0 && fileMeta.Size > 0 {
		return fmt.Errorf("file metadata indicates non-empty file but zero chunks for %s", fileMeta.Name)
	}
	if fileMeta.Size == 0 {
		dm.logger.Printf("File %s is empty (size 0). Creating empty file at %s.", fileMeta.Name, savePath)
		emptyFile, err := os.Create(savePath)
		if err != nil {
			return fmt.Errorf("failed to create empty file %s: %w", savePath, err)
		}
		emptyFile.Close()
		emptyHasher := sha256.New()
		emptyChecksum := hex.EncodeToString(emptyHasher.Sum(nil))
		if fileMeta.Checksum != emptyChecksum {
			os.Remove(savePath)
			return fmt.Errorf("checksum mismatch for empty file %s: expected %s, got %s", fileMeta.Name, emptyChecksum, fileMeta.Checksum)
		}
		dm.logger.Printf("Successfully verified empty file %s.", fileMeta.Name)
		return nil
	}
	if fileMeta.NumChunks > 0 && len(fileMeta.ChunkHashes) != fileMeta.NumChunks {
		return fmt.Errorf(
			"inconsistent chunk hash count for %s: expected %d, got %d",
			fileMeta.Name, fileMeta.NumChunks, len(fileMeta.ChunkHashes),
		)
	}

	peerAddresses, err := dm.indexClient.QueryFilePeers(ctx, fileMeta.Checksum)
	if err != nil {
		return fmt.Errorf("failed to query peers for file %s: %w", fileMeta.Checksum, err)
	}
	if len(peerAddresses) == 0 {
		return fmt.Errorf("no peers found for file %s (checksum: %s)", fileMeta.Name, fileMeta.Checksum)
	}

	dm.logger.Printf(
		"Starting parallel download of %s (%s, %d chunks) from %d available peers to %s",
		fileMeta.Name, fileMeta.Checksum, fileMeta.NumChunks, len(peerAddresses), savePath,
	)

	file, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", savePath, err)
	}
	defer func() {
		file.Close()
		if err != nil { // if this method (DownloadFile) has an error
			if _, statErr := os.Stat(savePath); !os.IsNotExist(statErr) {
				os.Remove(savePath)
			}
		}
	}()

	if err := file.Truncate(fileMeta.Size); err != nil {
		return fmt.Errorf("failed to pre-allocate file size for %s: %w", savePath, err)
	}

	downloadedChunkData := make([][]byte, fileMeta.NumChunks)
	chunksPending := make(map[int]struct{})
	for i := range fileMeta.NumChunks {
		chunksPending[i] = struct{}{}
	}
	chunkFailureCounts := make(map[int]int)

	downloadPasses := 0
	maxDownloadPasses := len(peerAddresses) + 2 // Extra passes for retries (arbitrary number, can be tuned)

	rand.New(rand.NewSource(time.Now().UnixNano()))

	for len(chunksPending) > 0 && downloadPasses < maxDownloadPasses {
		downloadPasses++
		dm.logger.Printf("Starting download pass %d for %s. Chunks pending: %d", downloadPasses, fileMeta.Name, len(chunksPending))

		if ctx.Err() != nil {
			return fmt.Errorf("download context cancelled before pass %d: %w", downloadPasses, ctx.Err())
		}

		chunkJobQueue := make(chan int, len(chunksPending))
		for chunkIdx := range chunksPending {
			if chunkFailureCounts[chunkIdx] < maxRetriesPerChunk {
				chunkJobQueue <- chunkIdx
			} else {
				dm.logger.Printf("Chunk %d for %s has reached max retries (%d), skipping.", chunkIdx, fileMeta.Name, maxRetriesPerChunk)
				delete(chunksPending, chunkIdx)
			}
		}
		close(chunkJobQueue)

		if len(chunkJobQueue) == 0 && len(chunksPending) > 0 {
			break
		}
		if len(chunkJobQueue) == 0 && len(chunksPending) == 0 {
			break
		}

		resultsChan := make(chan chunkDownloadResult, fileMeta.NumChunks)
		var workersWg sync.WaitGroup

		shuffledPeers := make([]netip.AddrPort, len(peerAddresses))
		copy(shuffledPeers, peerAddresses)
		rand.Shuffle(len(shuffledPeers), func(i, j int) { shuffledPeers[i], shuffledPeers[j] = shuffledPeers[j], shuffledPeers[i] })

		numWorkers := min(len(shuffledPeers), maxConcurrentDownloadsPerFile, len(chunksPending))
		if numWorkers == 0 && len(chunksPending) > 0 {
			numWorkers = 1
		}

		dm.logger.Printf("Pass %d: Launching %d workers for %d jobs.", downloadPasses, numWorkers, len(chunkJobQueue))

		for i := range numWorkers {
			peerAddr := shuffledPeers[i%len(shuffledPeers)]
			workersWg.Add(1)
			go dm.runPeerDownloadSession(ctx, peerAddr, fileMeta, chunkJobQueue, resultsChan, &workersWg)
		}

		jobsProcessed := 0
		expectedJobs := len(chunkJobQueue)

	passResultLoop:
		for jobsProcessed < expectedJobs {
			select {
			case <-ctx.Done():
				dm.logger.Printf("Download context cancelled during pass %d results processing: %v", downloadPasses, ctx.Err())
				err = ctx.Err()
				break passResultLoop
			case res, ok := <-resultsChan:
				if !ok { // Should not happen if workersWg is handled correctly
					dm.logger.Printf("Results channel closed unexpectedly during pass %d.", downloadPasses)
					if jobsProcessed < expectedJobs {
						err = fmt.Errorf("results channel closed prematurely")
					}
					break passResultLoop
				}

				jobsProcessed++
				if res.err != nil {
					dm.logger.Printf(
						"Pass %d: Chunk %d failed (peer: %s, error: %v). Will retry if possible.",
						downloadPasses, res.index, res.peer, res.err,
					)
					chunkFailureCounts[res.index]++
					if chunkFailureCounts[res.index] >= maxRetriesPerChunk {
						dm.logger.Printf(
							"Chunk %d for %s has now reached max retries (%d) after failure in pass %d.",
							res.index, fileMeta.Name, maxRetriesPerChunk, downloadPasses,
						)
						delete(chunksPending, res.index)
					}
					continue
				}

				currentChunkHasher := sha256.New()
				currentChunkHasher.Write(res.data)
				actualChunkChecksum := hex.EncodeToString(currentChunkHasher.Sum(nil))

				if actualChunkChecksum != fileMeta.ChunkHashes[res.index] {
					dm.logger.Printf("Pass %d: Chunk %d checksum mismatch (expected %s, got %s). Will retry.",
						downloadPasses, res.index, fileMeta.ChunkHashes[res.index], actualChunkChecksum)
					chunkFailureCounts[res.index]++
					if chunkFailureCounts[res.index] >= maxRetriesPerChunk {
						dm.logger.Printf("Chunk %d for %s (checksum mismatch) has now reached max retries (%d).", res.index, fileMeta.Name, maxRetriesPerChunk)
						delete(chunksPending, res.index)
					}
					continue
				}

				downloadedChunkData[res.index] = res.data
				offset := int64(res.index) * fileMeta.ChunkSize
				_, writeErr := file.WriteAt(res.data, offset)
				if writeErr != nil {
					dm.logger.Printf("Pass %d: Failed to write chunk %d to file %s: %v. Marking for retry.", downloadPasses, res.index, savePath, writeErr)
					chunkFailureCounts[res.index]++
					if chunkFailureCounts[res.index] >= maxRetriesPerChunk {
						delete(chunksPending, res.index)
					}
					continue
				}

				dm.logger.Printf("Pass %d: Successfully processed chunk %d for %s.", downloadPasses, res.index, fileMeta.Name)
				delete(chunksPending, res.index)
				delete(chunkFailureCounts, res.index)
			}
		}
		if err != nil { // Context cancelled or other break from loop
			workersWg.Wait()
			return err
		}

		workersWg.Wait()
		close(resultsChan)
		// Drain any straggler results
		for res := range resultsChan {
			dm.logger.Printf("Pass %d: Drained straggler result for chunk %d (err: %v)", downloadPasses, res.index, res.err)
			if _, isPending := chunksPending[res.index]; isPending {
				if res.err != nil {
					chunkFailureCounts[res.index]++
					if chunkFailureCounts[res.index] >= maxRetriesPerChunk {
						delete(chunksPending, res.index)
					}
				}
			}
		}

		if len(chunksPending) > 0 {
			dm.logger.Printf("End of pass %d for %s. Chunks still pending: %d. Retrying.", downloadPasses, fileMeta.Name, len(chunksPending))
		}
	}

	if len(chunksPending) > 0 {
		return fmt.Errorf(
			"failed to download all chunks for %s after %d passes. %d chunks remain.",
			fileMeta.Name, downloadPasses, len(chunksPending),
		)
	}

	dm.logger.Printf(
		"All %d chunks for %s appear to be downloaded. Verifying overall file checksum.",
		fileMeta.NumChunks, fileMeta.Name,
	)

	if errSync := file.Sync(); errSync != nil {
		dm.logger.Printf("Warning: failed to sync file %s to disk: %v.", savePath, errSync)
	}
	if _, errSeek := file.Seek(0, io.SeekStart); errSeek != nil {
		return fmt.Errorf("failed to seek to start of file %s for final hashing: %w", savePath, errSeek)
	}
	fullFileHasher := sha256.New()
	if _, errCopy := io.Copy(fullFileHasher, file); errCopy != nil {
		return fmt.Errorf("failed to read file %s for final hashing: %w", savePath, errCopy)
	}

	receivedFullChecksum := hex.EncodeToString(fullFileHasher.Sum(nil))
	if receivedFullChecksum != fileMeta.Checksum {
		return fmt.Errorf(
			"overall checksum mismatch for %s: expected %s, actual %s",
			fileMeta.Name, fileMeta.Checksum, receivedFullChecksum,
		)
	}

	dm.logger.Printf("Successfully downloaded and verified file %s (%d chunks) to %s.", fileMeta.Name, fileMeta.NumChunks, savePath)
	return nil
}

// runPeerDownloadSession attempts to download multiple chunks from a single peer over a persistent connection.
// It takes chunk indices from the 'jobs' channel and sends results (data or error) to the 'results' channel.
// If the connection to the peer fails, the session is terminated.
func (dm *DownloadManager) runPeerDownloadSession(
	ctx context.Context,
	peerAddr netip.AddrPort,
	fileMeta protocol.FileMeta,
	jobs <-chan int,
	results chan<- chunkDownloadResult,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	dm.logger.Printf("Worker session starting with peer %s for file %s", peerAddr, fileMeta.Name)

	conn, err := net.DialTimeout("tcp", peerAddr.String(), peerConnectTimeout)
	if err != nil {
		dm.logger.Printf(
			"Worker for %s: failed to connect to peer %s: %v. This worker will not process jobs.",
			fileMeta.Name, peerAddr, err,
		)
		return
	}
	defer conn.Close()
	dm.logger.Printf("Worker for %s: successfully connected to peer %s", fileMeta.Name, peerAddr)

	writer := bufio.NewWriter(conn)
	reader := bufio.NewReader(conn)

	for {
		var chunkIndex int
		var ok bool

		select {
		case <-ctx.Done():
			dm.logger.Printf("Worker for %s with peer %s: context cancelled. Exiting session.", fileMeta.Name, peerAddr)
			return
		case chunkIndex, ok = <-jobs:
			if !ok {
				dm.logger.Printf("Worker for %s with peer %s: jobs channel closed. Exiting session.", fileMeta.Name, peerAddr)
				return
			}
		}

		dm.logger.Printf("Worker for %s: peer %s attempting to download chunk %d", fileMeta.Name, peerAddr, chunkIndex)
		conn.SetDeadline(time.Now().Add(chunkDownloadTimeout))

		var downloadedData []byte
		var chunkErr error

		// Send CHUNK_REQUEST
		if _, err := writer.Write([]byte{byte(protocol.CHUNK_REQUEST)}); err != nil {
			chunkErr = fmt.Errorf("conn error writing CHUNK_REQUEST type: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		if _, err := writer.WriteString(fileMeta.Checksum + "\n"); err != nil {
			chunkErr = fmt.Errorf("conn error writing file checksum: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		if _, err := writer.WriteString(strconv.Itoa(chunkIndex) + "\n"); err != nil {
			chunkErr = fmt.Errorf("conn error writing chunk index: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		if err := writer.Flush(); err != nil {
			chunkErr = fmt.Errorf("conn error flushing CHUNK_REQUEST: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}

		// Read response header
		msgTypeByte, err := reader.ReadByte()
		if err != nil {
			chunkErr = fmt.Errorf("conn error reading response type: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}

		if protocol.MessageType(msgTypeByte) == protocol.ERROR {
			errMsgFromServer, readErr := reader.ReadString('\n')
			if readErr != nil {
				dm.logger.Printf(
					"Worker for %s: peer %s sent ERROR, but failed to read full error message: %v",
					fileMeta.Name, peerAddr, readErr,
				)
				chunkErr = fmt.Errorf("peer sent ERROR, then conn error reading error message: %w", readErr)
				results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
				return
			}
			chunkErr = fmt.Errorf("peer %s returned error for chunk %d: %s", peerAddr, chunkIndex, strings.TrimSpace(errMsgFromServer))
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s reported error for chunk %d: %v. Continuing session for next job.",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			continue
		}

		if protocol.MessageType(msgTypeByte) != protocol.CHUNK_DATA {
			chunkErr = fmt.Errorf("unexpected response type %d from peer %s for chunk %d", msgTypeByte, peerAddr, chunkIndex)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf("Worker for %s: peer %s session terminated for chunk %d due to: %v", fileMeta.Name, peerAddr, chunkIndex, chunkErr)
			return
		}

		// Read CHUNK_DATA details
		respFileChecksum, err := reader.ReadString('\n')
		if err != nil {
			chunkErr = fmt.Errorf("conn error reading response file checksum: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		if strings.TrimSpace(respFileChecksum) != fileMeta.Checksum {
			chunkErr = fmt.Errorf("response file checksum mismatch from peer %s for chunk %d", peerAddr, chunkIndex)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}

		respChunkIndexStr, err := reader.ReadString('\n')
		if err != nil {
			chunkErr = fmt.Errorf("conn error reading response chunk index: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		respChunkIndex, convErr := strconv.Atoi(strings.TrimSpace(respChunkIndexStr))
		if convErr != nil || respChunkIndex != chunkIndex {
			chunkErr = fmt.Errorf(
				"response chunk index mismatch (expected %d, got '%s', err: %v) from peer %s",
				chunkIndex, strings.TrimSpace(respChunkIndexStr), convErr, peerAddr,
			)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}

		chunkLengthStr, err := reader.ReadString('\n')
		if err != nil {
			chunkErr = fmt.Errorf("conn error reading chunk length: %w", err)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}
		chunkLength, convErr := strconv.ParseInt(strings.TrimSpace(chunkLengthStr), 10, 64)
		if convErr != nil {
			chunkErr = fmt.Errorf(
				"invalid chunk length '%s' (err: %v) from peer %s",
				strings.TrimSpace(chunkLengthStr), convErr, peerAddr,
			)
			results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
			dm.logger.Printf(
				"Worker for %s: peer %s session terminated for chunk %d due to: %v",
				fileMeta.Name, peerAddr, chunkIndex, chunkErr,
			)
			return
		}

		expectedChunkSize := fileMeta.ChunkSize
		if chunkIndex == fileMeta.NumChunks-1 {
			if remainder := fileMeta.Size % fileMeta.ChunkSize; remainder != 0 {
				expectedChunkSize = remainder
			} else if fileMeta.Size > 0 {
				expectedChunkSize = fileMeta.ChunkSize
			}
		}
		if chunkLength < 0 || chunkLength > fileMeta.ChunkSize || chunkLength != expectedChunkSize {
			if !(chunkLength == 0 && expectedChunkSize == 0) { // Allow 0-length for 0-expected
				chunkErr = fmt.Errorf(
					"invalid chunk data length %d (expected %d, max %d) from peer %s",
					chunkLength, expectedChunkSize, fileMeta.ChunkSize, peerAddr,
				)
				results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
				dm.logger.Printf(
					"Worker for %s: peer %s session terminated for chunk %d due to: %v",
					fileMeta.Name, peerAddr, chunkIndex, chunkErr,
				)
				return
			}
		}

		if chunkLength > 0 {
			downloadedData = make([]byte, chunkLength)
			n, readFullErr := io.ReadFull(reader, downloadedData)
			if readFullErr != nil {
				chunkErr = fmt.Errorf("conn error reading chunk data (expected %d bytes): %w", chunkLength, readFullErr)
				results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
				dm.logger.Printf(
					"Worker for %s: peer %s session terminated for chunk %d due to: %v",
					fileMeta.Name, peerAddr, chunkIndex, chunkErr,
				)
				return
			}
			if int64(n) != chunkLength {
				chunkErr = fmt.Errorf("incomplete chunk data: read %d, expected %d from peer %s", n, chunkLength, peerAddr)
				results <- chunkDownloadResult{index: chunkIndex, peer: peerAddr, err: chunkErr}
				dm.logger.Printf(
					"Worker for %s: peer %s session terminated for chunk %d due to: %v",
					fileMeta.Name, peerAddr, chunkIndex, chunkErr,
				)
				return
			}
		} else {
			downloadedData = []byte{}
		}

		results <- chunkDownloadResult{index: chunkIndex, data: downloadedData, peer: peerAddr, err: nil}
	}
}
