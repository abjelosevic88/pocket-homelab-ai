package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// voiceSent is what the fake Pocket Voice received for one recording.
type voiceSent struct {
	auth, name, ctype, lang, audio string
}

// voiceCore fakes Pocket Core (the person's links, link tokens) and Pocket
// Voice behind it; a recording that says "noise" can't be written out.
func voiceCore(t *testing.T, s *Server, linkOn *bool) *[]voiceSent {
	t.Helper()
	sent := &[]voiceSent{}
	voice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/speech":
			json.NewEncoder(w).Encode(map[string]any{"engine": "local", "ready": true})
		case "/api/v1/transcribe":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, fh, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(f)
			*sent = append(*sent, voiceSent{auth: r.Header.Get("Authorization"), name: fh.Filename, ctype: fh.Header.Get("Content-Type"), lang: r.FormValue("language"), audio: string(b)})
			if string(b) == "noise" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": "there were no words in the recording"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"text": " Call the plumber tomorrow. ", "language": "en", "segments": []any{}, "engine": "whisper"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(voice.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "voice", Scope: voiceScope, On: *linkOn, State: map[bool]string{true: "on", false: "off_person"}[*linkOn]}})
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_voice", URL: voice.URL, App: "voice"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	return sent
}

// recording is a browser's dictation upload: one webm file, maybe a language.
func recording(audio, lang string) ([]byte, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="../../dictation.webm"`)
	h.Set("Content-Type", "audio/webm;codecs=opus")
	fw, _ := mw.CreatePart(h)
	_, _ = fw.Write([]byte(audio))
	if lang != "" {
		_ = mw.WriteField("language", lang)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func TestVoiceDictation(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "voice@example.test", "member", true)
	var st struct {
		Voice  bool   `json:"voice"`
		Engine string `json:"engine"`
		Reason string `json:"reason"`
	}

	// no Core: the browser's own dictation, and nothing to send a recording to
	alice.want("GET", "/api/v1/dictation", nil, 200, &st)
	if st.Voice || !strings.Contains(st.Reason, "Pocket Core") {
		t.Fatalf("without core: %+v", st)
	}
	body, ctype := recording("hello", "")
	if code, b := alice.do("POST", "/api/v1/dictation", ctype, body); code != 409 {
		t.Fatalf("without core: %d %s", code, b)
	}

	// the link off: the same, with how to turn it on; Voice hears nothing
	on := false
	sent := voiceCore(t, s, &on)
	alice.want("GET", "/api/v1/dictation", nil, 200, &st)
	if st.Voice || !strings.Contains(st.Reason, "Pocket AI → Pocket Voice") {
		t.Fatalf("link off: %+v", st)
	}
	code, b := alice.do("POST", "/api/v1/dictation", ctype, body)
	if code != 409 || !strings.Contains(string(b), "Links") {
		t.Fatalf("link off: %d %s", code, b)
	}
	if len(*sent) != 0 {
		t.Fatalf("voice heard a recording with the link off: %+v", *sent)
	}

	// the link on: the recording goes to Voice as the person, the words come back
	on = true
	st.Reason = ""
	alice.want("GET", "/api/v1/dictation", nil, 200, &st)
	if !st.Voice || st.Engine != "local" || st.Reason != "" {
		t.Fatalf("link on: %+v", st)
	}
	var got struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	code, b = alice.do("POST", "/api/v1/dictation", ctype, body)
	if code != 200 || json.Unmarshal(b, &got) != nil || got.Text != "Call the plumber tomorrow." || got.Language != "en" {
		t.Fatalf("dictation: %d %s", code, b)
	}
	if len(*sent) != 1 {
		t.Fatalf("voice got %d recordings", len(*sent))
	}
	if r := (*sent)[0]; r.auth != "Bearer pcx_voice" || r.name != "dictation.webm" || r.ctype != "audio/webm;codecs=opus" || r.audio != "hello" || r.lang != "" {
		t.Fatalf("forwarded: %+v", r)
	}
	body, ctype = recording("hallo", "de")
	alice.want("POST", "/api/v1/dictation", nil, 400, nil) // not a recording
	if code, b := alice.do("POST", "/api/v1/dictation", ctype, body); code != 200 || (*sent)[1].lang != "de" {
		t.Fatalf("language: %d %s %+v", code, b, *sent)
	}

	// Voice can't make out the words: said plainly, not a server error
	body, ctype = recording("noise", "")
	code, b = alice.do("POST", "/api/v1/dictation", ctype, body)
	if code != 422 || !strings.Contains(string(b), "no words in the recording") {
		t.Fatalf("noise: %d %s", code, b)
	}
}
