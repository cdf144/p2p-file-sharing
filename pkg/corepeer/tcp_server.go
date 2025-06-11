package corepeer

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
)

type TCPServer struct {
	logger            *log.Logger
	listener          net.Listener
	serveCtx          context.Context
	serveCancel       context.CancelFunc
	connectionHandler *ConnectionHandler
	fileManager       *FileManager
}

func NewTCPServer(logger *log.Logger, connectionHandler *ConnectionHandler, fileManager *FileManager) *TCPServer {
	return &TCPServer{
		logger:            logger,
		connectionHandler: connectionHandler,
		fileManager:       fileManager,
	}
}

func (ts *TCPServer) IsRunning() bool {
	return ts.listener != nil
}

// Start initializes the TCP server, binds it to the specified port, and starts accepting connections.
// The server will continue to run until the context is cancelled or an error occurs during listening.
// If a listener already exists, it will not start a new one and returns with no error.
func (ts *TCPServer) Start(ctx context.Context, port int) error {
	if ts.listener != nil {
		ts.logger.Println("TCP server already running or listener already exists.")
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return fmt.Errorf("cannot start TCP server, context is not active or nil")
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}
	ts.logger.Printf("TCP server started on port: %d", port)

	ts.listener = l
	ts.serveCtx, ts.serveCancel = context.WithCancel(ctx)
	go ts.acceptConnections(ts.serveCtx)

	return nil
}

// Stop gracefully stops the TCP server, closing the listener and cancelling the context.
// If the listener is already closed, it will log a warning but not return an error.
// After stopping, the listener will be set to nil.
func (ts *TCPServer) Stop() {
	if ts.serveCancel != nil {
		ts.serveCancel()
	}
	if ts.listener != nil {
		err := ts.listener.Close()
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			ts.logger.Printf("Warning: Error closing listener: %v", err)
		}
		ts.listener = nil
		ts.logger.Println("TCP file server stopped.")
	}
}

// UpdateState manages the TCP server lifecycle based on the desired and current state.
// It starts the TCP server if it should be running but isn't currently running or if the listener is nil.
// It stops the TCP server if it shouldn't be running but is currently running.
// Returns an error if starting the TCP server fails.
func (ts *TCPServer) UpdateState(ctx context.Context, shouldBeRunning, wasRunning bool, port int) error {
	if shouldBeRunning && (!wasRunning || ts.listener == nil) {
		ts.logger.Printf("Starting TCP server for sharing directory: %s", ts.fileManager.GetShareDir())
		if ctx == nil {
			ts.logger.Println("Error: peerRootCtx is nil, cannot start TCP server. Peer might not have been started correctly.")
			return fmt.Errorf("peerRootCtx is nil, cannot start TCP server")
		}
		return ts.Start(ctx, port)
	} else if !shouldBeRunning && wasRunning {
		ts.logger.Println("Stopping TCP server - no directory to share.")
		ts.Stop()
	}
	return nil
}

func (ts *TCPServer) acceptConnections(ctx context.Context) {
	if ts.listener == nil {
		ts.logger.Println("Error: acceptConnections called with a nil listener.")
		return
	}
	ts.logger.Printf("Accepting connections on %s", ts.listener.Addr().String())

	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				ts.logger.Println("Context cancelled, server accept loop stopping gracefully.")
				return
			default:
				if strings.Contains(err.Error(), "use of closed network connection") {
					ts.logger.Println("Listener closed, server accept loop stopping.")
				} else {
					ts.logger.Printf("Error accepting connection: %v. Server accept loop stopping.", err)
				}
				return
			}
		}
		ts.logger.Printf("Accepted connection from %s", conn.RemoteAddr().String())
		go ts.connectionHandler.HandleConnection(conn)
	}
}
