// Package tts speaks to Microsoft Edge's Read Aloud endpoint.
package tts

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/midi-HL/ncp-tts/internal/drm"
)

// DefaultVoice is used when the caller does not pick one.
const DefaultVoice = "en-US-EmmaMultilingualNeural"

const (
	baseURL          = "speech.platform.bing.com/consumer/speech/synthesize/readaloud"
	wssURL           = "wss://" + baseURL + "/edge/v1"
	voiceListURL     = "https://" + baseURL + "/voices/list"
	secMSGECVersion  = "2-143.0.3650.75"
	chromiumMajor    = "143"
	outputFormatMime = "audio/mpeg"
	// mp3BitrateBPS is the constant bitrate of the requested output format,
	// used to convert received bytes into timeline ticks.
	mp3BitrateBPS  = 48000
	ticksPerSecond = 10000000
)

var browserHeaders = http.Header{
	"User-Agent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36" +
		" (KHTML, like Gecko) Chrome/" + chromiumMajor + ".0.0.0 Safari/537.36" +
		" Edg/" + chromiumMajor + ".0.0.0"},
	"Accept-Encoding": {"gzip, deflate, br, zstd"},
	"Accept-Language": {"en-US,en;q=0.9"},
}

func wsHeaders() http.Header {
	h := browserHeaders.Clone()
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
	h.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	return h
}

func voiceHeaders() http.Header {
	h := browserHeaders.Clone()
	h.Set("Authority", "speech.platform.bing.com")
	h.Set("Sec-CH-UA", `" Not;A Brand";v="99", "Microsoft Edge";v="`+chromiumMajor+`", "Chromium";v="`+chromiumMajor+`"`)
	h.Set("Sec-CH-UA-Mobile", "?0")
	h.Set("Accept", "*/*")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Dest", "empty")
	return h
}

// muid returns a random 32-character uppercase hex identifier.
func muid() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000000000000000000000000000"
	}
	return fmt.Sprintf("%X", buf)
}

func connectID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", 32)
	}
	return fmt.Sprintf("%x", buf)
}

func dateString() string {
	return time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
}

// Client issues synthesis requests.
type Client struct {
	// HTTPClient fetches the voice list; nil means a sensible default.
	HTTPClient *http.Client
	// Dialer connects the synthesis WebSocket; nil means a sensible default.
	Dialer *websocket.Dialer
	// ClockSkew is added to the local clock when minting tokens. The service
	// returns 403 when the local clock drifts; callers can correct it.
	ClockSkew time.Duration
}

// NewClient returns a Client with production defaults.
func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
			TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

// Voice describes one available synthesis voice.
type Voice struct {
	Name           string   `json:"Name"`
	ShortName      string   `json:"ShortName"`
	Gender         string   `json:"Gender"`
	Locale         string   `json:"Locale"`
	SuggestedCodec string   `json:"SuggestedCodec"`
	FriendlyName   string   `json:"FriendlyName"`
	Status         string   `json:"Status"`
	Categories     []string `json:"-"`
}

// ListVoices fetches every voice the service exposes.
func (c *Client) ListVoices(ctx context.Context) ([]Voice, error) {
	token := c.token()
	url := voiceListURL + "?trustedclienttoken=" + drm.TrustedClientToken +
		"&Sec-MS-GEC=" + token + "&Sec-MS-GEC-Version=" + secMSGECVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = voiceHeaders()
	req.Header.Set("Cookie", "muid="+muid()+";")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("list voices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		if err := c.adjustSkewFrom(resp); err != nil {
			return nil, err
		}
		return c.ListVoices(ctx)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list voices: unexpected status %d", resp.StatusCode)
	}
	var voices []Voice
	if err := json.NewDecoder(resp.Body).Decode(&voices); err != nil {
		return nil, fmt.Errorf("list voices: decode: %w", err)
	}
	return voices, nil
}

