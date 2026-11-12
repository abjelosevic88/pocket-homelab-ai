package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWithoutPicturesKeepsLogSmall(t *testing.T) {
	data := strings.Repeat("QUJD", 1000) // 3000 bytes
	in := json.RawMessage(`{"isError":false,"structuredContent":{"result":{"file_name":"shot.png"}},"content":[{"type":"text","text":"{}"},{"type":"image","mimeType":"image/png","data":"` + data + `"}]}`)
	out := string(withoutPictures(in))
	if strings.Contains(out, data) || !strings.Contains(out, `"data_omitted_bytes":3000`) || !strings.Contains(out, "shot.png") || !strings.Contains(out, `"text":"{}"`) {
		t.Fatalf("got %s", out)
	}
	plain := json.RawMessage(`{"content":[{"type":"text","text":"an image of a cat"}]}`)
	if string(withoutPictures(plain)) != string(plain) {
		t.Fatal("text results must stay as they are")
	}
}
