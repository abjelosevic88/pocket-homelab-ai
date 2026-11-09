package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The Core link Pocket AI → Pocket Voice (voice:transcribe). While it is on,
// the composer's mic records in the browser and Pocket AI hands the recording
// to Voice as the person: whisper on this server writes it out with their
// language and words settings, and Voice keeps no recording; the words go to
// the person's dictation history there, with where they were said (the phone
// app or the web page). While it is on there
// is no falling back to the browser's speech recognition (Chrome's sends the
// audio to Google); while it is off the mic is that, as before.
const voiceScope = "voice:transcribe"

var voiceUse = core.Use{App: "voice", Scopes: []string{voiceScope},
	Reason: "Write out what you dictate in the composer on your own server, with Pocket Voice, instead of a cloud service"}

var (
	voiceHTTP  = &http.Client{Timeout: 5 * time.Minute} // whisper on a long dictation takes a while
	voiceExtRe = regexp.MustCompile(`^\.[a-z0-9]{1,5}$`)
	// a Voice speech model id (GET /speech in Voice): parakeet-tdt-0.6b-v3-q8_0, large-v3-turbo-q5_0, …
	voiceModelRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// voiceLinkOff is why dictation can't go to Voice for the person, "" when it can.
func (s *Server) voiceLinkOff(ctx context.Context, email string) string {
	if !s.core.Configured() {
		return "There is no Pocket Core on this box, so Pocket AI can't reach Pocket Voice."
	}
	on, state := s.core.LinkOn(ctx, email, "voice", voiceScope)
	switch {
	case on:
		return ""
	case state == "target_missing":
		return "Pocket Voice isn't on this box."
	case state == "not_offered":
		return "This Pocket Voice can't write out dictation for other apps yet; update it."
	}
	return "Turn on Pocket AI → Pocket Voice in Pocket Core's Links to write out dictation on your own server."
}

// voiceCall makes a Voice API request as the person and decodes the answer.
func (s *Server) voiceCall(ctx context.Context, email, method, path, contentType string, body io.Reader, out any) (int, error) {
	tok, err := s.core.ActAs(ctx, email, "voice", voiceScope)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, onThisHost(strings.TrimRight(tok.URL, "/"))+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := voiceHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return res.StatusCode, errors.New(e.Error)
	}
	return res.StatusCode, json.NewDecoder(http.MaxBytesReader(nil, res.Body, 4<<20)).Decode(out)
}

// voiceModel is a speech model Voice can write dictation out with, as GET /dictation lists it.
type voiceModel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Note   string `json:"note"`
}

