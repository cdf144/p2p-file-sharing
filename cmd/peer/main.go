package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cdf144/p2p-file-sharing/pkg/corepeer"
)

var (
	indexURLFlag   string
	shareDirFlag   string
	servePortFlag  int
	publicPortFlag int
)

var (
	checksumFlag string
	outputFlag   string
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	flag.StringVar(&indexURLFlag, "indexURL", "http://localhost:9090", "Index server URL")
	flag.StringVar(&shareDirFlag, "shareDir", "", "Directory to share files from (required for 'start')")
	flag.IntVar(&servePortFlag, "servePort", 0, "Port to serve files on (0 for random)")
	flag.IntVar(&publicPortFlag, "publicPort", 0, "Public port to announce (0 to use servePort)")

	flag.StringVar(&checksumFlag, "checksum", "", "Checksum of the file to download (for 'download' command)")
	flag.StringVar(&outputFlag, "output", "", "Path to save the downloaded file (for 'download' command, default: current dir with original filename)")

	flag.Usage = printUsageAndExit
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "Error: Command not specified.")
		printUsageAndExit()
	}

	command := flag.Arg(0)

	cfg := corepeer.CorePeerConfig{
		IndexURL:   indexURLFlag,
		ShareDir:   shareDirFlag,
		ServePort:  servePortFlag,
		PublicPort: publicPortFlag,
	}

	corePeer := corepeer.NewCorePeer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch command {
	case "start":
		if cfg.ShareDir == "" {
			log.Fatal("Error: -shareDir is required for the 'start' command.")
		}

		log.Println("Starting peer...")
		statusMsg, err := corePeer.Start(ctx)
		if err != nil {
			log.Fatalf("Error starting peer: %v", err)
		}
		log.Println(statusMsg)
		log.Println("Peer is running. Press Ctrl+C to stop.")

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		log.Println("Shutting down peer...")
		corePeer.Stop()
		log.Println("Peer stopped.")

	case "query-files":
		if cfg.IndexURL == "" {
			log.Fatal("Error: -indexURL is required for the 'query-files' command.")
		}

		log.Println("Querying network files from index server:", cfg.IndexURL)
		files, err := corePeer.FetchFilesFromIndex(ctx)
		if err != nil {
			log.Fatalf("Error fetching files from index: %v", err)
		}

		if len(files) == 0 {
			log.Println("No files found on the network.")
			return
		}
		log.Printf("Found %d files:\n", len(files))
		for i, f := range files {
			fmt.Printf("%d. Name: %s\n   Size: %d bytes\n   Checksum: %s\n   Chunks: %d (Size: %d)\n\n",
				i+1, f.Name, f.Size, f.Checksum, f.NumChunks, f.ChunkSize)
		}

	case "download":
		if cfg.IndexURL == "" {
			log.Fatal("Error: -indexURL is required for the 'download' command.")
		}
		if checksumFlag == "" {
			log.Fatal("Error: -checksum is required for the 'download' command.")
		}

		log.Printf("Fetching metadata for file with checksum: %s\n", checksumFlag)
		fileMeta, err := corePeer.FetchFileFromIndex(ctx, checksumFlag)
		if err != nil {
			log.Fatalf("Error fetching file metadata: %v", err)
		}
		if fileMeta.Name == "" {
			log.Fatalf("Error: No file metadata found for checksum %s on the index server.", checksumFlag)
		}

		savePath := outputFlag
		if savePath == "" {
			savePath = fileMeta.Name
		}

		absSavePath, err := filepath.Abs(savePath)
		if err != nil {
			log.Fatalf("Error resolving absolute path for output %s: %v", savePath, err)
		}

		info, statErr := os.Stat(absSavePath)
		if statErr == nil && info.IsDir() {
			absSavePath = filepath.Join(absSavePath, fileMeta.Name)
		}

		saveDir := filepath.Dir(absSavePath)
		if _, err := os.Stat(saveDir); os.IsNotExist(err) {
			log.Printf("Output directory %s does not exist, attempting to create it.", saveDir)
			if err := os.MkdirAll(saveDir, 0o755); err != nil {
				log.Fatalf("Error creating output directory %s: %v", saveDir, err)
			}
			log.Printf("Created output directory: %s", saveDir)
		}

		log.Printf("Attempting to download file: %s (Size: %d bytes)\n", fileMeta.Name, fileMeta.Size)
		log.Printf("Saving to: %s\n", absSavePath)

		err = corePeer.DownloadFile(ctx, fileMeta, absSavePath, nil)
		if err != nil {
			log.Fatalf("Error downloading file: %v", err)
		}
		log.Printf("Successfully downloaded %s to %s\n", fileMeta.Name, absSavePath)

	default:
		fmt.Fprintf(os.Stderr, "Error: Unknown command: %s\n", command)
		printUsageAndExit()
	}
}

func printUsageAndExit() {
	fmt.Fprintf(os.Stderr, "Usage: %s [options] <command>\n\n", filepath.Base(os.Args[0]))
	fmt.Fprintln(os.Stderr, "Description:")
	fmt.Fprintln(os.Stderr, "  A CLI tool to interact with the P2P file sharing network.")
	fmt.Fprintln(os.Stderr, "\nOptions (global for all commands):")
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, "\nCommands:")
	fmt.Fprintln(os.Stderr, "  start          Start the peer to share files and listen for connections.")
	fmt.Fprintln(os.Stderr, "                 Requires -shareDir. Runs until interrupted (Ctrl+C).")
	fmt.Fprintln(os.Stderr, "  query-files    Query the index server for all available files.")
	fmt.Fprintln(os.Stderr, "                 Requires -indexURL.")
	fmt.Fprintln(os.Stderr, "  download       Download a file from the network.")
	fmt.Fprintln(os.Stderr, "                 Requires -indexURL and -checksum.")
	fmt.Fprintln(os.Stderr, "                 Use -output to specify save location (file path or directory).")
	fmt.Fprintln(os.Stderr, "\nExamples:")
	fmt.Fprintf(os.Stderr, "  %s -shareDir /my/shared/files -servePort 8001 start\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, "  %s -indexURL http://localhost:9090 query-files\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(
		os.Stderr,
		"  %s -indexURL http://localhost:9090 -checksum <sha256_hex_string> -output ./downloaded_file.dat download\n",
		filepath.Base(os.Args[0]),
	)
	fmt.Fprintf(
		os.Stderr,
		"  %s -indexURL http://localhost:9090 -checksum <sha256_hex_string> -output /path/to/downloads/ download\n",
		filepath.Base(os.Args[0]),
	)
	os.Exit(1)
}
