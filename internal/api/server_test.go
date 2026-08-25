package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xh12321/ctftools/internal/agent"
	"github.com/Xh12321/ctftools/internal/api"
	"github.com/Xh12321/ctftools/internal/eventhub"
	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/sandbox"
	"github.com/Xh12321/ctftools/internal/storage"
	"github.com/Xh12321/ctftools/internal/workspace"
)

func startTestAPI(t *testing.T) (baseURL, token string, svc *agent.Service) {
	t.Helper()
	tempDir := t.TempDir()
	store, err := storage.Open(filepath.Join(tempDir, "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hub := eventhub.New()
	t.Cleanup(hub.Close)

	wsMgr, err := workspace.New(workspace.Options{BaseDir: filepath.Join(tempDir, "workspaces")})
	if err != nil {
		t.Fatal(err)
	}
	sbMgr := sandbox.NewMockManager()

	var s *agent.Service
	runner := agent.NewFakeRunner(agent.FakeRunnerOptions{
		StepDelay: 5 * time.Millisecond,
		FlagValue: "flag{api-ok}",
		Workspace: wsMgr,
		OnFinding: func(taskID string, f platform.FlagFinding) {
			if s != nil {
				s.RegisterFinding(taskID, f)
			}
		},
	})
	s = agent.NewService(store, hub, runner, wsMgr, sbMgr)
	t.Cleanup(s.Shutdown)

	token = "test-token"
	srv := api.NewServer(s, token)
	addr, err := srv.ListenAndServe("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + addr.String(), token, s
}

func doJSON(t *testing.T, method, url, token string, body any, wantStatus int) map[string]any {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status=%d body=%s", method, url, resp.StatusCode, data)
	}
	if len(data) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		var generic any
		if err2 := json.Unmarshal(data, &generic); err2 != nil {
			t.Fatalf("json: %v body=%s", err, data)
		}
		return map[string]any{"_raw": generic}
	}
	return out
}

func TestHealthAndAuth(t *testing.T) {
	base, token, _ := startTestAPI(t)
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	// unauthorized
	req, _ := http.NewRequest(http.MethodGet, base+"/api/tasks", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
	_ = token
}

func TestTaskAPIVerticalSlice(t *testing.T) {
	base, token, svc := startTestAPI(t)

	created := doJSON(t, http.MethodPost, base+"/api/tasks", token, map[string]any{
		"title":       "API slice",
		"category":    "web",
		"description": "d",
		"prompt":      "go",
		"flagFormat":  "flag{...}",
	}, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created=%v", created)
	}
	if created["status"] != "queued" {
		t.Fatalf("status=%v", created["status"])
	}

	// List
	listed := doJSON(t, http.MethodGet, base+"/api/tasks", token, nil, http.StatusOK)
	if listed["tasks"] == nil {
		t.Fatal("no tasks key")
	}

	// Start
	started := doJSON(t, http.MethodPost, base+"/api/tasks/"+id+"/start", token, nil, http.StatusOK)
	if started["status"] != "running" {
		t.Fatalf("started=%v", started)
	}

	// Poll events until flag candidate
	var flagID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && flagID == "" {
		evBody := doJSON(t, http.MethodGet, base+"/api/tasks/"+id+"/events?after=0", token, nil, http.StatusOK)
		raw, _ := json.Marshal(evBody["events"])
		var events []platform.TaskEvent
		_ = json.Unmarshal(raw, &events)
		for _, e := range events {
			if e.Type == platform.EventFlagCandidate {
				findings := svc.ListFindings(id)
				if len(findings) > 0 {
					flagID = findings[0].ID
				}
			}
		}
		if flagID == "" {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if flagID == "" {
		t.Fatal("no flag candidate")
	}

	// after catch-up: request with high after returns empty
	max := int64(0)
	{
		evBody := doJSON(t, http.MethodGet, base+"/api/tasks/"+id+"/events?after=0", token, nil, http.StatusOK)
		raw, _ := json.Marshal(evBody["events"])
		var events []platform.TaskEvent
		_ = json.Unmarshal(raw, &events)
		for _, e := range events {
			if e.Sequence > max {
				max = e.Sequence
			}
		}
	}
	empty := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/events?after=%d", base, id, max), token, nil, http.StatusOK)
	rawEmpty, _ := json.Marshal(empty["events"])
	var rest []any
	_ = json.Unmarshal(rawEmpty, &rest)
	if len(rest) != 0 {
		t.Fatalf("expected empty catch-up, got %v", rest)
	}

	// Flag feedback accept
	fb := doJSON(t, http.MethodPost, base+"/api/tasks/"+id+"/flag-feedback", token, map[string]any{
		"flagId": flagID,
		"action": "accept",
		"note":   "lgtm",
	}, http.StatusOK)
	if fb["task"] == nil {
		t.Fatalf("fb=%v", fb)
	}

	// System endpoint
	sys := doJSON(t, http.MethodGet, base+"/api/system", token, nil, http.StatusOK)
	if sys["mode"] != "fake-agent" {
		t.Fatalf("sys=%v", sys)
	}
	if sys["sandboxPolicies"] == nil {
		t.Fatalf("expected sandboxPolicies in system status: %v", sys)
	}
}

func TestFilesAndAttachmentsAPI(t *testing.T) {
	base, token, svc := startTestAPI(t)

	created := doJSON(t, http.MethodPost, base+"/api/tasks", token, map[string]any{
		"title":    "Attachment and File Challenge",
		"category": "pwn",
		"prompt":   "solve pwn",
	}, http.StatusCreated)
	taskID := created["id"].(string)

	// 1. Upload attachment via multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "vuln.elf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("\x7fELFfakeexecutablebinary"))
	_ = writer.Close()

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/tasks/%s/attachments", base, taskID), &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 for attachment upload, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. List files
	fileListResp := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/files", base, taskID), token, nil, http.StatusOK)
	filesRaw, _ := json.Marshal(fileListResp["files"])
	var files []platform.FileInfo
	_ = json.Unmarshal(filesRaw, &files)

	foundVuln := false
	for _, f := range files {
		if f.Path == "attachments/vuln.elf" {
			foundVuln = true
		}
	}
	if !foundVuln {
		t.Fatalf("uploaded file not found in file list: %+v", files)
	}

	// 3. Read specific file via GET /api/tasks/{id}/file?path=...
	fileContentResp := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/file?path=attachments/vuln.elf", base, taskID), token, nil, http.StatusOK)
	infoRaw, _ := json.Marshal(fileContentResp["info"])
	var fileInfo platform.FileInfo
	_ = json.Unmarshal(infoRaw, &fileInfo)
	if fileInfo.Path != "attachments/vuln.elf" {
		t.Fatalf("unexpected file info: %+v", fileContentResp)
	}

	// 4. Writeup endpoints
	wuResp := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/writeup", base, taskID), token, nil, http.StatusOK)
	if wuResp["exists"] != true {
		t.Fatalf("expected initial writeup to exist: %v", wuResp)
	}

	newWU := "# Solution\n\nPWNED!"
	putWU := doJSON(t, http.MethodPut, fmt.Sprintf("%s/api/tasks/%s/writeup", base, taskID), token, map[string]any{
		"content": newWU,
	}, http.StatusOK)
	if putWU["content"] != newWU {
		t.Fatalf("unexpected writeup put response: %v", putWU)
	}

	// 5. Download endpoints
	// 5a. Download single file
	dlReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/download?path=attachments/vuln.elf", base, taskID), nil)
	dlReq.Header.Set("Authorization", "Bearer "+token)
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatal(err)
	}
	dlData, _ := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK || string(dlData) != "\x7fELFfakeexecutablebinary" {
		t.Fatalf("download single file mismatch: status=%d data=%q", dlResp.StatusCode, dlData)
	}

	// 5b. Download full workspace as ZIP
	zipReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/tasks/%s/download", base, taskID), nil)
	zipReq.Header.Set("Authorization", "Bearer "+token)
	zipResp, err := http.DefaultClient.Do(zipReq)
	if err != nil {
		t.Fatal(err)
	}
	zipData, _ := io.ReadAll(zipResp.Body)
	zipResp.Body.Close()
	if zipResp.StatusCode != http.StatusOK {
		t.Fatalf("download zip status=%d", zipResp.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) == 0 {
		t.Fatal("empty zip archive")
	}

	_ = svc
}
