package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/ncp-tts/internal/tts"
)

type runningServices struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	url    string
	client *http.Client
}

func startServices(t *testing.T, in io.Reader, out io.Writer) *runningServices {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &runningServices{
		cancel: cancel,
		done:   make(chan struct{}),
		url:    "http://" + listener.Addr().String(),
		client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{}},
	}
	go func() {
		f.err = serveServices(ctx, listener, tts.NewClient(), "", in, out, log.New(io.Discard, "", 0))
		close(f.done)
	}()
	t.Cleanup(func() {
		cancel()
		if closer, ok := in.(io.Closer); ok {
			closer.Close()
		}
		if closer, ok := out.(io.Closer); ok {
			closer.Close()
		}
		select {
		case <-f.done:
		case <-time.After(3 * time.Second):
			t.Error("services did not stop during cleanup")
			listener.Close()
		}
		f.client.CloseIdleConnections()
	})
	return f
}

func (f *runningServices) checkAPI(t *testing.T) {
	t.Helper()
	resp, err := f.client.Get(f.url + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Object string
		Data   []struct{ ID string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 || body.Object != "list" || len(body.Data) != 2 {
		t.Fatalf("model endpoint: status=%d body=%+v error=%v", resp.StatusCode, body, err)
	}
}

func (f *runningServices) waitForStop(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
		if f.err != nil {
			t.Fatalf("services exited with error: %v", f.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("services did not stop promptly")
	}
}

func TestStandaloneAPISurvivesUnusedStdinEOF(t *testing.T) {
	f := startServices(t, strings.NewReader(""), io.Discard)
	f.checkAPI(t)
	select {
	case <-f.done:
		t.Fatalf("standalone API stopped at stdin EOF: %v", f.err)
	case <-time.After(100 * time.Millisecond):
	}
	f.checkAPI(t)
	f.cancel()
	f.waitForStop(t)
}

func TestMCPAndAPIServeTogetherAndStopAtSessionEOF(t *testing.T) {
	stdin, input := io.Pipe()
	output, stdout := io.Pipe()
	defer input.Close()
	defer output.Close()
	f := startServices(t, stdin, stdout)
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n")
		written <- err
	}()
	response := make(chan error, 1)
	go func() {
		var body struct {
			JSONRPC string
			ID      int
			Result  struct{ ProtocolVersion string }
		}
		err := json.NewDecoder(output).Decode(&body)
		if err == nil && (body.JSONRPC != "2.0" || body.ID != 1 || body.Result.ProtocolVersion == "") {
			err = io.ErrUnexpectedEOF
		}
		response <- err
	}()
	select {
	case err := <-response:
		if err != nil {
			t.Fatalf("MCP handshake response: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP handshake did not return")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	f.checkAPI(t)
	input.Close()
	f.waitForStop(t)
}

func TestCancellationStopsServicesWithIdleMCPStdin(t *testing.T) {
	stdin, input := io.Pipe()
	defer input.Close()
	f := startServices(t, stdin, io.Discard)
	f.checkAPI(t)
	f.cancel()
	f.waitForStop(t)
}
