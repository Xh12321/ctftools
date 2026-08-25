package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/Xh12321/ctftools/internal/eventhub"
	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/sandbox"
	"github.com/Xh12321/ctftools/internal/storage"
	"github.com/Xh12321/ctftools/internal/workspace"
)

// Sentinel errors matching observed daemon behaviour.
var (
	ErrTaskNotPausable   = errors.New("task is not pausable")
	ErrTaskNotResumable  = errors.New("task is not resumable")
	ErrTaskNotRetryable  = errors.New("task is not retryable")
	ErrTaskNotDeletable  = errors.New("task is not deletable")
	ErrSandboxNotClosable = errors.New("sandbox is not closable")
	ErrFlagNotReviewable = errors.New("flag is not reviewable")
	ErrPromptLocked      = errors.New("the task prompt cannot be changed while the agent is running")
	ErrAttachmentsLocked = errors.New("attachments are locked while the agent is running")
	ErrAlreadyRunning    = errors.New("task is already running")
	ErrTaskNotFound      = errors.New("task not found")
)

// Runner executes the agent loop for a task. Production will use Pi RPC;
// tests and Milestone 1/2 use FakeRunner.
type Runner interface {
	// Run executes until the context is cancelled or the run finishes.
	// Implementations must publish lifecycle events through the service callbacks
	// (Emit) and should be restart-safe when context is cancelled.
	Run(ctx context.Context, task platform.Task, emit EmitFunc) error
}

// EmitFunc appends and publishes a task event.
type EmitFunc func(source, eventType, turnID, toolCallID string, payload any) (platform.TaskEvent, error)

// Service owns task lifecycle orchestration on top of storage, workspace, sandbox and event hub.
type Service struct {
	store  *storage.Store
	hub    *eventhub.Hub
	runner Runner
	wsMgr  *workspace.Manager
	sbMgr  sandbox.Manager

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	running  map[string]struct{}
	findings map[string]map[string]platform.FlagFinding // taskID -> flagID -> finding
}

// NewService constructs an agent service with workspace and sandbox managers.
func NewService(store *storage.Store, hub *eventhub.Hub, runner Runner, wsMgr *workspace.Manager, sbMgr sandbox.Manager) *Service {
	if wsMgr == nil {
		tmpDir, _ := os.MkdirTemp("", "ctf-ws-*")
		wsMgr, _ = workspace.New(workspace.Options{BaseDir: tmpDir})
	}
	if sbMgr == nil {
		sbMgr = sandbox.NewMockManager()
	}
	if runner == nil {
		runner = NewFakeRunner(FakeRunnerOptions{Workspace: wsMgr})
	}

	return &Service{
		store:    store,
		hub:      hub,
		runner:   runner,
		wsMgr:    wsMgr,
		sbMgr:    sbMgr,
		cancels:  make(map[string]context.CancelFunc),
		running:  make(map[string]struct{}),
		findings: make(map[string]map[string]platform.FlagFinding),
	}
}

// CreateTask creates a task, initializes its workspace, and publishes creation events.
func (s *Service) CreateTask(ctx context.Context, in platform.CreateTask) (platform.Task, error) {
	task, err := s.store.CreateTask(ctx, in)
	if err != nil {
		return platform.Task{}, err
	}

	// Initialize disk workspace directory with standard layout.
	if err := s.wsMgr.InitWorkspace(task.ID); err != nil {
		return platform.Task{}, fmt.Errorf("init task workspace: %w", err)
	}

	if _, err := s.emit(ctx, task.ID, platform.SourceSystem, platform.EventWorkspaceInitialized, "", "", map[string]any{
		"path": s.wsMgr.WorkspacePath(task.ID),
	}); err != nil {
		return platform.Task{}, err
	}

	// Replay creation events to hub for any early subscriber.
	events, err := s.store.ListEvents(ctx, task.ID, 0, 100)
	if err == nil {
		s.hub.PublishAll(events)
	}
	return task, nil
}

// GetTask returns a task by id.
func (s *Service) GetTask(ctx context.Context, id string) (platform.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return platform.Task{}, fmt.Errorf("%w: %s", ErrTaskNotFound, id)
		}
		return platform.Task{}, err
	}
	return task, nil
}

