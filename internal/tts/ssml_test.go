package tts

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRemoveIncompatible(t *testing.T) {
	in := "a\x00b\x0bc\x1fd"
	got := removeIncompatible(in)
	if strings.ContainsAny(got, "\x00\x0b\x1f") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "d") {
		t.Fatalf("visible characters were dropped: %q", got)
	}
}

func TestEscapeXML(t *testing.T) {
	got := escapeXML(`a & b < c > d " e ' f`)
	want := "a &amp; b &lt; c &gt; d &quot; e &apos; f"
	if got != want {
		t.Fatalf("escapeXML = %q, want %q", got, want)
	}
}

func TestSplitTextShortInput(t *testing.T) {
	got := splitText("hello", maxChunkBytes)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("splitText = %v, want [hello]", got)
	}
}

func TestSplitTextRespectsLimit(t *testing.T) {
	input := strings.Repeat("word ", 2000)
	chunks := splitText(input, 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 1000 {
			t.Fatalf("chunk %d is %d bytes, over the 1000 limit", i, len(c))
		}
	}
	// Reassembly must preserve every word: only whitespace at the cut points
	// may be dropped.
	if n := len(strings.Fields(strings.Join(chunks, " "))); n != 2000 {
		t.Fatalf("word count changed: %d, want 2000", n)
	}
}

// TestSplitTextNeverBreaksRunes is the important regression: cutting a
// multi-byte character in half produces invalid UTF-8 and the service rejects
// the whole request.
func TestSplitTextNeverBreaksRunes(t *testing.T) {
	input := strings.Repeat("中文字符测试", 400)
	chunks := splitText(input, 500)
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
		if len(c) > 500 {
			t.Fatalf("chunk %d is %d bytes, over the 500 limit", i, len(c))
		}
	}
	if joined := strings.Join(chunks, ""); joined != input {
		t.Fatalf("reassembled text differs (len %d vs %d)", len(joined), len(input))
	}
}

func TestSplitTextKeepsEntitiesWhole(t *testing.T) {
	// A long run of escaped ampersands puts entity boundaries right where the
	// byte limit falls.
	input := strings.Repeat("&amp;", 400)
	chunks := splitText(input, 100)
	for i, c := range chunks {
		if strings.HasSuffix(c, "&") || strings.HasSuffix(c, "&am") || strings.HasSuffix(c, "&amp") {
			t.Fatalf("chunk %d ends mid-entity: %q", i, c)
		}
	}
}

func TestTTSConfigDefaults(t *testing.T) {
	got := TTSConfig{}.withDefaults()
	if got.Voice != DefaultVoice {
		t.Errorf("Voice = %q, want %q", got.Voice, DefaultVoice)
	}
	if got.Rate != "+0%" || got.Volume != "+0%" || got.Pitch != "+0Hz" {
		t.Errorf("unexpected defaults: %+v", got)
	}
	if got.Boundary != "SentenceBoundary" {
		t.Errorf("Boundary = %q", got.Boundary)
	}
}

func TestSpeechConfigBoundaryFlags(t *testing.T) {
	sentence := TTSConfig{}.withDefaults().speechConfig()
	if !strings.Contains(sentence, `"sentenceBoundaryEnabled":"true"`) {
		t.Errorf("sentence boundary not enabled: %s", sentence)
	}
	word := TTSConfig{Boundary: "WordBoundary"}.withDefaults().speechConfig()
	if !strings.Contains(word, `"wordBoundaryEnabled":"true"`) {
		t.Errorf("word boundary not enabled: %s", word)
	}
	if !strings.Contains(word, "audio-24khz-48kbitrate-mono-mp3") {
		t.Errorf("output format missing: %s", word)
	}
}

func TestSSMLContainsVoiceAndProsody(t *testing.T) {
	cfg := TTSConfig{Voice: "zh-CN-XiaoxiaoNeural", Rate: "+10%"}.withDefaults()
	ssml := cfg.ssml("hello")
	for _, want := range []string{"zh-CN-XiaoxiaoNeural", "rate='+10%'", "hello", "<speak"} {
		if !strings.Contains(ssml, want) {
			t.Errorf("ssml missing %q: %s", want, ssml)
		}
	}
}
