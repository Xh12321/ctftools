package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/storage"
)

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndGetTask(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, platform.CreateTask{
		Title:       "demo",
		Category:    platform.CategoryWeb,
		Description: "desc",
		Prompt:      "solve it",
		FlagFormat:  "flag{...}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != platform.StatusQueued {
		t.Fatalf("status=%s", task.Status)
	}
	if task.Image == "" {
		t.Fatal("expected default image")
	}

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "demo" {
		t.Fatalf("title=%s", got.Title)
	}

	events, err := s.ListEvents(ctx, task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected created+queued events, got %d", len(events))
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("sequences: %d %d", events[0].Sequence, events[1].Sequence)
	}
	// after filter
	more, err := s.ListEvents(ctx, task.ID, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(more) != len(events)-1 {
		t.Fatalf("after filter: %d vs %d", len(more), len(events)-1)
	}
}

func TestStatusTransitionAndEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, platform.CreateTask{
		Title:    "t",
		Category: platform.CategoryCrypto,
	})
	if err != nil {
		t.Fatal(err)
	}

	empty := ""
	runtime := "fake"
	cid := "c1"
	task, ev, err := s.ApplyStatusUpdate(ctx, task.ID, storage.StatusUpdate{
		To:          platform.StatusRunning,
		From:        []platform.TaskStatus{platform.StatusQueued},
		Runtime:     &runtime,
		ContainerID: &cid,
		LastError:   &empty,
		EventType:   platform.EventSandboxStarted,
		EventSource: platform.SourceSandbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != platform.StatusRunning || task.ContainerID != "c1" {
		t.Fatalf("task=%+v", task)
	}
	if ev.Sequence < 1 {
		t.Fatal("event sequence")
	}

	// Invalid transition
	_, _, err = s.ApplyStatusUpdate(ctx, task.ID, storage.StatusUpdate{
		To:   platform.StatusQueued,
		From: []platform.TaskStatus{platform.StatusSettled},
	})
	if err == nil {
		t.Fatal("expected invalid transition")
	}
}

func TestModelUsageSummary(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, platform.CreateTask{Title: "u", Category: platform.CategoryMisc})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.InsertModelUsage(ctx, platform.ModelUsage{
		TaskID:        task.ID,
		Model:         "gpt-test",
		InputTokens:   10,
		OutputTokens:  5,
		TotalTokens:   15,
		UsageReported: true,
		StatusCode:    200,
		Status:        "ok",
		CreatedAt:     platform.NowUTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sum, err := s.SummarizeModelUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Days) != 1 || sum.Days[0].RequestCount != 1 || sum.Days[0].TotalTokens != 15 {
		t.Fatalf("summary=%+v", sum)
	}
}

func TestSettings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxConcurrentTasks != 2 {
		t.Fatalf("default=%d", got.MaxConcurrentTasks)
	}
	if err := s.PutSettings(ctx, platform.ExecutionSettings{MaxConcurrentTasks: 4}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxConcurrentTasks != 4 {
		t.Fatalf("got=%d", got.MaxConcurrentTasks)
	}
}

func TestUpdatePromptLockedWhenActive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, _ := s.CreateTask(ctx, platform.CreateTask{Title: "p", Category: platform.CategoryPwn})
	_, _, err := s.ApplyStatusUpdate(ctx, task.ID, storage.StatusUpdate{
		To:   platform.StatusRunning,
		From: []platform.TaskStatus{platform.StatusQueued},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.UpdatePrompt(ctx, task.ID, "new")
	if err == nil {
		t.Fatal("expected lock")
	}
}
