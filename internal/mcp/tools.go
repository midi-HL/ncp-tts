package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midi-HL/ncp-tts/internal/tts"
)

// TTSHandler wires the tool definitions to the synthesis client.
type TTSHandler struct {
	Client *tts.Client
	// OutDir is where audio is written when the caller asks for a file.
	OutDir string
}

// RegisterTools installs every tool this server exposes.
func RegisterTools(srv *Server, h *TTSHandler) {
	srv.AddTool(Tool{
		Name: "tts_speak",
		Description: "Synthesize speech from text using Microsoft Edge neural voices. " +
			"Returns the audio as a file path by default, or inline base64 when requested.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "The text to speak.",
				},
				"voice": map[string]any{
					"type":        "string",
					"description": "Voice short name, e.g. zh-CN-XiaoxiaoNeural. Defaults to en-US-EmmaMultilingualNeural.",
				},
				"rate": map[string]any{
					"type":        "string",
					"description": "Speaking rate delta as a percentage, e.g. +10% or -20%.",
				},
				"volume": map[string]any{
					"type":        "string",
					"description": "Volume delta as a percentage, e.g. +0% or -30%.",
				},
				"pitch": map[string]any{
					"type":        "string",
					"description": "Pitch delta in hertz, e.g. +0Hz or -20Hz.",
				},
				"output": map[string]any{
					"type":        "string",
					"description": "Absolute path for the MP3 file. Defaults to a generated name inside the configured output directory.",
				},
				"inline": map[string]any{
					"type":        "boolean",
					"description": "Return the audio as base64 in the response instead of writing a file.",
				},
			},
			"required": []string{"text"},
		},
	}, h.speak)

	srv.AddTool(Tool{
		Name: "tts_list_voices",
		Description: "List available neural voices, optionally filtered by locale prefix " +
			"(e.g. zh-CN, en-US) or a substring of the voice name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"locale": map[string]any{
					"type":        "string",
					"description": "Locale prefix filter such as zh-CN.",
				},
				"contains": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring match on the short name.",
				},
			},
		},
	}, h.listVoices)
}

func (h *TTSHandler) speak(ctx context.Context, args map[string]any) (*CallToolResult, error) {
	text, _ := args["text"].(string)
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	cfg := tts.TTSConfig{
		Voice:  stringArg(args, "voice"),
		Rate:   stringArg(args, "rate"),
		Volume: stringArg(args, "volume"),
		Pitch:  stringArg(args, "pitch"),
	}
	for name, v := range map[string]string{"rate": cfg.Rate, "volume": cfg.Volume} {
		if err := tts.ParsePercent(v); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}

	chunks, errs, err := h.Client.Synthesize(ctx, text, cfg)
	if err != nil {
		return nil, err
	}
	var audio []byte
	for chunk := range chunks {
		audio = append(audio, chunk.Audio...)
	}
	for streamErr := range errs {
		if streamErr != nil {
			return nil, streamErr
		}
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("service returned no audio")
	}

	inline, _ := args["inline"].(bool)
	voice := cfg.Voice
	if voice == "" {
		voice = tts.DefaultVoice
	}

	if inline {
		return &CallToolResult{Content: []Content{
			{Type: "text", Text: fmt.Sprintf("voice=%s bytes=%d", voice, len(audio))},
			{Type: "audio", Data: base64.StdEncoding.EncodeToString(audio), MimeType: "audio/mpeg"},
		}}, nil
	}

	path, err := h.outputPath(stringArg(args, "output"), voice)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		return nil, fmt.Errorf("write audio: %w", err)
	}
	return &CallToolResult{Content: []Content{
		{Type: "text", Text: fmt.Sprintf("wrote %d bytes to %s (voice=%s)", len(audio), path, voice)},
	}}, nil
}

// outputPath resolves the destination file, guarding against path traversal in
// generated names.
func (h *TTSHandler) outputPath(requested, voice string) (string, error) {
	if requested != "" {
		abs, err := filepath.Abs(requested)
		if err != nil {
			return "", fmt.Errorf("resolve output path: %w", err)
		}
		return abs, nil
	}
	dir := h.OutDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("ncp-tts-%d.mp3", os.Getpid())
	return filepath.Join(dir, name), nil
}

func (h *TTSHandler) listVoices(ctx context.Context, args map[string]any) (*CallToolResult, error) {
	voices, err := h.Client.ListVoices(ctx)
	if err != nil {
		return nil, err
	}
	locale := stringArg(args, "locale")
	contains := strings.ToLower(stringArg(args, "contains"))

	var matched []tts.Voice
	for _, v := range voices {
		if locale != "" && !strings.HasPrefix(strings.ToLower(v.Locale), strings.ToLower(locale)) {
			continue
		}
		if contains != "" && !strings.Contains(strings.ToLower(v.ShortName), contains) {
			continue
		}
		matched = append(matched, v)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ShortName < matched[j].ShortName })

	var b strings.Builder
	fmt.Fprintf(&b, "%d voice(s) matched (of %d total)\n", len(matched), len(voices))
	for _, v := range matched {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", v.ShortName, v.Gender, v.Locale)
	}
	return &CallToolResult{Content: []Content{{Type: "text", Text: b.String()}}}, nil
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}
