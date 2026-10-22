package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssistantOwnershipAndMemory(t *testing.T) {
	s, h, root := testServer(t)
	a, u := person(t, s, h, "assistant-a@test.local", "admin", false)
	b, _ := person(t, s, h, "assistant-b@test.local", "admin", false)
	var m memory
	a.want("POST", "/api/v1/memory", map[string]any{"label": "Car", "value": "Use my Golf", "enabled": true}, 200, &m)
	b.want("PUT", fmt.Sprintf("/api/v1/memory/%d", m.ID), map[string]any{"label": "x", "value": "x"}, 404, nil)
	b.want("DELETE", fmt.Sprintf("/api/v1/memory/%d", m.ID), nil, 404, nil)
	var others []memory
	b.want("GET", "/api/v1/memory", nil, 200, &others)
	if len(others) != 0 {
		t.Fatal("memory leaked")
	}
	if !strings.Contains(s.memoryContext(context.Background(), u.ID, false), "Golf") || strings.Contains(s.memoryContext(context.Background(), u.ID, true), "Golf") {
		t.Fatal("memory toggle ignored")
	}
	th := thread(t, s, u.ID, root)
	a.want("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"no_memory": true}, 200, nil)
	got, _ := s.store.GetThread(context.Background(), u.ID, th.ID)
	if !got.NoMemory {
		t.Fatal("thread toggle not persisted")
	}
	id := s.startAppCall(context.Background(), u.ID, th.ID, "pocket_notes_get", json.RawMessage(`{"id":7}`), false)
	s.finishAppCall(id, "done", toolResult(map[string]any{"private": "only Alice"}, false), time.Second)
	b.want("GET", "/api/v1/app-calls?thread="+th.ID, nil, 404, nil)
	b.want("GET", "/api/v1/app-calls/"+id, nil, 404, nil)
	a.want("GET", "/api/v1/app-calls/"+id, nil, 200, nil)
	var calls []appCall
	b.want("GET", "/api/v1/app-calls", nil, 200, &calls)
	if len(calls) > 0 {
		t.Fatal("call leaked")
	}
	a.want("GET", "/api/v1/app-calls", nil, 200, &calls)
	if len(calls) != 1 || calls[0].Duration != 1000 {
		t.Fatalf("missing evidence: %#v", calls)
	}
	a.want("DELETE", fmt.Sprintf("/api/v1/memory/%d", m.ID), nil, 204, nil)
	if strings.Contains(s.memoryContext(context.Background(), u.ID, false), "Golf") {
		t.Fatal("forgotten memory injected")
	}
}
func TestWorkflowOwnershipAndValidation(t *testing.T) {
	s, h, _ := testServer(t)
	a, u := person(t, s, h, "flow-a@test.local", "member", false)
	b, _ := person(t, s, h, "flow-b@test.local", "admin", false)
	sealed, _ := s.seal("pcma_test")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	var x workflow
	a.want("POST", "/api/v1/workflows", map[string]any{"name": "Daily", "prompt": "Brief me", "trigger_kind": "schedule", "spec": "daily 08:00"}, 200, &x)
	if x.ID == 0 || x.Next == "" {
		t.Fatal("schedule missing")
	}
	b.want("POST", fmt.Sprintf("/api/v1/workflows/%d/run", x.ID), nil, 404, nil)
	b.want("DELETE", fmt.Sprintf("/api/v1/workflows/%d", x.ID), nil, 404, nil)
	a.want("POST", "/api/v1/workflows", map[string]any{"name": "Bad", "prompt": "x", "trigger_kind": "schedule", "spec": "not a schedule"}, 400, nil)
	a.want("POST", "/api/v1/workflows", map[string]any{"name": "Bad", "prompt": "x", "model": "shell"}, 400, nil)
	var flows []workflow
	b.want("GET", "/api/v1/workflows", nil, 200, &flows)
	if len(flows) > 0 {
		t.Fatal("workflow leaked")
	}
	for i := 0; i < 2; i++ {
		s.store.DB.Exec(`INSERT OR IGNORE INTO workflow_runs(workflow_id,trigger_key) VALUES(?,?)`, x.ID, "same-event")
	}
	var n int
	s.store.DB.QueryRow(`SELECT count(*) FROM workflow_runs`).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate event accepted")
	}
}
func TestDocumentUploadAndOwnership(t *testing.T) {
	s, h, _ := testServer(t)
	a, u := person(t, s, h, "file-a@test.local", "admin", false)
	b, _ := person(t, s, h, "file-b@test.local", "admin", false)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, _ := mw.CreateFormFile("file", "receipt.csv")
	f.Write([]byte("item,amount\nCoffee,3.50\n"))
	mw.Close()
	code, raw := a.do("POST", "/api/v1/uploads", mw.FormDataContentType(), body.Bytes())
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	var out []uploadOut
	json.Unmarshal(raw, &out)
	up, err := s.store.GetUpload(context.Background(), u.ID, out[0].ID)
	if err != nil || !strings.Contains(up.ExtractedText, "Coffee") {
		t.Fatal("text not extracted")
	}
	if strings.Contains(string(raw), "Coffee") {
		t.Fatal("extracted text should not be in upload metadata")
	}
	b.want("GET", "/api/v1/uploads/"+up.ID, nil, 404, nil)
}
func TestCostMatchesAreConservative(t *testing.T) {
	g := map[string]any{"id": float64(1), "cost": float64(1250), "currency": "EUR", "date": "2026-10-22", "money_tx_id": float64(4), "money_user_id": float64(8)}
	m := map[string]any{"id": float64(4), "user_id": float64(8), "amount": float64(1250), "currency": "EUR", "date": "2026-10-22", "kind": "expense"}
	if costMatch(g, m) != "confirmed" {
		t.Fatal("verified link missed")
	}
	m["id"] = float64(5)
	if costMatch(g, m) != "possible" {
		t.Fatal("unlinked equal amounts must remain possible")
	}
	m["currency"] = "USD"
	if costMatch(g, m) != "" {
		t.Fatal("mixed currencies matched")
	}
	m["currency"] = "EUR"
	m["amount"] = float64(1249)
	if costMatch(g, m) != "" {
		t.Fatal("unequal amounts matched")
	}
}
func TestUndoRequiresCurrentVersion(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "undo@test.local", "admin", false)
	th := thread(t, s, u.ID, root)
	sealed, _ := s.seal("pcma_test")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	version := "after"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": toolResult(map[string]any{"result": map[string]any{"id": 1, "etag": version}}, false)})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	id := s.startAppCall(context.Background(), u.ID, th.ID, "pocket_notes_update", json.RawMessage(`{"id":1,"data":{"title":"New","etag":"old"}}`), true)
	before := toolResult(map[string]any{"result": map[string]any{"title": "Original", "etag": "old"}}, false)
	after := toolResult(map[string]any{"result": map[string]any{"title": "New", "etag": "after"}}, false)
	s.store.DB.Exec(`UPDATE app_calls SET before_state=?,result=?,status='done' WHERE id=?`, string(before), string(after), id)
	args, err := s.undoNoteArgs(context.Background(), u.ID, id)
	if err != nil || !strings.Contains(string(args), "Original") || !strings.Contains(string(args), "after") {
		t.Fatalf("restore failed: %s %v", args, err)
	}
	version = "edited"
	if _, err = s.undoNoteArgs(context.Background(), u.ID, id); err == nil {
		t.Fatal("undo would overwrite a newer edit")
	}
	if _, err = s.undoNoteArgs(context.Background(), u.ID+100, id); err == nil {
		t.Fatal("cross-owner undo allowed")
	}
}
func TestCatalogIsScopedAndDiscoverable(t *testing.T) {
	all := []askTool{}
	for i := 0; i < 80; i++ {
		all = append(all, askTool{Name: fmt.Sprintf("pocket_notes_op%d", i), Description: "Read notes"})
	}
	v := scopedCatalog(all, "notes").(map[string]any)["tools"].([]askTool)
	if len(v) > 20 {
		t.Fatal("catalog not bounded")
	}
	found := false
	for _, x := range v {
		found = found || x.Name == "pocket_find_tools"
	}
	if !found {
		t.Fatal("no way to find tools on later turns")
	}
}

