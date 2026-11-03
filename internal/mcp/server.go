package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
)

// Handler executes one tool call.
type Handler func(ctx context.Context, args map[string]any) (*CallToolResult, error)

// Server dispatches JSON-RPC requests to registered tools.
type Server struct {
	name     string
	version  string
	tools    []Tool
	handlers map[string]Handler
	mu       sync.RWMutex
	logger   *log.Logger
}

// NewServer creates a server that will identify itself with the given name and
// version during the handshake.
func NewServer(name, version string, logger *log.Logger) *Server {
	return &Server{
		name:     name,
		version:  version,
		handlers: make(map[string]Handler),
		logger:   logger,
	}
}

// AddTool registers a tool and its handler.
func (s *Server) AddTool(tool Tool, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, tool)
	s.handlers[tool.Name] = handler
}

// Serve reads newline-delimited JSON-RPC from in and writes replies to out
// until the input closes or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// Tool arguments can carry long text; allow large frames.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	writer := &lockedWriter{w: out}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeResponse(writer, Response{
				JSONRPC: "2.0",
				Error:   &Error{Code: CodeParseError, Message: "invalid JSON: " + err.Error()},
			})
			continue
		}
		if err := s.handle(ctx, writer, req); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}

func (s *Server) handle(ctx context.Context, w io.Writer, req Request) error {
	// Notifications have no id and must never be answered.
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		result := InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    Capabilities{Tools: &ToolsCapability{}},
			ServerInfo:      ServerInfo{Name: s.name, Version: s.version},
			Instructions: "Text-to-speech via Microsoft Edge neural voices. " +
				"Call tts_list_voices to discover voice names, then tts_speak to synthesize.",
		}
		if !isNotification {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: result})
		}
	case "notifications/initialized":
		return nil
	case "ping":
		if !isNotification {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		}
	case "tools/list":
		s.mu.RLock()
		tools := make([]Tool, len(s.tools))
		copy(tools, s.tools)
		s.mu.RUnlock()
		if !isNotification {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: ListToolsResult{Tools: tools}})
		}
	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID,
				Error: &Error{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}})
			return nil
		}
		s.mu.RLock()
		handler, ok := s.handlers[params.Name]
		s.mu.RUnlock()
		if !ok {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID,
				Error: &Error{Code: CodeInvalidParams, Message: "unknown tool: " + params.Name}})
			return nil
		}
		result, err := handler(ctx, params.Arguments)
		if err != nil {
			// Tool failures are reported in-band so the model can react to
			// them, rather than as protocol errors.
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: &CallToolResult{
				Content: []Content{{Type: "text", Text: "error: " + err.Error()}},
				IsError: true,
			}})
			return nil
		}
		if !isNotification {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: result})
		}
	default:
		if !isNotification {
			s.writeResponse(w, Response{JSONRPC: "2.0", ID: req.ID,
				Error: &Error{Code: CodeMethodNotFound, Message: "method not found: " + req.Method}})
		}
	}
	return nil
}

func (s *Server) writeResponse(w io.Writer, resp Response) {
	resp.JSONRPC = "2.0"
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Printf("marshal response: %v", err)
		return
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		s.logger.Printf("write response: %v", err)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
