// Command ncp-tts serves Microsoft Edge neural voices over MCP and HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

const defaultListenAddr = "0.0.0.0:8000"

func main() {
	var (
		outDir      = flag.String("out", "", "directory for generated MP3 files (default: system temp)")
		showVersion = flag.Bool("version", false, "print version and exit")
		selfTest    = flag.Bool("selftest", false, "synthesize a short sample and exit")
		listenAddr  = flag.String("listen", defaultListenAddr, "HTTP API host:port; use an empty value to disable HTTP")
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

	var listener net.Listener
	if *listenAddr != "" {
		var err error
		network := "tcp"
		host, _, _ := net.SplitHostPort(*listenAddr)
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			// Keep an explicit IPv4 address on IPv4, including on Windows.
			network = "tcp4"
		}
		listener, err = net.Listen(network, *listenAddr)
		if err != nil {
			logger.Fatalf("listen HTTP on %s: %v (choose another address with --listen)", *listenAddr, err)
		}
	}
	if err := serveServices(ctx, listener, client, *outDir, os.Stdin, os.Stdout, logger); err != nil {
		logger.Fatalf("serve: %v", err)
	}
}

// serveServices keeps a standalone API alive after an unused stdin closes.
// A connected MCP client's EOF stops both services to match its process lifetime.
func serveServices(ctx context.Context, listener net.Listener, client *tts.Client, outDir string, in io.Reader, out io.Writer, logger *log.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	var httpDone chan error
	if listener != nil {
		httpDone = make(chan error, 1)
		go func() { httpDone <- serveHTTP(ctx, listener, client, logger) }()
	}
	defer func() {
		cancel()
		if httpDone != nil {
			<-httpDone
		}
	}()

	server := mcp.NewServer("ncp-tts", version, logger)
	mcp.RegisterTools(server, &mcp.TTSHandler{Client: client, OutDir: outDir})
	type mcpResult struct {
		err  error
		used bool
	}
	mcpDone := make(chan mcpResult, 1)
	go func() {
		input := &observedReader{Reader: in}
		err := server.Serve(ctx, input, out)
		mcpDone <- mcpResult{err: err, used: input.used}
	}()

	logger.Printf("serving MCP over stdio (version %s)", version)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-httpDone:
			httpDone = nil // This result has already been collected.
			return err
		case result := <-mcpDone:
			if listener == nil || result.used || result.err != nil {
				return result.err
			}
			mcpDone = nil
			logger.Print("stdin closed without an MCP session; HTTP API remains available")
		}
	}
}

type observedReader struct {
	io.Reader
	used bool
}

func (r *observedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.used = r.used || n > 0
	return n, err
}

func serveHTTP(ctx context.Context, listener net.Listener, client *tts.Client, logger *log.Logger) error {
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