// Chunk is one streamed piece of a synthesis response.
type Chunk struct {
	// Audio carries MP3 bytes when non-empty.
	Audio []byte
	// Text and timing are populated for boundary metadata.
	Text     string
	Offset   int64
	Duration int64
	Boundary string
}

// Synthesize streams audio for text. The returned channel closes when the
// utterance ends; the error is delivered through the second return value only
// for failures that prevent streaming from starting.
func (c *Client) Synthesize(ctx context.Context, text string, cfg TTSConfig) (<-chan Chunk, <-chan error, error) {
	cfg = cfg.withDefaults()
	cleaned := escapeXML(removeIncompatible(text))
	chunks := splitText(cleaned, maxChunkBytes)
	if len(chunks) == 0 {
		return nil, nil, fmt.Errorf("text is empty after cleaning")
	}

	out := make(chan Chunk, 16)
	errs := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errs)
		var cumulative int64
		for i, chunk := range chunks {
			compensation, err := c.streamOne(ctx, chunk, cfg, out, cumulative)
			if err != nil {
				errs <- err
				return
			}
			cumulative += compensation
			_ = i
		}
	}()
	return out, errs, nil
}

// streamOne performs a single WebSocket utterance and returns the audio byte
// count so the caller can compensate timeline offsets across chunks.
func (c *Client) streamOne(ctx context.Context, ssmlText string, cfg TTSConfig, out chan<- Chunk, priorBytes int64) (int64, error) {
	url := wssURL + "?TrustedClientToken=" + drm.TrustedClientToken +
		"&ConnectionId=" + connectID() +
		"&Sec-MS-GEC=" + c.token() +
		"&Sec-MS-GEC-Version=" + secMSGECVersion

	headers := wsHeaders()
	headers.Set("Cookie", "muid="+muid()+";")

	conn, resp, err := c.dialer().DialContext(ctx, url, headers)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			if skewErr := c.adjustSkewFrom(resp); skewErr == nil {
				return c.streamOne(ctx, ssmlText, cfg, out, priorBytes)
			}
		}
		return 0, fmt.Errorf("websocket dial: %w", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(cfg.speechConfig())); err != nil {
		return 0, fmt.Errorf("send speech.config: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(ssmlMessage(connectID(), cfg.ssml(ssmlText)))); err != nil {
		return 0, fmt.Errorf("send ssml: %w", err)
	}

	var chunkBytes int64
	audioSeen := false
	for {
		msgType, payload, err := conn.ReadMessage()
		if debugFrames {
			fmt.Printf("[dbg] type=%d len=%d head=%q\n", msgType, len(payload), firstN(payload, 120))
		}
		if err != nil {
			if !audioSeen {
				return 0, fmt.Errorf("no audio received: %w", err)
			}
			return chunkBytes, nil
		}
		switch msgType {
		case websocket.TextMessage:
			path, body, ok := splitHeaderBlock(payload)
			if !ok {
				continue
			}
			switch path {
			case "turn.end":
				return chunkBytes, nil
			case "audio.metadata":
				if chunk, ok := parseMetadata(body, priorBytes, chunkBytes); ok {
					select {
					case out <- chunk:
					case <-ctx.Done():
						return chunkBytes, ctx.Err()
					}
				}
			}
		case websocket.BinaryMessage:
			if len(payload) < 2 {
				return chunkBytes, fmt.Errorf("binary frame too short")
			}
			// The first two bytes hold the big-endian length of the header
			// block that follows; audio begins right after that block.
			headerLen := int(binary.BigEndian.Uint16(payload[:2]))
			if debugFrames && headerLen < len(payload) {
				lo := headerLen - 8
				if lo < 0 {
					lo = 0
				}
				hi := headerLen + 8
				if hi > len(payload) {
					hi = len(payload)
				}
				fmt.Printf("[dbg] headerLen=%d region=%q\n", headerLen, payload[lo:hi])
			}
			headerEnd := 2 + headerLen
			if headerEnd > len(payload) {
				return chunkBytes, fmt.Errorf("binary frame header length out of range")
			}
			// A binary frame's header block is delimited by its length prefix
			// rather than by a blank line, so it ends with a single CRLF.
			path := headerPath(payload[2:headerEnd])
			if path != "audio" {
				continue
			}
			data := payload[headerEnd:]
			if len(data) == 0 {
				continue
			}
			audioSeen = true
			chunkBytes += int64(len(data))
			select {
			case out <- Chunk{Audio: data}:
			case <-ctx.Done():
				return chunkBytes, ctx.Err()
			}
		}
	}
}