// GET /dictation: does the mic record for Pocket Voice (and how Voice writes
// it out: "local" whisper here, or "api", the speech service set in Voice).
// With "local", also the models downloaded there (`models`) and the one used
// when POST /dictation names none (`default_model`). `drop_fillers`: the
// person's Voice setting, for dictation written out on the phone itself.
func (s *Server) handleDictation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if why := s.voiceLinkOff(ctx, u.Email); why != "" {
		writeJSON(w, http.StatusOK, map[string]any{"voice": false, "reason": why})
		return
	}
	out := map[string]any{"voice": true}
	var sp struct {
		Engine         string `json:"engine"`
		DictationModel string `json:"dictation_model"`
		DropFillers    *bool  `json:"drop_fillers"`
		Models         []struct {
			voiceModel
			Ready bool `json:"ready"`
			Runs  bool `json:"runs"`
		} `json:"models"`
	}
	quick, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.voiceCall(quick, u.Email, http.MethodGet, "/api/v1/speech", "", nil, &sp); err == nil && sp.Engine != "" {
		out["engine"] = sp.Engine
		models := []voiceModel{}
		if sp.Engine == "local" {
			for _, m := range sp.Models {
				if m.Ready && m.Runs {
					models = append(models, m.voiceModel)
				}
			}
			out["default_model"] = sp.DictationModel
		}
		out["models"] = models
		// Voice's "Take out uh and um", for what the phone writes out itself
		if sp.DropFillers != nil {
			out["drop_fillers"] = *sp.DropFillers
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /dictation (multipart "file", optional "language" and "model", an id
// from the models GET /dictation lists): the words of a recording, written
// out by Pocket Voice. Without "model", Voice's dictation model.
func (s *Server) handleDictate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if why := s.voiceLinkOff(ctx, u.Email); why != "" {
		writeErr(w, http.StatusConflict, why)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "send the recording as multipart field \"file\" (at most 32 MB)")
		return
	}
	defer r.MultipartForm.RemoveAll() //nolint:errcheck
	f, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no recording in field \"file\"")
		return
	}
	defer f.Close()

	// a form of our own for Voice: the recording under a plain name, its type kept
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	name := "dictation"
	if ext := strings.ToLower(filepath.Ext(fh.Filename)); voiceExtRe.MatchString(ext) {
		name += ext
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", "application/octet-stream")
	if ct := fh.Header.Get("Content-Type"); strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
		h.Set("Content-Type", ct)
	}
	part, err := mw.CreatePart(h)
	if err == nil {
		_, err = io.Copy(part, f)
	}
	if lang := strings.TrimSpace(r.FormValue("language")); err == nil && lang != "" {
		err = mw.WriteField("language", lang)
	}
	if model := strings.TrimSpace(r.FormValue("model")); err == nil && model != "" {
		if !voiceModelRe.MatchString(model) {
			writeErr(w, http.StatusBadRequest, "model is a speech model id from GET /dictation")
			return
		}
		err = mw.WriteField("model", model)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the recording did not arrive whole")
		return
	}

	var got struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Engine   string `json:"engine"`
		Duration int64  `json:"duration_ms"` // the recording's length (Voice 0.7.9+)
		Segments []struct {
			End int64 `json:"e"` // ms
		} `json:"segments"`
	}
	at := time.Now()
	code, err := s.voiceCall(ctx, u.Email, http.MethodPost, "/api/v1/transcribe", mw.FormDataContentType(), &body, &got)
	switch {
	case err == nil:
		text := strings.TrimSpace(got.Text)
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "language": got.Language})
		if text != "" {
			d := voiceDictation{Text: text, App: "Pocket AI", Engine: got.Engine, Language: got.Language, DictatedAt: at.UTC().Format(time.RFC3339Nano)}
			d.Source, d.Device = dictationClient(r)
			d.DurationMS = got.Duration
			if n := len(got.Segments); d.DurationMS <= 0 && n > 0 {
				d.DurationMS = got.Segments[n-1].End // an older Voice: where the words end
			}
			go s.keepDictation(context.WithoutCancel(ctx), u.Email, d)
		}
	case code == http.StatusUnprocessableEntity || code == http.StatusBadRequest:
		writeErr(w, http.StatusUnprocessableEntity, "Pocket Voice couldn't write it out: "+err.Error())
	default:
		s.log.Warn("voice dictation", "status", code, "err", err)
		if code == 0 {
			writeErr(w, http.StatusBadGateway, "Pocket Voice can't be reached right now; nothing was sent anywhere else.")
			return
		}
		writeErr(w, http.StatusBadGateway, "Pocket Voice: "+err.Error())
	}
}

// voiceDictation is one dictation for the person's history in Pocket Voice
// (POST /api/v1/dictations there).
type voiceDictation struct {
	Ref        string `json:"ref"`
	Text       string `json:"text"`
	App        string `json:"app"`
	DurationMS int64  `json:"duration_ms"`
	Engine     string `json:"engine"`
	Language   string `json:"language"`
	Device     string `json:"device"`
	Source     string `json:"source"`
	DictatedAt string `json:"dictated_at"`
}

// keepDictation adds what was just dictated to the person's dictation history
// in Voice. The words are already back in the composer, so a failure here only
// costs the history line.
func (s *Server) keepDictation(ctx context.Context, email string, d voiceDictation) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var ref [16]byte
	_, _ = rand.Read(ref[:])
	d.Ref = "pocket-ai-" + hex.EncodeToString(ref[:])
	b, _ := json.Marshal(d)
	var out map[string]any
	if code, err := s.voiceCall(ctx, email, http.MethodPost, "/api/v1/dictations", "application/json", bytes.NewReader(b), &out); err != nil {
		s.log.Warn("voice dictation history", "status", code, "err", err)
	}
}