// ListTasks returns root tasks.
func (s *Service) ListTasks(ctx context.Context) ([]platform.Task, error) {
	return s.store.ListTasks(ctx)
}

// ListEvents returns events after a sequence number.
func (s *Service) ListEvents(ctx context.Context, taskID string, after int64, limit int) ([]platform.TaskEvent, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.ListEvents(ctx, taskID, after, limit)
}

// emit persists and publishes one event.
func (s *Service) emit(ctx context.Context, taskID, source, eventType, turnID, toolCallID string, payload any) (platform.TaskEvent, error) {
	ev, err := s.store.AppendEvent(ctx, taskID, source, eventType, turnID, toolCallID, payload)
	if err != nil {
		return platform.TaskEvent{}, err
	}
	s.hub.Publish(ev)
	return ev, nil
}

// Start transitions queued/failed/cancelled → provisioning → running and
// provisions a sandbox adhering to category security policies.
func (s *Service) Start(ctx context.Context, taskID string) (platform.Task, error) {
	s.mu.Lock()
	if _, ok := s.running[taskID]; ok {
		s.mu.Unlock()
		return platform.Task{}, ErrAlreadyRunning
	}
	s.mu.Unlock()

	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if task.Status.IsActive() {
		return platform.Task{}, ErrAlreadyRunning
	}
	if !task.Status.CanStart() && task.Status != platform.StatusPaused {
		if !task.Status.CanRetry() && task.Status != platform.StatusQueued {
			return platform.Task{}, fmt.Errorf("%w (current status: %s)", ErrTaskNotRetryable, task.Status)
		}
	}

	// 1. Resolve sandbox policy for category
	policy := platform.DefaultSandboxPolicy(task.Category, task.Image)

	// Report capability degradation warning for pwn/reverse with SYS_PTRACE
	if policy.AllowPtrace {
		if _, err := s.emit(ctx, taskID, platform.SourceSandbox, platform.EventSandboxDegraded, "", "", map[string]any{
			"warning":  "SYS_PTRACE capability granted for binary debugging",
			"category": task.Category,
		}); err != nil {
			return platform.Task{}, err
		}
	}

	empty := ""
	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusProvisioning,
		From:        []platform.TaskStatus{platform.StatusQueued, platform.StatusFailed, platform.StatusCancelled, platform.StatusSettled, platform.StatusPaused},
		LastError:   &empty,
		EventType:   platform.EventTaskProvisioning,
		EventSource: platform.SourceSandbox,
		Payload: map[string]any{
			"status":        platform.StatusProvisioning,
			"image":         policy.Image,
			"cpuQuotaCores": policy.CPUQuotaCores,
			"memoryLimitMB": policy.MemoryLimitMB,
			"pidsLimit":     policy.PidsLimit,
			"capabilities":  policy.Capabilities,
			"allowNetwork":  policy.AllowNetwork,
		},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)

	// 2. Provision sandbox instance
	sbInst, err := s.sbMgr.Provision(ctx, sandbox.Config{
		TaskID:       task.ID,
		WorkspaceDir: s.wsMgr.WorkspacePath(task.ID),
		Policy:       policy,
	})
	if err != nil {
		return platform.Task{}, fmt.Errorf("provision sandbox: %w", err)
	}

	// 3. Start sandbox instance
	if err := s.sbMgr.Start(ctx, sbInst.ID); err != nil {
		return platform.Task{}, fmt.Errorf("start sandbox: %w", err)
	}

	containerID := sbInst.ID
	runtime := sbInst.Runtime
	task, ev, err = s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusRunning,
		From:        []platform.TaskStatus{platform.StatusProvisioning},
		Runtime:     &runtime,
		ContainerID: &containerID,
		EventType:   platform.EventSandboxStarted,
		EventSource: platform.SourceSandbox,
		Payload: map[string]any{
			"status":      platform.StatusRunning,
			"runtime":     runtime,
			"containerId": containerID,
			"image":       policy.Image,
		},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)

	if _, err := s.emit(ctx, taskID, platform.SourceAgent, platform.EventAgentStarted, "", "", map[string]any{
		"status": platform.StatusRunning,
	}); err != nil {
		return platform.Task{}, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[taskID] = cancel
	s.running[taskID] = struct{}{}
	s.mu.Unlock()

	go s.runLoop(runCtx, task)
	return task, nil
}

