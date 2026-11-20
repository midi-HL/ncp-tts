// Package api exposes the Edge TTS backend through an OpenAI-compatible HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/midi-HL/ncp-tts/internal/tts"
)

// RequestTimeout bounds synthesis and conversion together.
const RequestTimeout = 3 * time.Minute

type backend interface {
	ListVoices(context.Context) ([]tts.Voice, error)
	Synthesize(context.Context, string, tts.TTSConfig) (<-chan tts.Chunk, <-chan error, error)
}

type server struct {
	backend backend
	logger  *log.Logger
}

// NewHandler returns the HTTP API. It does not inspect Authorization headers.
func NewHandler(client backend, logger *log.Logger) http.Handler {
	s := &server{backend: client, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", only("GET", s.listModels))
	mux.HandleFunc("/v1/models/{id}", only("GET", s.retrieveModel))
	mux.HandleFunc("/v1/voices", only("GET", s.listVoices))
	mux.HandleFunc("/v1/audio/speech", only("POST", s.speech))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Endpoint not found.", "", "not_found")
	})
	return mux
}

func only(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed.", "", "method_not_allowed")
			return
		}
		handler(w, r)
	}
}

type model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

var models = []model{
	{ID: "edge-tts", Object: "model", OwnedBy: "ncp-tts"},
	{ID: "tts-1", Object: "model", OwnedBy: "ncp-tts"},
}

func findModel(id string) (model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return model{}, false
}

type listResponse[T any] struct {
	Object string `json:"object"`
	Data   []T    `json:"data"`
}

func (s *server) listModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listResponse[model]{Object: "list", Data: models})
}

func (s *server) retrieveModel(w http.ResponseWriter, r *http.Request) {
	m, ok := findModel(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown model. See GET /v1/models.", "model", "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type voice struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Locale string `json:"locale"`
	Gender string `json:"gender"`
	Name   string `json:"name"`
}

func (s *server) listVoices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	voices, err := s.backend.ListVoices(ctx)
	if err != nil {
		s.upstreamError(w, ctx, "list voices", err)
		return
	}
	locale := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("locale")))
	data := make([]voice, 0, len(voices))
	for _, v := range voices {
		if v.ShortName != "" && strings.HasPrefix(strings.ToLower(v.Locale), locale) {
			data = append(data, voice{ID: v.ShortName, Object: "voice", Locale: v.Locale, Gender: v.Gender, Name: v.FriendlyName})
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	writeJSON(w, http.StatusOK, listResponse[voice]{Object: "list", Data: data})
}

type speechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	Speed          *float64 `json:"speed"`
	ResponseFormat string   `json:"response_format"`
	StreamFormat   string   `json:"stream_format"`
	Instructions   string   `json:"instructions"`
}

var voiceName = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)+$`)

var voiceAliases = map[string]string{
	"alloy":   "en-US-AvaNeural",
	"echo":    "en-US-GuyNeural",
	"fable":   "en-GB-RyanNeural",
	"onyx":    "en-US-ChristopherNeural",
	"nova":    "en-US-JennyNeural",
	"shimmer": "en-US-AriaNeural",
}

func (s *server) speech(w http.ResponseWriter, r *http.Request) {
	var req speechRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	err := decoder.Decode(&req)
	if err == nil && decoder.Decode(new(json.RawMessage)) != io.EOF {
		err = errors.New("unexpected data after speech request")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "Expected a JSON speech request object.", "", "invalid_json")
		return
	}
	if req.Model == "" {
		invalidParam(w, "model", "Provide a model ID from GET /v1/models.")
		return
	}
	if _, ok := findModel(req.Model); !ok {
		writeError(w, http.StatusNotFound, "Unknown model. See GET /v1/models.", "model", "model_not_found")
		return
	}
	if strings.TrimSpace(req.Input) == "" || utf8.RuneCountInString(req.Input) > 4096 {
		invalidParam(w, "input", "Input must contain 1 to 4096 characters of text.")
		return
	}
	if req.Voice == "" {
		req.Voice = tts.DefaultVoice
	} else if alias, ok := voiceAliases[req.Voice]; ok {
		req.Voice = alias
	} else if len(req.Voice) > 128 || !voiceName.MatchString(req.Voice) {
		invalidParam(w, "voice", "Use a voice ID from GET /v1/voices or a supported OpenAI voice alias.")
		return
	}
	rate := "+0%"
	if req.Speed != nil {
		if *req.Speed < 0.25 || *req.Speed > 4 {
			invalidParam(w, "speed", "Speed must be between 0.25 and 4.0.")
			return
		}
		rate = fmt.Sprintf("%+d%%", int(math.Round((*req.Speed-1)*100)))
	}
	if req.StreamFormat != "" && req.StreamFormat != "audio" {
		invalidParam(w, "stream_format", "The supported stream format is audio.")
		return
	}
	if req.Instructions != "" {
		invalidParam(w, "instructions", "The Edge TTS model does not support speech instructions.")
		return
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "mp3"
	}
	format, ok := audioFormats[req.ResponseFormat]
	if !ok {
		invalidParam(w, "response_format", "Choose mp3, opus, aac, flac, wav, or pcm.")
		return
	}
	ffmpeg, err := format.converter()
	if err != nil {
		writeError(w, http.StatusNotImplemented, "This format requires FFmpeg on the server PATH. MP3 is available directly.", "response_format", "format_unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()
	audio, err := s.synthesize(ctx, req.Input, tts.TTSConfig{Voice: req.Voice, Rate: rate})
	if err != nil {
		s.upstreamError(w, ctx, "synthesize speech", err)
		return
	}
	if ffmpeg != "" {
		audio, err = format.convert(ctx, ffmpeg, audio)
		if err != nil {
			s.logError("convert audio", err)
			if ctx.Err() != nil {
				writeError(w, http.StatusGatewayTimeout, "The audio conversion timed out.", "", "timeout")
			} else {
				writeError(w, http.StatusInternalServerError, "The server could not convert the audio.", "response_format", "conversion_error")
			}
			return
		}
	}
	w.Header().Set("Content-Type", format.mime)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(audio)
}

func (s *server) synthesize(ctx context.Context, input string, cfg tts.TTSConfig) ([]byte, error) {
	chunks, errs, err := s.backend.Synthesize(ctx, input, cfg)
	if err != nil {
		return nil, err
	}
	var audio []byte
	for chunks != nil || errs != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			audio = append(audio, chunk.Audio...)
		case err, ok := <-errs:
			if !ok {
				errs = nil
			} else if err != nil {
				return nil, err
			}
		}
	}
	if len(audio) == 0 {
		return nil, errors.New("upstream returned no audio")
	}
	return audio, nil
}

func (s *server) upstreamError(w http.ResponseWriter, ctx context.Context, action string, err error) {
	s.logError(action, err)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "The speech service timed out.", "", "timeout")
		return
	}
	writeError(w, http.StatusBadGateway, "The speech service could not complete the request.", "", "upstream_error")
}

func (s *server) logError(action string, err error) {
	if s.logger != nil {
		s.logger.Printf("%s: %v", action, err)
	}
}

func invalidParam(w http.ResponseWriter, param, message string) {
	writeError(w, http.StatusBadRequest, message, param, "invalid_value")
}

type errorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func writeError(w http.ResponseWriter, status int, message, param, code string) {
	detail := errorDetail{Message: message, Type: "invalid_request_error", Code: code}
	if status >= 500 {
		detail.Type = "server_error"
	}
	if param != "" {
		detail.Param = &param
	}
	writeJSON(w, status, struct {
		Error errorDetail `json:"error"`
	}{Error: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
