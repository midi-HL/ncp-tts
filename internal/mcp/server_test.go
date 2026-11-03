package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, handler Handler) *Server {
	t.Helper()
	srv := NewServer("test", "1", log.New(io.Discard, "", 0))
	srv.AddTool(Tool{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}, handler)
	return srv
}

func run(t *testing.T, srv *Server, lines ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response is not JSON: %q", line)
		}
		resp = append(resp, m)
	}
	return resp
}

func TestInitialize(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(resp) != 1 {
		t.Fatalf("got %d responses, want 1", len(resp))
	}
	result := resp[0]["result"].(map[string]any)
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "test" || info["version"] != "1" {
		t.Fatalf("serverInfo = %v", info)
	}
	if result["protocolVersion"] == "" {
		t.Fatal("protocolVersion missing")
	}
}

// TestNotificationGetsNoReply is a protocol requirement: answering a
// notification corrupts the client's stream.
func TestNotificationGetsNoReply(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(resp) != 0 {
		t.Fatalf("notification produced %d responses, want 0", len(resp))
	}
}

func TestToolsList(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := resp[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("unexpected tool: %v", tools[0])
	}
}

func TestToolCall(t *testing.T) {
	srv := newTestServer(t, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return &CallToolResult{Content: []Content{{Type: "text", Text: "hello " + args["name"].(string)}}}, nil
	})
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"name":"world"}}}`)
	content := resp[0]["result"].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["text"] != "hello world" {
		t.Fatalf("unexpected content: %v", content)
	}
}

// TestToolErrorIsInBand checks that handler failures surface as isError results
// rather than JSON-RPC errors, so the model can read and react to them.
func TestToolErrorIsInBand(t *testing.T) {
	srv := newTestServer(t, func(ctx context.Context, args map[string]any) (*CallToolResult, error) {
		return nil, io.ErrUnexpectedEOF
	})
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	if _, hasErr := resp[0]["error"]; hasErr {
		t.Fatalf("tool failure became a protocol error: %v", resp[0])
	}
	result := resp[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError not set: %v", result)
	}
}

func TestUnknownMethod(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	errObj := resp[0]["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("code = %v, want %d", errObj["code"], CodeMethodNotFound)
	}
}

func TestInvalidJSON(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{not json}`)
	errObj := resp[0]["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeParseError {
		t.Fatalf("code = %v, want %d", errObj["code"], CodeParseError)
	}
}

func TestUnknownTool(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing","arguments":{}}}`)
	errObj := resp[0]["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeInvalidParams {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestBlankLinesIgnored(t *testing.T) {
	srv := newTestServer(t, nil)
	resp := run(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if len(resp) != 1 {
		t.Fatalf("got %d responses, want 1", len(resp))
	}
}