func (s *Service) runLoop(ctx context.Context, task platform.Task) {
	defer func() {
		s.mu.Lock()
		delete(s.running, task.ID)
		delete(s.cancels, task.ID)
		s.mu.Unlock()
	}()

	emit := func(source, eventType, turnID, toolCallID string, payload any) (platform.TaskEvent, error) {
		return s.emit(ctx, task.ID, source, eventType, turnID, toolCallID, payload)
	}

	err := s.runner.Run(ctx, task, emit)
	// If context cancelled, Pause/Abort already set status.
	if ctx.Err() != nil {
		return
	}

	// Re-read status; runner may have requested settle via flag accept path.
	cur, getErr := s.store.GetTask(context.Background(), task.ID)
	if getErr != nil {
		return
	}
	if cur.Status != platform.StatusRunning && cur.Status != platform.StatusDelegating {
		return
	}

	if err != nil {
		msg := err.Error()
		_, ev, uerr := s.store.ApplyStatusUpdate(context.Background(), task.ID, storage.StatusUpdate{
			To:          platform.StatusFailed,
			From:        []platform.TaskStatus{platform.StatusRunning, platform.StatusDelegating},
			LastError:   &msg,
			EventType:   platform.EventTaskFailed,
			EventSource: platform.SourceSystem,
			Payload:     map[string]any{"error": msg, "status": platform.StatusFailed},
		})
		if uerr == nil {
			s.hub.Publish(ev)
		}
		return
	}

	// Successful runner completion without explicit settle → settle.
	_, ev, uerr := s.store.ApplyStatusUpdate(context.Background(), task.ID, storage.StatusUpdate{
		To:          platform.StatusSettled,
		From:        []platform.TaskStatus{platform.StatusRunning, platform.StatusDelegating},
		EventType:   platform.EventTaskSettled,
		EventSource: platform.SourceSystem,
		Payload:     map[string]any{"status": platform.StatusSettled},
	})
	if uerr == nil {
		s.hub.Publish(ev)
	}
	_, _ = s.emit(context.Background(), task.ID, platform.SourceAgent, platform.EventAgentSettled, "", "", map[string]any{
		"status": platform.StatusSettled,
	})
}

// Pause stops the runner and marks the task paused.
func (s *Service) Pause(ctx context.Context, taskID string) (platform.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if !task.Status.CanPause() {
		return platform.Task{}, fmt.Errorf("%w (current status: %s)", ErrTaskNotPausable, task.Status)
	}

	s.mu.Lock()
	if cancel, ok := s.cancels[taskID]; ok {
		cancel()
	}
	s.mu.Unlock()

	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusPaused,
		From:        []platform.TaskStatus{platform.StatusRunning, platform.StatusDelegating, platform.StatusProvisioning},
		EventType:   platform.EventTaskPaused,
		EventSource: platform.SourceUser,
		Payload:     map[string]any{"status": platform.StatusPaused},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)
	return task, nil
}

// Resume continues a paused task.
func (s *Service) Resume(ctx context.Context, taskID string) (platform.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if !task.Status.CanResume() {
		return platform.Task{}, fmt.Errorf("%w (current status: %s)", ErrTaskNotResumable, task.Status)
	}

	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusRunning,
		From:        []platform.TaskStatus{platform.StatusPaused},
		EventType:   platform.EventTaskResumed,
		EventSource: platform.SourceUser,
		Payload:     map[string]any{"status": platform.StatusRunning},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)

	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[taskID] = cancel
	s.running[taskID] = struct{}{}
	s.mu.Unlock()
	go s.runLoop(runCtx, task)
	return task, nil
}

// Abort cancels a non-terminal task.
func (s *Service) Abort(ctx context.Context, taskID string) (platform.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if !task.Status.CanAbort() {
		return platform.Task{}, fmt.Errorf("%w (current status: %s)", storage.ErrInvalidStatus, task.Status)
	}

	s.mu.Lock()
	if cancel, ok := s.cancels[taskID]; ok {
		cancel()
	}
	s.mu.Unlock()

	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusCancelled,
		From:        []platform.TaskStatus{platform.StatusQueued, platform.StatusProvisioning, platform.StatusRunning, platform.StatusPaused, platform.StatusDelegating},
		EventType:   platform.EventTaskCancelled,
		EventSource: platform.SourceUser,
		Payload:     map[string]any{"status": platform.StatusCancelled, "reason": "user requested abort"},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)
	return task, nil
}

