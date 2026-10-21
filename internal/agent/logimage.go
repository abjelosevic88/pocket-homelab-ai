package agent

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// ErrNoImage means the session file has no such image.
var ErrNoImage = errors.New("no such image in the session")

type imageBlock struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// LogImage returns image n of a timeline item from a Claude Code session
// file: for "u-<uuid>" the images of that message (pasted with a prompt),
// otherwise key is a tool_use id and the images are in its result (Read of
// a picture). The CLI keeps them there as base64, so Pocket AI stores no copy.
func LogImage(path, key string, n int) ([]byte, string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer fh.Close()
	uuid, isMsg := strings.CutPrefix(key, "u-")
	needle := []byte(`"tool_use_id":"` + key + `"`)
	if isMsg {
		needle = []byte(`"uuid":"` + uuid + `"`)
	}
	r := bufio.NewReaderSize(fh, 1<<20)
	for {
		line, rerr := r.ReadBytes('\n')
		if bytes.Contains(line, needle) {
			if blocks := lineImages(line, isMsg, uuid, key); len(blocks) > 0 {
				if n < 0 || n >= len(blocks) {
					return nil, "", ErrNoImage
				}
				b := blocks[n]
				if b.Source.Type != "base64" {
					return nil, "", ErrNoImage
				}
				data, err := base64.StdEncoding.DecodeString(b.Source.Data)
				if err != nil {
					return nil, "", err
				}
				return data, b.Source.MediaType, nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil, "", ErrNoImage
			}
			return nil, "", rerr
		}
	}
}

func lineImages(line []byte, isMsg bool, uuid, toolID string) []imageBlock {
	var m struct {
		UUID    string `json:"uuid"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Attachment struct {
			Prompt json.RawMessage `json:"prompt"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	images := func(raw json.RawMessage) []imageBlock {
		var all, out []imageBlock
		_ = json.Unmarshal(raw, &all)
		for _, b := range all {
			if b.Type == "image" {
				out = append(out, b)
			}
		}
		return out
	}
	if isMsg {
		if m.UUID != uuid {
			return nil
		}
		if len(m.Attachment.Prompt) > 0 {
			return images(m.Attachment.Prompt)
		}
		return images(m.Message.Content)
	}
	var results []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(m.Message.Content, &results) != nil {
		return nil
	}
	for _, b := range results {
		if b.Type == "tool_result" && b.ToolUseID == toolID {
			return images(b.Content)
		}
	}
	return nil
}
