package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midi-HL/ncp-tts/internal/api"
	"github.com/midi-HL/ncp-tts/internal/tts"
)

type fakeSpeech struct {
	input  string
	config tts.TTSConfig
	err    error
}

func (f *fakeSpeech) ListVoices(context.Context) ([]tts.Voice, error) {
	return []tts.Voice{
		{ShortName: "en-US-AriaNeural", Locale: "en-US"},
		{ShortName: "zh-CN-XiaoxiaoNeural", Locale: "zh-CN", Gender: "Female", FriendlyName: "Xiaoxiao"},
	}, f.err
}

func (f *fakeSpeech) Synthesize(_ context.Context, input string, config tts.TTSConfig) (<-chan tts.Chunk, <-chan error, error) {
	f.input = input
	f.config = config
	audio := make(chan tts.Chunk, 2)
	audio <- tts.Chunk{Audio: []byte{0xff, 0xf3}}
	audio <- tts.Chunk{Audio: []byte{0xaa}}
	close(audio)
	errs := make(chan error, 1)
	if f.err != nil {
		errs <- f.err
	}
	close(errs)
	return audio, errs, nil
}

func request(handler http.Handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestSpeechOptionsAndNoAuthentication(t *testing.T) {
	for _, test := range []struct{ label, body, auth, voice, rate string }{
		{"default", `{"model":"edge-tts","input":"你好"}`, "", "zh-CN-XiaoxiaoNeural", "+0%"},
		{"custom", `{"model":"tts-1","input":"你好","voice":"zh-CN-YunxiNeural","speed":1.2,"response_format":"mp3"}`, "Bearer arbitrary", "zh-CN-YunxiNeural", "+20%"},
	} {
		t.Run(test.label, func(t *testing.T) {
			fake := &fakeSpeech{}
			w := request(api.NewHandler(fake, nil), http.MethodPost, "/v1/audio/speech", test.body, test.auth)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.String() != "\xff\xf3\xaa" {
				t.Fatalf("status=%d content type=%s body=%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			if fake.input != "你好" || fake.config.Voice != test.voice || fake.config.Rate != test.rate {
				t.Fatalf("unexpected upstream request: %q %+v", fake.input, fake.config)
			}
		})
	}
}

func TestModelAndVoiceDiscovery(t *testing.T) {
	handler := api.NewHandler(&fakeSpeech{}, nil)
	for _, test := range []struct {
		path string
		ids  []string
	}{
		{"/v1/models", []string{"edge-tts", "tts-1"}},
		{"/v1/voices?locale=ZH-cn", []string{"zh-CN-XiaoxiaoNeural"}},
		{"/v1/voices?locale=none", []string{}},
	} {
		w := request(handler, http.MethodGet, test.path, "", "")
		var body struct {
			Object string
			Data   []struct{ ID string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Object != "list" || body.Data == nil || len(body.Data) != len(test.ids) {
			t.Fatalf("%s: status=%d body=%s error=%v", test.path, w.Code, w.Body.String(), err)
		}
		for i, id := range test.ids {
			if body.Data[i].ID != id {
				t.Errorf("%s: item %d = %q, want %q", test.path, i, body.Data[i].ID, id)
			}
		}
	}
	w := request(handler, http.MethodGet, "/v1/models/tts-1", "", "")
	var model struct{ ID, Object string }
	if err := json.Unmarshal(w.Body.Bytes(), &model); err != nil || w.Code != 200 || model.ID != "tts-1" || model.Object != "model" {
		t.Fatalf("retrieve model: status=%d body=%s error=%v", w.Code, w.Body.String(), err)
	}
}

func TestSpeechErrorsAreJSON(t *testing.T) {
	for _, test := range []struct {
		body, code string
		status     int
		upstream   error
	}{
		{`{"model":"bogus","input":"text"}`, "model_not_found", 404, nil},
		{`{"model":"tts-1","input":"  "}`, "invalid_value", 400, nil},
		{`{"model":"tts-1","input":"text","speed":0}`, "invalid_value", 400, nil},
		{`{"model":"tts-1","input":"text","response_format":"gif"}`, "invalid_value", 400, nil},
		{`{"model":"tts-1","input":"text"} []`, "invalid_json", 400, nil},
		{`{"model":"tts-1","input":"text"}`, "upstream_error", 502, errors.New("upstream interrupted after audio")},
	} {
		fake := &fakeSpeech{err: test.upstream}
		w := request(api.NewHandler(fake, nil), http.MethodPost, "/v1/audio/speech", test.body, "")
		var body struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != test.status || body.Error.Code != test.code || body.Error.Message == "" {
			t.Fatalf("input %s: status=%d body=%s error=%v", test.body, w.Code, w.Body.String(), err)
		}
		if test.upstream == nil && fake.input != "" {
			t.Errorf("invalid input reached upstream: %s", test.body)
		}
	}
}