// Retry re-queues a finished/failed/paused task and starts it.
func (s *Service) Retry(ctx context.Context, taskID string) (platform.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if !task.Status.CanRetry() {
		return platform.Task{}, fmt.Errorf("%w (current status: %s)", ErrTaskNotRetryable, task.Status)
	}

	s.mu.Lock()
	if cancel, ok := s.cancels[taskID]; ok {
		cancel()
	}
	s.mu.Unlock()

	empty := ""
	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          platform.StatusQueued,
		From:        []platform.TaskStatus{platform.StatusFailed, platform.StatusCancelled, platform.StatusSettled, platform.StatusPaused},
		LastError:   &empty,
		EventType:   platform.EventTaskRetryRequested,
		EventSource: platform.SourceUser,
		Payload:     map[string]any{"status": platform.StatusQueued},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)
	return s.Start(ctx, taskID)
}

// CloseSandbox stops sandbox container and clears container metadata for a finished task.
func (s *Service) CloseSandbox(ctx context.Context, taskID string) (platform.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if !task.Status.CanCloseSandbox() {
		return platform.Task{}, fmt.Errorf("%w (current status: %s)", ErrSandboxNotClosable, task.Status)
	}

	if task.ContainerID != "" {
		_ = s.sbMgr.Stop(ctx, task.ContainerID)
	}

	empty := ""
	task, ev, err := s.store.ApplyStatusUpdate(ctx, taskID, storage.StatusUpdate{
		To:          task.Status,
		ContainerID: &empty,
		Runtime:     &empty,
		EventType:   platform.EventSandboxStopped,
		EventSource: platform.SourceSandbox,
		Payload:     map[string]any{"status": task.Status, "containerId": ""},
	})
	if err != nil {
		return platform.Task{}, err
	}
	s.hub.Publish(ev)
	return task, nil
}

// ReviewFlag records operator accept/reject on a candidate flag.
// Accepting pauses the task to avoid further model spend.
func (s *Service) ReviewFlag(ctx context.Context, taskID string, fb platform.FlagFeedback) (platform.Task, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return platform.Task{}, err
	}
	action, err := platform.ParseFlagReviewAction(string(fb.Action))
	if err != nil {
		return platform.Task{}, err
	}

	s.mu.Lock()
	findings := s.findings[taskID]
	finding, ok := findings[fb.FlagID]
	if !ok {
		for _, f := range findings {
			if fb.Value != "" && f.Value == fb.Value {
				finding = f
				ok = true
				break
			}
			if f.ID == fb.FlagID {
				finding = f
				ok = true
				break
			}
		}
	}
	s.mu.Unlock()
	if !ok {
		return platform.Task{}, fmt.Errorf("%w: unknown flag %q", ErrFlagNotReviewable, fb.FlagID)
	}
	if finding.Status == "accepted" || finding.Status == "rejected" {
		return platform.Task{}, fmt.Errorf("%w: already %s", ErrFlagNotReviewable, finding.Status)
	}

	status := "rejected"
	if action == platform.FlagAccept {
		status = "accepted"
		finding.Verified = true
	}
	finding.Status = status

	s.mu.Lock()
	if s.findings[taskID] == nil {
		s.findings[taskID] = make(map[string]platform.FlagFinding)
	}
	s.findings[taskID][finding.ID] = finding
	s.mu.Unlock()

	if _, err := s.emit(ctx, taskID, platform.SourceUser, platform.EventFlagReviewed, "", "", map[string]any{
		"flagId":   finding.ID,
		"value":    finding.Value,
		"action":   action,
		"status":   status,
		"note":     fb.Note,
		"verified": finding.Verified,
	}); err != nil {
		return platform.Task{}, err
	}

	if action == platform.FlagAccept {
		cur, err := s.GetTask(ctx, taskID)
		if err != nil {
			return platform.Task{}, err
		}
		if cur.Status.CanPause() {
			paused, perr := s.Pause(ctx, taskID)
			if perr == nil {
				return paused, nil
			}
			cur, err = s.GetTask(ctx, taskID)
			if err != nil {
				return platform.Task{}, err
			}
			if cur.Status.IsTerminal() || cur.Status == platform.StatusPaused {
				return cur, nil
			}
			return platform.Task{}, perr
		}
		return cur, nil
	}
	return s.GetTask(ctx, taskID)
}

