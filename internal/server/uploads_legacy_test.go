package server

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyOfficeUploadConvertsThroughCollabora(t *testing.T) {
	s, h, _ := testServer(t)
	_, me := person(t, s, h, "doc@example.com", "admin", true)
	var docx bytes.Buffer
	z := zip.NewWriter(&docx)
	f, _ := z.Create("word/document.xml")
	f.Write([]byte(`<w:document xmlns:w="word"><w:p><w:r><w:t>Old memo</w:t></w:r></w:p></w:document>`))
	z.Close()
	var asked string
	conv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if _, _, err := r.FormFile("data"); err != nil {
			http.Error(w, "no data", 400)
			return
		}
		w.Write(docx.Bytes())
	}))
	defer conv.Close()
	doc := append(append([]byte{}, oleMagic...), make([]byte, 512)...)

	if _, err := s.saveUpload(context.Background(), me.ID, "memo.doc", doc); err == nil || !strings.Contains(err.Error(), "converter") {
		t.Fatalf("no converter configured: %v", err)
	}
	s.cfg.OfficeConvertURL = conv.URL
	if _, err := s.saveUpload(context.Background(), me.ID, "fake.doc", []byte("not ole")); err == nil {
		t.Fatal("non-OLE .doc accepted")
	}
	u, err := s.saveUpload(context.Background(), me.ID, "memo.doc", doc)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "/cool/convert-to/docx" || !strings.Contains(u.ExtractedText, "Old memo") || u.Name != "memo.doc" {
		t.Fatalf("path %q, upload %+v", asked, u)
	}
}
