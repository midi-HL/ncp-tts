package tts

import (
	"strings"
	"unicode/utf8"
)

// maxChunkBytes is the service's per-request SSML payload ceiling.
const maxChunkBytes = 4096

// removeIncompatible replaces control characters the service rejects. The
// vertical tab is the common offender (it shows up in text copied out of
// PDFs) and triggers a hard error rather than being ignored.
func removeIncompatible(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 0 && r <= 8) || (r >= 11 && r <= 12) || (r >= 14 && r <= 31) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func escapeXML(s string) string { return xmlEscaper.Replace(s) }

// splitText breaks text into chunks of at most maxChunkBytes bytes, preferring
// newline then space boundaries and never cutting a multi-byte rune or an XML
// entity in half.
func splitText(text string, maxBytes int) []string {
	data := []byte(text)
	var out []string
	for len(data) > maxBytes {
		splitAt := -1
		if i := lastIndexByte(data[:maxBytes], '\n'); i >= 0 {
			splitAt = i
		} else if i := lastIndexByte(data[:maxBytes], ' '); i >= 0 {
			splitAt = i
		} else {
			// No boundary to prefer, so cut at the largest valid UTF-8 prefix
			// that still fits the limit.
			splitAt = safeUTF8Split(data[:maxBytes])
		}
		splitAt = adjustForXMLEntity(data, splitAt)
		if splitAt <= 0 {
			splitAt = safeUTF8Split(data[:maxBytes])
		}
		if chunk := strings.TrimSpace(string(data[:splitAt])); chunk != "" {
			out = append(out, chunk)
		}
		if splitAt > 0 {
			data = data[splitAt:]
		} else {
			data = data[1:]
		}
	}
	if rest := strings.TrimSpace(string(data)); rest != "" {
		out = append(out, rest)
	}
	return out
}

func lastIndexByte(data []byte, c byte) int {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == c {
			return i
		}
	}
	return -1
}

// safeUTF8Split returns the largest prefix length that decodes cleanly.
func safeUTF8Split(data []byte) int {
	n := len(data)
	for n > 0 {
		if utf8.Valid(data[:n]) {
			return n
		}
		n--
	}
	return 0
}

// adjustForXMLEntity walks back so a split never lands inside "&amp;" style
// entities that were produced by escaping.
func adjustForXMLEntity(data []byte, splitAt int) int {
	for splitAt > 0 {
		idx := lastIndexByte(data[:splitAt], '&')
		if idx < 0 {
			return splitAt
		}
		semi := -1
		for i := idx; i < splitAt; i++ {
			if data[i] == ';' {
				semi = i
				break
			}
		}
		if semi >= 0 {
			return splitAt
		}
		splitAt = idx
	}
	return splitAt
}

// TTSConfig carries the per-request voice parameters.
type TTSConfig struct {
	Voice    string
	Rate     string
	Volume   string
	Pitch    string
	Boundary string // "SentenceBoundary" (default) or "WordBoundary"
}

// withDefaults fills unset parameters with the service-neutral values.
func (c TTSConfig) withDefaults() TTSConfig {
	if c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.Rate == "" {
		c.Rate = "+0%"
	}
	if c.Volume == "" {
		c.Volume = "+0%"
	}
	if c.Pitch == "" {
		c.Pitch = "+0Hz"
	}
	if c.Boundary == "" {
		c.Boundary = "SentenceBoundary"
	}
	return c
}

// ssml renders the synthesis request body.
func (c TTSConfig) ssml(escapedText string) string {
	return "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>" +
		"<voice name='" + c.Voice + "'>" +
		"<prosody pitch='" + c.Pitch + "' rate='" + c.Rate + "' volume='" + c.Volume + "'>" +
		escapedText +
		"</prosody></voice></speak>"
}

// speechConfig is the JSON handshake that selects the output format.
func (c TTSConfig) speechConfig() string {
	wordBoundary := "false"
	sentenceBoundary := "true"
	if c.Boundary == "WordBoundary" {
		wordBoundary = "true"
		sentenceBoundary = "false"
	}
	return "X-Timestamp:" + dateString() + "\r\n" +
		"Content-Type:application/json; charset=utf-8\r\n" +
		"Path:speech.config\r\n\r\n" +
		`{"context":{"synthesis":{"audio":{"metadataoptions":{` +
		`"sentenceBoundaryEnabled":"` + sentenceBoundary + `","wordBoundaryEnabled":"` + wordBoundary + `"` +
		`},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}` + "\r\n"
}

// ssmlMessage wraps SSML in the service's text-frame header block.
func ssmlMessage(requestID, ssml string) string {
	return "X-RequestId:" + requestID + "\r\n" +
		"Content-Type:application/ssml+xml\r\n" +
		"X-Timestamp:" + dateString() + "Z\r\n" +
		"Path:ssml\r\n\r\n" +
		ssml
}
