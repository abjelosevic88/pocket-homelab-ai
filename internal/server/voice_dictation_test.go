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
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// voiceSent is what the fake Pocket Voice received for one recording.
type voiceSent struct {
	auth, name, ctype, lang, model, audio string
}

// voiceCore fakes Pocket Core (the person's links, link tokens) and Pocket
// Voice behind it; a recording that says "noise" can't be written out.
func voiceCore(t *testing.T, s *Server, linkOn *bool) *[]voiceSent {
	t.Helper()
	sent, _ := voiceCoreKept(t, s, linkOn)
	return sent
}

// voiceCoreKept is voiceCore that also hands over each dictation Pocket AI
// adds to the person's history in Voice (POST /api/v1/dictations).
func voiceCoreKept(t *testing.T, s *Server, linkOn *bool) (*[]voiceSent, chan map[string]any) {
	t.Helper()
	sent := &[]voiceSent{}
	kept := make(chan map[string]any, 64)
	voice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/speech":
			json.NewEncoder(w).Encode(map[string]any{"engine": "local", "ready": true, "dictation_model": "parakeet-tdt-0.6b-v3-q8_0", "models": []any{
				map[string]any{"id": "parakeet-tdt-0.6b-v3-q8_0", "name": "Fastest", "engine": "parakeet", "ready": true, "runs": true},
				map[string]any{"id": "large-v3-turbo-q5_0", "name": "Best", "engine": "whisper", "ready": true, "runs": true},
				map[string]any{"id": "small", "name": "Good", "engine": "whisper", "ready": false, "runs": true},
			}})
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
			*sent = append(*sent, voiceSent{auth: r.Header.Get("Authorization"), name: fh.Filename, ctype: fh.Header.Get("Content-Type"), lang: r.FormValue("language"), model: r.FormValue("model"), audio: string(b)})
			if string(b) == "noise" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": "there were no words in the recording"})
				return
			}
			out := map[string]any{"text": " Call the plumber tomorrow. ", "language": "en", "engine": "parakeet",
				"segments": []any{map[string]any{"s": 0, "e": 1200, "t": "Call the plumber"}, map[string]any{"s": 1200, "e": 2300, "t": "tomorrow."}}}
			if string(b) == "short" { // Parakeet on a short clip: no timings, Voice says how long it was
				out["segments"], out["duration_ms"] = []any{map[string]any{"s": 0, "e": 0, "t": "Call the plumber tomorrow."}}, 2650
			}
			json.NewEncoder(w).Encode(out)
		case "/api/v1/dictations":
			var d map[string]any
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer pcx_voice" || json.NewDecoder(r.Body).Decode(&d) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case kept <- d:
			default:
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"dictation": d})
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
	return sent, kept
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

// The model for one dictation: GET /dictation lists the ones downloaded in
// Voice and its default; POST /dictation passes the chosen one on, and none
// when none was chosen (Voice's default, so other apps are unchanged).
func TestVoiceDictationModel(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "voice-model@example.test", "member", true)
	on := true
	sent := voiceCore(t, s, &on)
	var st struct {
		Voice        bool         `json:"voice"`
		DefaultModel string       `json:"default_model"`
		Models       []voiceModel `json:"models"`
	}
	alice.want("GET", "/api/v1/dictation", nil, 200, &st)
	if !st.Voice || st.DefaultModel != "parakeet-tdt-0.6b-v3-q8_0" || len(st.Models) != 2 || st.Models[1].ID != "large-v3-turbo-q5_0" || st.Models[1].Name != "Best" {
		t.Fatalf("models: %+v", st)
	}
	withModel := func(model string) ([]byte, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "dictation.m4a")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.WriteField("model", model)
		_ = mw.Close()
		return buf.Bytes(), mw.FormDataContentType()
	}
	body, ctype := recording("hello", "")
	if code, b := alice.do("POST", "/api/v1/dictation", ctype, body); code != 200 || (*sent)[0].model != "" {
		t.Fatalf("no model: %d %s %+v", code, b, *sent)
	}
	body, ctype = withModel("large-v3-turbo-q5_0")
	if code, b := alice.do("POST", "/api/v1/dictation", ctype, body); code != 200 || (*sent)[1].model != "large-v3-turbo-q5_0" {
		t.Fatalf("whisper: %d %s %+v", code, b, *sent)
	}
	body, ctype = withModel("../etc/passwd")
	if code, b := alice.do("POST", "/api/v1/dictation", ctype, body); code != 400 || len(*sent) != 2 {
		t.Fatalf("bad model: %d %s", code, b)
	}
}

// Each dictation written out goes to the person's dictation history in Voice,
// with where it was said: the iPhone app (iOS's CFNetwork) or the web page
// (the browser and device), or what the client says itself.
func TestVoiceDictationHistory(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "history@example.test", "member", true)
	on := true
	_, kept := voiceCoreKept(t, s, &on)
	next := func() map[string]any {
		t.Helper()
		select {
		case d := <-kept:
			return d
		case <-time.After(3 * time.Second):
			t.Fatal("no dictation reached Voice's history")
		}
		return nil
	}
	send := func(ua string, fields map[string]string, audio ...string) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "dictation.m4a")
		_, _ = fw.Write([]byte(append(audio, "hello")[0]))
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/api/v1/dictation", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+alice.token)
		req.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("dictation: %d %s", w.Code, w.Body.String())
		}
	}

	send("Pocket%20AI/412 CFNetwork/3826.500.131 Darwin/24.5.0", nil)
	d := next()
	if d["source"] != "Pocket AI app" || d["device"] != "iPhone" || d["app"] != "Pocket AI" || d["text"] != "Call the plumber tomorrow." ||
		d["engine"] != "parakeet" || d["language"] != "en" || d["duration_ms"] != float64(2300) || !strings.HasPrefix(d["ref"].(string), "pocket-ai-") {
		t.Fatalf("from the app: %v", d)
	}
	if _, err := time.Parse(time.RFC3339Nano, d["dictated_at"].(string)); err != nil {
		t.Fatalf("dictated_at: %v", d["dictated_at"])
	}
	send("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", nil)
	if d2 := next(); d2["source"] != "Pocket AI on the web" || d2["device"] != "Chrome on Mac" || d2["ref"] == d["ref"] {
		t.Fatalf("from the web: %v", d2)
	}
	send("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", nil)
	if d3 := next(); d3["source"] != "Pocket AI on the web" || d3["device"] != "Safari on iPhone" {
		t.Fatalf("web on the phone: %v", d3)
	}
	send("Pocket%20AI/412 CFNetwork/3826.500.131 Darwin/24.5.0", map[string]string{"source": " Pocket AI app ", "device": "Aleksandar’s iPhone"})
	if d4 := next(); d4["source"] != "Pocket AI app" || d4["device"] != "Aleksandar’s iPhone" {
		t.Fatalf("named by the client: %v", d4)
	}
	send("Pocket%20AI/412 CFNetwork/3826.500.131 Darwin/24.5.0", nil, "short")
	if d5 := next(); d5["duration_ms"] != float64(2650) {
		t.Fatalf("length from Voice: %v", d5)
	}
}
