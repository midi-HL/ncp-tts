// Command ncp-tts serves Microsoft Edge neural voices over MCP or HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/midi-HL/ncp-tts/internal/api"
	"github.com/midi-HL/ncp-tts/internal/mcp"
	"github.com/midi-HL/ncp-tts/internal/tts"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var (
		outDir      = flag.String("out", "", "directory for generated MP3 files (default: system temp)")
		showVersion = flag.Bool("version", false, "print version and exit")
		selfTest    = flag.Bool("selftest", false, "synthesize a short sample and exit")
		listenAddr  = flag.String("listen", "", "serve the HTTP API on host:port instead of MCP (e.g. 0.0.0.0:8000)")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// stdout is reserved for JSON-RPC framing; every diagnostic goes to stderr.
	logger := log.New(os.Stderr, "ncp-tts: ", log.LstdFlags|log.Lmsgprefix)

	client := tts.NewClient()

	if *selfTest {
		if err := runSelfTest(client); err != nil {
			logger.Fatalf("selftest failed: %v", err)
		}
		fmt.Println("selftest ok")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *listenAddr != "" {
		if err := serveHTTP(ctx, *listenAddr, client, logger); err != nil {
			logger.Fatalf("serve HTTP: %v", err)
		}
		return
	}

	server := mcp.NewServer("ncp-tts", version, logger)
	mcp.RegisterTools(server, &mcp.TTSHandler{Client: client, OutDir: *outDir})

	logger.Printf("serving MCP over stdio (version %s)", version)
	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Fatalf("serve: %v", err)
	}
}

func serveHTTP(ctx context.Context, addr string, client *tts.Client, logger *log.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           api.NewHandler(client, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      api.RequestTimeout + 30*time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	defer srv.Close()

	stopped := make(chan error, 1)
	go func() {
		logger.Printf("serving HTTP API on %s (version %s)", listener.Addr(), version)
		stopped <- srv.Serve(listener)
	}()
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// runSelfTest verifies the network path end to end without involving a client.
func runSelfTest(client *tts.Client) error {
	ctx := context.Background()
	voices, err := client.ListVoices(ctx)
	if err != nil {
		return fmt.Errorf("list voices: %w", err)
	}
	if len(voices) == 0 {
		return fmt.Errorf("service returned no voices")
	}
	chunks, errs, err := client.Synthesize(ctx, "ncp-tts self test.", tts.TTSConfig{})
	if err != nil {
		return fmt.Errorf("synthesize: %w", err)
	}
	var audio int
	for chunk := range chunks {
		audio += len(chunk.Audio)
	}
	for streamErr := range errs {
		if streamErr != nil {
			return fmt.Errorf("stream: %w", streamErr)
		}
	}
	if audio == 0 {
		return fmt.Errorf("no audio produced")
	}
	fmt.Printf("voices=%d audio_bytes=%d\n", len(voices), audio)
	return nil
}
