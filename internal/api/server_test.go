package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xh12321/ctftools/internal/agent"
	"github.com/Xh12321/ctftools/internal/api"
	"github.com/Xh12321/ctftools/internal/eventhub"
	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/storage"
)

func startTestAPI(t *testing.T) (baseURL, token string, svc *agent.Service) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hub := eventhub.New()
	t.Cleanup(hub.Close)

	var s *agent.Service
	runner := agent.NewFakeRunner(agent.FakeRunnerOptions{
		StepDelay: 5 * time.Millisecond,
		FlagValue: "flag{api-ok}",
		OnFinding: func(taskID string, f platform.FlagFinding) {
			if s != nil {
				s.RegisterFinding(taskID, f)
			}
		},
	})
	s = agent.NewService(store, hub, runner)
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
		// maybe list wrapper
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
		"title":      "API slice",
		"category":   "web",
		"description": "d",
		"prompt":     "go",
		"flagFormat": "flag{...}",
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
}
