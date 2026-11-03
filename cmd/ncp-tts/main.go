// Command ncp-tts is a Model Context Protocol server that turns text into
// speech using Microsoft Edge's neural voices.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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

	server := mcp.NewServer("ncp-tts", version, logger)
	mcp.RegisterTools(server, &mcp.TTSHandler{Client: client, OutDir: *outDir})

	logger.Printf("serving MCP over stdio (version %s)", version)
	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Fatalf("serve: %v", err)
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