// splitHeaderBlock separates a "Key:value\r\n...\r\n\r\nbody" frame.
var debugFrames = os.Getenv("NCP_DEBUG_FRAMES") != ""

func firstN(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	return string(b[:n])
}

func splitHeaderBlock(payload []byte) (path string, body []byte, ok bool) {
	idx := indexDoubleCRLF(payload)
	if idx < 0 {
		return "", nil, false
	}
	header := payload[:idx]
	body = payload[idx+4:]
	for _, line := range strings.Split(string(header), "\r\n") {
		if strings.HasPrefix(line, "Path:") {
			path = strings.TrimSpace(strings.TrimPrefix(line, "Path:"))
		}
	}
	return path, body, path != ""
}

// headerPath extracts the Path field from a raw header block.
func headerPath(header []byte) string {
	for _, line := range strings.Split(string(header), "\r\n") {
		if strings.HasPrefix(line, "Path:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Path:"))
		}
	}
	return ""
}

func indexDoubleCRLF(b []byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
}

// parseMetadata converts an audio.metadata frame into a Chunk.
func parseMetadata(body []byte, priorBytes, chunkBytes int64) (Chunk, bool) {
	var payload struct {
		Metadata []struct {
			Type string
			Data struct {
				Offset   int64
				Duration int64
				Text     struct {
					Text string
				}
			}
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Chunk{}, false
	}
	for _, m := range payload.Metadata {
		if m.Type != "WordBoundary" && m.Type != "SentenceBoundary" {
			continue
		}
		// Offsets are in 100ns ticks and drift across chunks; realign them to
		// the audio actually produced so far at a constant bitrate.
		compensation := (priorBytes + chunkBytes) * 8 * ticksPerSecond / mp3BitrateBPS
		return Chunk{
			Text:     m.Data.Text.Text,
			Offset:   m.Data.Offset + compensation,
			Duration: m.Data.Duration,
			Boundary: m.Type,
		}, true
	}
	return Chunk{}, false
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) dialer() *websocket.Dialer {
	if c.Dialer != nil {
		return c.Dialer
	}
	return &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
}

func (c *Client) token() string {
	ts := float64(time.Now().Add(c.ClockSkew).UnixNano()) / 1e9
	return drm.Generate(ts)
}

// adjustSkewFrom corrects ClockSkew using the server's Date header, which is
// the only reliable way to recover from a 403 caused by clock drift.
func (c *Client) adjustSkewFrom(resp *http.Response) error {
	date := resp.Header.Get("Date")
	if date == "" {
		return fmt.Errorf("403 with no server date; cannot correct clock skew")
	}
	server, err := http.ParseTime(date)
	if err != nil {
		return fmt.Errorf("403 with unparsable server date %q: %w", date, err)
	}
	c.ClockSkew = time.Until(server)
	return nil
}

// TicksToDuration converts service ticks (100ns) to a time.Duration.
func TicksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * 100 * time.Nanosecond
}

// ParsePercent validates "+10%" / "-20%" style rate or volume parameters.
func ParsePercent(v string) error {
	if v == "" {
		return nil
	}
	if !strings.HasSuffix(v, "%") {
		return fmt.Errorf("expected a percentage like +10%%, got %q", v)
	}
	if _, err := strconv.Atoi(strings.TrimSuffix(v, "%")); err != nil {
		return fmt.Errorf("invalid percentage %q", v)
	}
	return nil
}