func TestReadRetriesBusyButWriteDoesNot(t *testing.T) {
	s, _, _ := testServer(t)
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(429)
			return
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	if _, err := s.askReadRPC(context.Background(), "test", "tools/list", map[string]any{}); err != nil || attempts != 2 {
		t.Fatalf("read retry: %v, attempts %d", err, attempts)
	}
	attempts = 0
	if _, err := s.askCoreRPC(context.Background(), "test", "tools/call", map[string]any{"name": "pocket_notes_create"}); err != errPocketBusy || attempts != 1 {
		t.Fatal("write replayed after busy response")
	}
}

func TestWorkflowEventBaselineAndNoReplay(t *testing.T) {
	s, h, _ := testServer(t)
	a, u := person(t, s, h, "events@test.local", "member", false)
	sealed, _ := s.seal("pcma_events")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	ids := []int{1}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&rpc)
		var result any
		if rpc.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "pocket_documents_list", "annotations": map[string]bool{"readOnlyHint": true}}}}
		} else {
			rows := []any{}
			for _, id := range ids {
				rows = append(rows, map[string]any{"id": id, "title": "fixture"})
			}
			result = toolResult(map[string]any{"result": map[string]any{"documents": rows}}, false)
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	var x workflow
	a.want("POST", "/api/v1/workflows", map[string]any{"name": "Event fixture", "prompt": "Read only", "trigger_kind": "documents", "notify": false}, 200, &x)
	poll := func() {
		s.store.DB.Exec(`UPDATE workflows SET next_run_at='' WHERE id=?`, x.ID)
		s.pollWorkflows(context.Background())
	}
	count := func() int {
		var n int
		s.store.DB.QueryRow(`SELECT count(*) FROM workflow_runs WHERE workflow_id=?`, x.ID).Scan(&n)
		return n
	}
	poll()
	if count() != 0 {
		t.Fatal("existing records triggered a workflow")
	}
	ids = []int{1, 2}
	poll()
	if count() != 1 {
		t.Fatal("new item did not trigger exactly one occurrence")
	}
	poll()
	if count() != 1 {
		t.Fatal("same item replayed")
	}
	var seen string
	s.store.DB.QueryRow(`SELECT seen FROM workflows WHERE id=?`, x.ID).Scan(&seen)
	if !strings.Contains(seen, "2") {
		t.Fatal("event cursor not saved")
	}
}

