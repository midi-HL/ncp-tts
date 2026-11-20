package tts

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type voiceRoundTripper func(*http.Request) (*http.Response, error)

func (f voiceRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestListVoicesStopsAfterOneForbiddenRetry(t *testing.T) {
	var calls int
	client := &Client{HTTPClient: &http.Client{Transport: voiceRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls > 2 {
			t.Fatal("voice list attempted a third request")
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}}
	if _, err := client.ListVoices(context.Background()); err == nil || calls != 2 {
		t.Fatalf("ListVoices got error %v after %d requests; want error after two requests", err, calls)
	}
}

func TestSynthesizeCancellationInterruptsWebSocketRead(t *testing.T) {
	received := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for range 2 {
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Error(err)
				return
			}
		}
		close(received)
		// Hold the connection open without sending audio until the client closes it.
		conn.ReadMessage()
	}))
	defer server.Close()
	client := NewClient()
	client.Dialer.NetDialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialer := tls.Dialer{Config: server.Client().Transport.(*http.Transport).TLSClientConfig}
		return dialer.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, errs, err := client.Synthesize(ctx, "Test text", TTSConfig{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case err := <-errs:
		t.Fatalf("synthesis failed before cancellation: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the speech request")
	}
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v; want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled synthesis did not close the WebSocket")
	}
}