// dictationRefRe is a client's own id for one dictation (a UUID, with a prefix).
var dictationRefRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// POST /dictations (JSON {ref, text, duration_ms, engine, language,
// dictated_at, source, device}): something dictated and written out on the
// device itself (Parakeet or Apple's model on the phone), for the person's
// dictation history in Voice. The phone posts each one from a retrying
// outbox; `ref` (the phone's id for it) makes a repeat harmless: Voice answers
// 200 with the row it already has. Answers 201 or 200 once Voice has it, 409
// while the link is off (the outbox keeps it), 400 for a bad one (the outbox
// drops it), 502 when Voice can't be reached (the outbox tries again later).
func (s *Server) handleKeepDictation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Ref        string `json:"ref"`
		Text       string `json:"text"`
		DurationMS int64  `json:"duration_ms"`
		Engine     string `json:"engine"`
		Language   string `json:"language"`
		DictatedAt string `json:"dictated_at"`
		Source     string `json:"source"`
		Device     string `json:"device"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "send the dictation as JSON")
		return
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case !dictationRefRe.MatchString(in.Ref):
		writeErr(w, http.StatusBadRequest, "ref is the client's id for this dictation (1–64 letters, digits, . _ : -)")
		return
	case text == "":
		writeErr(w, http.StatusBadRequest, "text is what was dictated")
		return
	case len([]rune(text)) > 20000:
		writeErr(w, http.StatusBadRequest, "text is at most 20000 characters")
		return
	case in.DurationMS < 0:
		writeErr(w, http.StatusBadRequest, "duration_ms is the speaking time, 0 or more")
		return
	}
	at := time.Now()
	if in.DictatedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, in.DictatedAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "dictated_at is RFC 3339 with an offset")
			return
		}
		if t.After(at.Add(5 * time.Minute)) {
			t = at // a phone clock running ahead: Voice would refuse it
		}
		at = t
	}
	if why := s.voiceLinkOff(ctx, u.Email); why != "" {
		writeErr(w, http.StatusConflict, why)
		return
	}
	d := voiceDictation{Ref: in.Ref, Text: text, App: "Pocket AI", DurationMS: in.DurationMS,
		Engine: clipRunes(strings.TrimSpace(in.Engine), 60), Language: clipRunes(strings.TrimSpace(in.Language), 16),
		DictatedAt: at.UTC().Format(time.RFC3339Nano)}
	d.Source, d.Device = clipRunes(strings.TrimSpace(in.Source), 100), clipRunes(strings.TrimSpace(in.Device), 100)
	if d.Source == "" || d.Device == "" {
		src, dev := dictationClient(r)
		if d.Source == "" {
			d.Source = src
		}
		if d.Device == "" {
			d.Device = dev
		}
	}
	quick, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, _ := json.Marshal(d)
	var out struct {
		Dictation map[string]any `json:"dictation"`
	}
	code, err := s.voiceCall(quick, u.Email, http.MethodPost, "/api/v1/dictations", "application/json", bytes.NewReader(b), &out)
	switch {
	case err == nil:
		if code != http.StatusCreated {
			code = http.StatusOK
		}
		writeJSON(w, code, map[string]any{"kept": true, "ref": d.Ref})
	case code == http.StatusBadRequest:
		writeErr(w, http.StatusBadRequest, "Pocket Voice didn't take it: "+err.Error())
	default:
		s.log.Warn("voice dictation history", "status", code, "err", err)
		writeErr(w, http.StatusBadGateway, "Pocket Voice can't be reached right now; try again later.")
	}
}

// dictationClient is where a dictation in the composer was made, as Voice's
// history shows it: the form's "source" and "device" when the client sends
// them, else read from its User-Agent (the iPhone app's requests come from
// iOS's URL loading, CFNetwork; anything else is the web page in a browser).
func dictationClient(r *http.Request) (source, device string) {
	source, device = clipRunes(strings.TrimSpace(r.FormValue("source")), 100), clipRunes(strings.TrimSpace(r.FormValue("device")), 100)
	ua := r.UserAgent()
	if source == "" {
		source = "Pocket AI on the web"
		if strings.Contains(ua, "CFNetwork") {
			source = "Pocket AI app"
		}
	}
	if device == "" {
		device = uaDevice(ua)
	}
	return source, device
}

// uaDevice names the device (and the browser on it) a User-Agent comes from:
// "iPhone", "Safari on Mac", "Chrome on Windows"; "" when it can't tell.
func uaDevice(ua string) string {
	if strings.Contains(ua, "CFNetwork") { // a native app on iOS
		if strings.Contains(ua, "iPad") {
			return "iPad"
		}
		return "iPhone"
	}
	osName := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		osName = "iPhone"
	case strings.Contains(ua, "iPad"):
		osName = "iPad"
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "Macintosh"):
		osName = "Mac"
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "CrOS"):
		osName = "Chromebook"
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgiOS/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case osName == "":
		return browser
	case browser == "":
		return osName
	}
	return browser + " on " + osName
}