func TestBackgroundUpdatesPreserveMemoryToggle(t *testing.T) {
	s, h, root := testServer(t)
	a, u := person(t, s, h, "toggle@test.local", "admin", false)
	stale := thread(t, s, u.ID, root)
	a.want("PATCH", "/api/v1/threads/"+stale.ID, map[string]any{"no_memory": true}, 200, nil)
	stale.Title = "Background update"
	if err := s.store.UpdateThread(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	current, _ := s.store.GetThread(context.Background(), u.ID, stale.ID)
	if !current.NoMemory {
		t.Fatal("background update reset memory choice")
	}
}

func TestTaskTriggersUseCreationEvents(t *testing.T) {
	s, h, _ := testServer(t)
	_, u := person(t, s, h, "task-events@test.local", "member", false)
	sealed, _ := s.seal("pcma_events")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&rpc)
		var result any
		if rpc.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "pocket_tasks_activity", "annotations": map[string]bool{"readOnlyHint": true}}}}
		} else {
			result = toolResult(map[string]any{"result": []any{map[string]any{"id": 10, "task_id": 7, "kind": "updated"}, map[string]any{"id": 11, "task_id": 8, "kind": "created"}}}, false)
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	ids, err := s.workflowEventIDs(context.Background(), &workflow{UserID: u.ID, Trigger: "tasks"})
	if err != nil || len(ids) != 1 || ids[0] != "8" {
		t.Fatalf("wrong task event IDs: %v %v", ids, err)
	}
}

func TestSharedSearchCollectionsAndMetadataBeforeLimit(t *testing.T) {
	s, h, _ := testServer(t)
	_, u := person(t, s, h, "search-collections@test.local", "member", false)
	sealed, _ := s.seal("pcma_search")
	s.store.DB.Exec(`INSERT INTO ask_connections VALUES(?,?)`, u.ID, sealed)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []any{}
		for i := 0; i < 25; i++ {
			rows = append(rows, map[string]any{"title": "Other"})
		}
		rows = append(rows, map[string]any{"title": "Needle vehicle"})
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": 1, "result": toolResult(map[string]any{"result": rows}, false)})
	}))
	defer upstream.Close()
	s.core = core.New(upstream.URL, core.Options{})
	cat := map[string]askTool{}
	for _, name := range []string{"pocket_kitchen_recipes", "pocket_kitchen_pantry", "pocket_garage_vehicles"} {
		var tool askTool
		json.Unmarshal([]byte(`{"name":"`+name+`","annotations":{"readOnlyHint":true},"inputSchema":{"properties":{"query":{"properties":{"q":{"type":"string"}}}}}}`), &tool)
		if name == "pocket_garage_vehicles" {
			tool.InputSchema = json.RawMessage(`{}`)
		}
		cat[name] = tool
	}
	result := s.appSearch(context.Background(), u.ID, "", cat, searchInput{Query: "Needle"}).(map[string]any)
	sections := result["sections"].([]appSection)
	if len(sections) != 3 {
		t.Fatalf("missing collection: %v", sections)
	}
	for _, section := range sections {
		if section.Error != "" {
			t.Fatal(section.Error)
		}
		if section.Tool == "pocket_garage_vehicles" {
			rows := section.Data.(map[string]any)["result"].([]any)
			if len(rows) != 1 || rows[0].(map[string]any)["title"] != "Needle vehicle" {
				t.Fatalf("match beyond display limit lost: %v", rows)
			}
		}
	}
}
