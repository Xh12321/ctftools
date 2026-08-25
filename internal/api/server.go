package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Xh12321/ctftools/internal/agent"
	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/storage"
	"github.com/Xh12321/ctftools/internal/workspace"
)

// Server is the local authenticated HTTP control plane.
type Server struct {
	svc   *agent.Service
	token string
	mux   *http.ServeMux
	http  *http.Server
}

// NewServer builds an API server. token may be empty only for tests.
func NewServer(svc *agent.Service, token string) *Server {
	s := &Server{
		svc:   svc,
		token: token,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /api/system", s.auth(s.handleSystem))
	s.mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	s.mux.HandleFunc("GET /api/model-usage", s.auth(s.handleModelUsage))
	s.mux.HandleFunc("GET /api/tasks", s.auth(s.handleListTasks))
	s.mux.HandleFunc("POST /api/tasks", s.auth(s.handleCreateTask))
	s.mux.HandleFunc("GET /api/tasks/{id}", s.auth(s.handleGetTask))
	s.mux.HandleFunc("GET /api/tasks/{id}/events", s.auth(s.handleListEvents))
	s.mux.HandleFunc("GET /api/tasks/{id}/subtasks", s.auth(s.handleListSubtasks))
	s.mux.HandleFunc("GET /api/tasks/{id}/prompt", s.auth(s.handleGetPrompt))
	s.mux.HandleFunc("PUT /api/tasks/{id}/prompt", s.auth(s.handlePutPrompt))
	s.mux.HandleFunc("POST /api/tasks/{id}/start", s.auth(s.handleStart))
	s.mux.HandleFunc("POST /api/tasks/{id}/abort", s.auth(s.handleAbort))
	s.mux.HandleFunc("POST /api/tasks/{id}/pause", s.auth(s.handlePause))
	s.mux.HandleFunc("POST /api/tasks/{id}/resume", s.auth(s.handleResume))
	s.mux.HandleFunc("POST /api/tasks/{id}/retry", s.auth(s.handleRetry))
	s.mux.HandleFunc("POST /api/tasks/{id}/close-sandbox", s.auth(s.handleCloseSandbox))
	s.mux.HandleFunc("POST /api/tasks/{id}/flag-feedback", s.auth(s.handleFlagFeedback))
	// Workspace & File endpoints (Milestone 2)
	s.mux.HandleFunc("GET /api/tasks/{id}/files", s.auth(s.handleListFiles))
	s.mux.HandleFunc("GET /api/tasks/{id}/file", s.auth(s.handleGetFile))
	s.mux.HandleFunc("GET /api/tasks/{id}/writeup", s.auth(s.handleGetWriteup))
	s.mux.HandleFunc("PUT /api/tasks/{id}/writeup", s.auth(s.handlePutWriteup))
	s.mux.HandleFunc("GET /api/tasks/{id}/download", s.auth(s.handleDownload))
	s.mux.HandleFunc("POST /api/tasks/{id}/attachments", s.auth(s.handleUploadAttachments))
	// SSE live events
	s.mux.HandleFunc("GET /api/tasks/{id}/stream", s.auth(s.handleStream))
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS for local desktop / browser tooling.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// ListenAndServe starts the HTTP server on addr (e.g. "127.0.0.1:0").
func (s *Server) ListenAndServe(addr string) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.http.Serve(ln) }()
	return ln.Addr(), nil
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := r.Header.Get("Authorization")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			got = strings.TrimPrefix(got, "Bearer ")
			got = strings.TrimSpace(got)
			if got != s.token {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	settings, err := s.svc.Store().GetSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := s.svc.ListTasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	active, queued := 0, 0
	for _, t := range tasks {
		if t.Status.IsActive() {
			active++
		}
		if t.Status == platform.StatusQueued {
			queued++
		}
	}

	policies := make(map[platform.Category]platform.SandboxPolicy)
	for _, cat := range platform.AllCategories() {
		policies[cat] = platform.DefaultSandboxPolicy(cat, "")
	}

	wsDir := ""
	if s.svc.Workspace() != nil {
		wsDir = s.svc.Workspace().BaseDir()
	}

	dockerAvail := false
	if s.svc.Sandbox() != nil {
		dockerAvail = s.svc.Sandbox().DockerAvailable()
	}

	status := platform.SystemStatus{
		Configured:      true,
		Mode:            "fake-agent",
		DockerAvailable: dockerAvail,
		WorkspaceDir:    wsDir,
		ActiveSandboxes: active,
		Settings:        settings,
		Queue: platform.SchedulerStatus{
			MaxConcurrentTasks: settings.MaxConcurrentTasks,
			ActiveTaskCount:    active,
			QueuedTaskCount:    queued,
		},
		SandboxPolicies: policies,
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.svc.Store().GetSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body platform.ExecutionSettings
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.Store().PutSettings(r.Context(), body); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleModelUsage(w http.ResponseWriter, r *http.Request) {
	summary, err := s.svc.Store().SummarizeModelUsage(r.Context(), time.Time{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.svc.ListTasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, platform.TaskList{Tasks: tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var body platform.CreateTask
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.svc.CreateTask(r.Context(), body)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.svc.GetTask(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.svc.ListEvents(r.Context(), id, after, limit)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, platform.EventList{Events: events})
}

func (s *Server) handleListSubtasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.svc.GetTask(r.Context(), id); err != nil {
		writeTaskErr(w, err)
		return
	}
	tasks, err := s.svc.Store().ListSubtasks(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, platform.TaskList{Tasks: tasks})
}

func (s *Server) handleGetPrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.svc.GetTask(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt": task.Prompt})
}

func (s *Server) handlePutPrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.svc.UpdatePrompt(r.Context(), id, body.Prompt)
	if err != nil {
		if errors.Is(err, agent.ErrPromptLocked) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt": task.Prompt})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.Start)
}
func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.Abort)
}
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.Pause)
}
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.Resume)
}
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.Retry)
}
func (s *Server) handleCloseSandbox(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, s.svc.CloseSandbox)
}

