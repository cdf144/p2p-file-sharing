package corepeer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

type TCPServer struct {
	logger            *log.Logger
	listener          net.Listener
	serveCtx          context.Context
	serveCancel       context.CancelFunc
	tlsConfig         *tls.Config
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
// If enableTLS is true, certFile and keyFile must be valid paths to PEM-encoded files.
// The server will continue to run until the context is cancelled or an error occurs during listening.
// If a listener already exists, it will not start a new one and returns with no error.
func (ts *TCPServer) Start(ctx context.Context, port int, enableTLS bool, certFile, keyFile string) error {
	if ts.listener != nil {
		ts.logger.Println("TCP server already running or listener already exists.")
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return fmt.Errorf("cannot start TCP server, context is not active or nil")
	}

	var l net.Listener
	var err error
	listenAddr := fmt.Sprintf(":%d", port)

	if enableTLS {
		if certFile == "" || keyFile == "" {
			return fmt.Errorf("TLS enabled but certFile or keyFile not provided")
		}
		cert, errLoad := tls.LoadX509KeyPair(certFile, keyFile)
		if errLoad != nil {
			return fmt.Errorf("failed to load TLS key pair: %w", errLoad)
		}
		ts.tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		// Plain TCP listener, TLS will be handled per connection by upgradeToTLSIfNeeded
		l, err = net.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", port, err)
		}
		ts.logger.Printf("TCP server started with TLS support on port: %d", port)
	} else {
		l, err = net.Listen("tcp", listenAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", port, err)
		}
		ts.logger.Printf("TCP server started (no TLS) on port: %d", port)
	}

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
func (ts *TCPServer) UpdateState(
	ctx context.Context, shouldBeRunning, wasRunning bool, port int, tls bool, certFile, keyFile string,
) error {
	if shouldBeRunning && (!wasRunning || ts.listener == nil) {
		ts.logger.Printf("Starting TCP server for sharing directory: %s", ts.fileManager.GetShareDir())
		if ctx == nil {
			ts.logger.Println("Error: peerRootCtx is nil, cannot start TCP server. Peer might not have been started correctly.")
			return fmt.Errorf("peerRootCtx is nil, cannot start TCP server")
		}
		return ts.Start(ctx, port, tls, certFile, keyFile)
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

		if ts.tlsConfig != nil {
			conn = ts.upgradeToTLSIfNeeded(conn)
		}

		ts.logger.Printf("Accepted connection from %s", conn.RemoteAddr().String())
		go ts.connectionHandler.HandleConnection(conn)
	}
}

func (ts *TCPServer) upgradeToTLSIfNeeded(conn net.Conn) net.Conn {
	tlsConn := tls.Server(conn, ts.tlsConfig)
	tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if err := tlsConn.Handshake(); err != nil {
		ts.logger.Printf("TLS handshake failed with %s, falling back to plain TCP: %v",
			conn.RemoteAddr(), err)
		return conn
	}

	tlsConn.SetReadDeadline(time.Time{})
	return tlsConn
}
