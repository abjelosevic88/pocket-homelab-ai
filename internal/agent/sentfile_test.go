package agent

import (
	"encoding/json"
	"testing"
)

func TestSendUserFileIsAFileCard(t *testing.T) {
	tool := claudeTool("SendUserFile", json.RawMessage(`{"files":["out/report.html"],"caption":"The decisions page","status":"normal","display":"render"}`))
	if tool.Kind != "file" || tool.Title != "The decisions page" || tool.Display != "render" {
		t.Fatalf("tool: %+v", tool)
	}
	if len(tool.Files) != 1 || tool.Files[0].Name != "report.html" || tool.Files[0].Path != "out/report.html" {
		t.Fatalf("files: %+v", tool.Files)
	}
	// a single path instead of a list, no caption
	tool = claudeTool("SendUserFile", json.RawMessage(`{"files":"/tmp/a.png","status":"proactive"}`))
	if len(tool.Files) != 1 || tool.Title != "Sent a.png" {
		t.Fatalf("one path: %+v", tool)
	}
}

func TestSendUserFileResultResolvesPaths(t *testing.T) {
	got := sentFilesResult(json.RawMessage(`{"caption":"x","attachments":[{"path":"/home/me/out/report.html","size":61132,"isImage":false,"media_type":"text/html","pathValidated":true},{"path":"/home/me/shot.png","size":10,"isImage":true,"media_type":"image/png","upload_error":"too big for claude.ai"}]}`))
	if len(got) != 2 || got[0].Path != "/home/me/out/report.html" || got[0].Size != 61132 || got[0].Mime != "text/html" || !got[1].Image {
		t.Fatalf("resolved: %+v", got)
	}
	if sentFilesResult(json.RawMessage(`{"filePath":"x"}`)) != nil || sentFilesResult(nil) != nil {
		t.Fatal("not a SendUserFile result")
	}
}

func TestSendUserFileFromTheSessionFile(t *testing.T) {
	l := &ClaudeLog{}
	ops := l.Feed([]byte(`{"type":"assistant","uuid":"a","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"SendUserFile","input":{"files":["r.html"],"status":"normal"}}]}}`))
	if len(ops) != 1 || ops[0].Item == nil || ops[0].Item.Tool.Kind != "file" {
		t.Fatalf("tool_use: %+v", ops)
	}
	tool := ops[0].Item.Tool
	ops = l.Feed([]byte(`{"type":"user","uuid":"b","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"1 file delivered to user."}]},"toolUseResult":{"attachments":[{"path":"/w/r.html","size":15,"isImage":false,"media_type":"text/html"}]}}`))
	if len(ops) != 1 || ops[0].Result == nil {
		t.Fatalf("tool_result: %+v", ops)
	}
	ops[0].Result.Apply(tool)
	if tool.Status != "done" || len(tool.Files) != 1 || tool.Files[0].Path != "/w/r.html" || tool.Files[0].Size != 15 {
		t.Fatalf("applied: %+v", tool)
	}
}