// RegisterFinding records a flag candidate (used by FakeRunner / detector).
func (s *Service) RegisterFinding(taskID string, f platform.FlagFinding) {
	if f.ID == "" {
		f.ID = platform.NewID()
	}
	if f.Status == "" {
		f.Status = "pending"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findings[taskID] == nil {
		s.findings[taskID] = make(map[string]platform.FlagFinding)
	}
	s.findings[taskID][f.ID] = f
}

// ListFindings returns known flag candidates for a task.
func (s *Service) ListFindings(taskID string) []platform.FlagFinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]platform.FlagFinding, 0, len(s.findings[taskID]))
	for _, f := range s.findings[taskID] {
		out = append(out, f)
	}
	return out
}

// UpdatePrompt updates the prompt when not running.
func (s *Service) UpdatePrompt(ctx context.Context, taskID, prompt string) (platform.Task, error) {
	task, ev, err := s.store.UpdatePrompt(ctx, taskID, prompt)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return platform.Task{}, ErrPromptLocked
		}
		return platform.Task{}, err
	}
	s.hub.Publish(ev)
	return task, nil
}

// SaveAttachment saves an uploaded attachment stream into the task's workspace.
func (s *Service) SaveAttachment(ctx context.Context, taskID, filename string, r io.Reader, size int64) (platform.FileInfo, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return platform.FileInfo{}, err
	}
	if task.Status.IsActive() {
		return platform.FileInfo{}, ErrAttachmentsLocked
	}

	info, err := s.wsMgr.SaveAttachment(taskID, filename, r, size)
	if err != nil {
		return platform.FileInfo{}, err
	}

	if _, err := s.emit(ctx, taskID, platform.SourceUser, platform.EventAttachmentUploaded, "", "", map[string]any{
		"path": info.Path,
		"name": info.Name,
		"size": info.Size,
	}); err != nil {
		return platform.FileInfo{}, err
	}

	return info, nil
}

// ListFiles returns all files within a task workspace.
func (s *Service) ListFiles(ctx context.Context, taskID string) ([]platform.FileInfo, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.wsMgr.ListFiles(taskID)
}

// ReadFile reads a file from the task workspace.
func (s *Service) ReadFile(ctx context.Context, taskID, relPath string) ([]byte, platform.FileInfo, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return nil, platform.FileInfo{}, err
	}
	return s.wsMgr.ReadFile(taskID, relPath)
}

// GetWriteup retrieves the WRITEUP.md content.
func (s *Service) GetWriteup(ctx context.Context, taskID string) (platform.Writeup, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return platform.Writeup{}, err
	}
	return s.wsMgr.GetWriteup(taskID)
}

// SaveWriteup updates the task WRITEUP.md file and emits an event.
func (s *Service) SaveWriteup(ctx context.Context, taskID, content string) error {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return err
	}
	if err := s.wsMgr.SaveWriteup(taskID, content); err != nil {
		return err
	}
	_, _ = s.emit(ctx, taskID, platform.SourceUser, platform.EventWriteupUpdated, "", "", map[string]any{
		"length": len(content),
	})
	return nil
}

// ExportZip writes a zip archive of the task workspace to w.
func (s *Service) ExportZip(ctx context.Context, taskID string, w io.Writer) error {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return err
	}
	return s.wsMgr.ExportZip(taskID, w)
}

// Workspace returns the workspace manager.
func (s *Service) Workspace() *workspace.Manager { return s.wsMgr }

// Sandbox returns the sandbox manager.
func (s *Service) Sandbox() sandbox.Manager { return s.sbMgr }

// Hub returns the event hub (for API layer subscriptions).
func (s *Service) Hub() *eventhub.Hub { return s.hub }

// Store returns the underlying store.
func (s *Service) Store() *storage.Store { return s.store }

// IsRunning reports whether a background runner is active.
func (s *Service) IsRunning(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[taskID]
	return ok
}

// Shutdown cancels all runners.
func (s *Service) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cancel := range s.cancels {
		cancel()
		delete(s.cancels, id)
	}
}
