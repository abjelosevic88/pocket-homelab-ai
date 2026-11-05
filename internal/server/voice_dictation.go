package server

import (
	"bytes"
	"context"
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
// language and words settings, and Voice keeps nothing. While it is on there
// is no falling back to the browser's speech recognition (Chrome's sends the
// audio to Google); while it is off the mic is that, as before.
const voiceScope = "voice:transcribe"

var voiceUse = core.Use{App: "voice", Scopes: []string{voiceScope},
	Reason: "Write out what you dictate in the composer on your own server, with Pocket Voice, instead of a cloud service"}

var (
	voiceHTTP  = &http.Client{Timeout: 5 * time.Minute} // whisper on a long dictation takes a while
	voiceExtRe = regexp.MustCompile(`^\.[a-z0-9]{1,5}$`)
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
	if res.StatusCode != http.StatusOK {
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

// GET /dictation: does the mic record for Pocket Voice (and how Voice writes
// it out: "local" whisper here, or "api", the speech service set in Voice).
func (s *Server) handleDictation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if why := s.voiceLinkOff(ctx, u.Email); why != "" {
		writeJSON(w, http.StatusOK, map[string]any{"voice": false, "reason": why})
		return
	}
	out := map[string]any{"voice": true}
	var sp struct {
		Engine string `json:"engine"`
	}
	quick, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.voiceCall(quick, u.Email, http.MethodGet, "/api/v1/speech", "", nil, &sp); err == nil && sp.Engine != "" {
		out["engine"] = sp.Engine
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /dictation (multipart "file", optional "language"): the words of a
// recording, written out by Pocket Voice.
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
	}
	code, err := s.voiceCall(ctx, u.Email, http.MethodPost, "/api/v1/transcribe", mw.FormDataContentType(), &body, &got)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"text": strings.TrimSpace(got.Text), "language": got.Language})
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