func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) (platform.Task, error)) {
	id := r.PathValue("id")
	task, err := fn(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleFlagFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		FlagID string `json:"flagId"`
		Action string `json:"action"`
		Note   string `json:"note"`
		Value  string `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	action, err := platform.ParseFlagReviewAction(body.Action)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.svc.ReviewFlag(r.Context(), id, platform.FlagFeedback{
		FlagID: body.FlagID,
		Action: action,
		Note:   body.Note,
		Value:  body.Value,
	})
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task":     task,
		"findings": s.svc.ListFindings(id),
	})
}

// handleListFiles returns all files in task workspace.
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	files, err := s.svc.ListFiles(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, platform.FileList{Files: files})
}

// handleGetFile reads a file from task workspace.
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeErr(w, http.StatusBadRequest, "query parameter 'path' is required")
		return
	}

	data, info, err := s.svc.ReadFile(r.Context(), id, path)
	if err != nil {
		writeTaskErr(w, err)
		return
	}

	if r.URL.Query().Get("raw") == "true" {
		w.Header().Set("Content-Type", info.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	isBin := bytes.IndexByte(data, 0) != -1
	text := ""
	if !isBin {
		text = string(data)
	}

	writeJSON(w, http.StatusOK, platform.FileContent{
		Info:        info,
		ContentText: text,
		IsBinary:    isBin,
		Truncated:   false,
	})
}

// handleGetWriteup returns WRITEUP.md content.
func (s *Server) handleGetWriteup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wu, err := s.svc.GetWriteup(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wu)
}

// handlePutWriteup saves WRITEUP.md content.
func (s *Server) handlePutWriteup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.SaveWriteup(r.Context(), id, body.Content); err != nil {
		writeTaskErr(w, err)
		return
	}
	wu, err := s.svc.GetWriteup(r.Context(), id)
	if err != nil {
		writeTaskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wu)
}

// handleDownload downloads a single file or full workspace zip.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := r.URL.Query().Get("path")

	if strings.TrimSpace(path) == "" {
		// Download entire workspace as zip
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"task-%s-workspace.zip\"", id[:8]))
		if err := s.svc.ExportZip(r.Context(), id, w); err != nil {
			// Headers may already be sent if error occurs during streaming
			return
		}
		return
	}

	// Download specific file
	data, info, err := s.svc.ReadFile(r.Context(), id, path)
	if err != nil {
		writeTaskErr(w, err)
		return
	}

	filename := filepath.Base(info.Path)
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleUploadAttachments receives multipart/form-data files and saves to workspace attachments.
func (s *Server) handleUploadAttachments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Limit to 50MB
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("parse multipart form: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	if r.MultipartForm == nil || len(r.MultipartForm.File) == 0 {
		writeErr(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	var uploaded []platform.FileInfo
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fh := range fileHeaders {
			file, err := fh.Open()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Sprintf("open uploaded file: %v", err))
				return
			}
			info, err := s.svc.SaveAttachment(r.Context(), id, fh.Filename, file, fh.Size)
			_ = file.Close()
			if err != nil {
				writeTaskErr(w, err)
				return
			}
			uploaded = append(uploaded, info)
		}
	}

	writeJSON(w, http.StatusCreated, platform.FileList{Files: uploaded})
}

// handleStream is a simple SSE endpoint for live events.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.svc.GetTask(r.Context(), id); err != nil {
		writeTaskErr(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Catch-up from storage.
	events, err := s.svc.ListEvents(r.Context(), id, after, 1000)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	last := after
	for _, ev := range events {
		if err := writeSSE(w, ev); err != nil {
			return
		}
		last = ev.Sequence
		flusher.Flush()
	}

	ch, cancel := s.svc.Hub().Subscribe(id, last, 128)
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, ev); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, ev platform.TaskEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: task\ndata: %s\n\n", ev.Sequence, b)
	return err
}

func writeTaskErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrTaskNotFound), errors.Is(err, storage.ErrNotFound), errors.Is(err, workspace.ErrFileNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agent.ErrAlreadyRunning),
		errors.Is(err, agent.ErrTaskNotPausable),
		errors.Is(err, agent.ErrTaskNotResumable),
		errors.Is(err, agent.ErrTaskNotRetryable),
		errors.Is(err, agent.ErrSandboxNotClosable),
		errors.Is(err, agent.ErrFlagNotReviewable),
		errors.Is(err, agent.ErrPromptLocked),
		errors.Is(err, agent.ErrAttachmentsLocked),
		errors.Is(err, storage.ErrConflict),
		errors.Is(err, storage.ErrInvalidStatus):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, storage.ErrInvalidInput),
		errors.Is(err, workspace.ErrInvalidPath),
		errors.Is(err, workspace.ErrPathTraversal),
		errors.Is(err, workspace.ErrSymlinkEscape),
		errors.Is(err, workspace.ErrInvalidFilename),
		errors.Is(err, workspace.ErrFileTooLarge):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(data, dst)
}
