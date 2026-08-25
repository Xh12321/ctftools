package platform

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Task is the durable snapshot of a CTF challenge task.
type Task struct {
	ID           string     `json:"id"`
	ParentTaskID string     `json:"parentTaskId,omitempty"`
	HandoffID    string     `json:"handoffId,omitempty"`
	Title        string     `json:"title"`
	Category     Category   `json:"category"`
	Description  string     `json:"description"`
	Prompt       string     `json:"prompt"`
	Target       string     `json:"target,omitempty"`
	FlagFormat   string     `json:"flagFormat,omitempty"`
	ModelProfile string     `json:"modelProfile,omitempty"`
	ModelID      string     `json:"modelId,omitempty"`
	Status       TaskStatus `json:"status"`
	Image        string     `json:"image"`
	Runtime      string     `json:"runtime,omitempty"`
	ContainerID  string     `json:"containerId,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// CreateTask is the input for creating a new root task.
type CreateTask struct {
	Title        string   `json:"title"`
	Category     Category `json:"category"`
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	Target       string   `json:"target,omitempty"`
	FlagFormat   string   `json:"flagFormat,omitempty"`
	ModelProfile string   `json:"modelProfile,omitempty"`
	ModelID      string   `json:"modelId,omitempty"`
	Image        string   `json:"image,omitempty"`
}

// Validate checks required fields on CreateTask.
func (c CreateTask) Validate() error {
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(c.Title) > 256 {
		return fmt.Errorf("title must be at most 256 characters")
	}
	if _, err := ParseCategory(string(c.Category)); err != nil {
		return err
	}
	return nil
}

// QueuedTask is a lightweight view used by the scheduler.
type QueuedTask struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Category  Category   `json:"category"`
	Status    TaskStatus `json:"status"`
	QueuedAt  time.Time  `json:"queuedAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// TaskEvent is one append-only event in a task timeline.
type TaskEvent struct {
	ID         string          `json:"id"`
	TaskID     string          `json:"taskId"`
	Sequence   int64           `json:"sequence"`
	Source     string          `json:"source"`
	Type       string          `json:"type"`
	TurnID     string          `json:"turnId,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// JSONPayload marshals v into a JSON object suitable for event payloads.
// Nil values become an empty object.
func JSONPayload(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`{}`)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"payload_marshal_failed"}`)
	}
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(`{}`)
	}
	return b
}

// ModelUsage is one model API call usage record.
type ModelUsage struct {
	ID                string    `json:"id"`
	TaskID            string    `json:"taskId"`
	Model             string    `json:"model"`
	InputTokens       int64     `json:"inputTokens"`
	CachedInputTokens int64     `json:"cachedInputTokens"`
	OutputTokens      int64     `json:"outputTokens"`
	ReasoningTokens   int64     `json:"reasoningTokens"`
	TotalTokens       int64     `json:"totalTokens"`
	UsageReported     bool      `json:"usageReported"`
	LatencyMs         int64     `json:"latencyMs"`
	StatusCode        int       `json:"statusCode"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
}

// ModelUsageDay aggregates usage for a calendar day.
type ModelUsageDay struct {
	Date                string `json:"date"`
	RequestCount        int64  `json:"requestCount"`
	SuccessfulRequests  int64  `json:"successfulRequests"`
	FailedRequests      int64  `json:"failedRequests"`
	ReportedRequests    int64  `json:"reportedRequests"`
	InputTokens         int64  `json:"inputTokens"`
	CachedInputTokens   int64  `json:"cachedInputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	ReasoningTokens     int64  `json:"reasoningTokens"`
	TotalTokens         int64  `json:"totalTokens"`
}

// ModelUsageSummary is the response shape for GET /api/model-usage.
type ModelUsageSummary struct {
	Days []ModelUsageDay `json:"days"`
}

// ExecutionSettings holds global daemon execution knobs.
type ExecutionSettings struct {
	MaxConcurrentTasks int `json:"maxConcurrentTasks"`
}

// Validate checks execution settings.
func (s ExecutionSettings) Validate() error {
	if s.MaxConcurrentTasks < 1 {
		return fmt.Errorf("maxConcurrentTasks must be >= 1")
	}
	if s.MaxConcurrentTasks > 64 {
		return fmt.Errorf("maxConcurrentTasks must be <= 64")
	}
	return nil
}

// DefaultExecutionSettings returns safe defaults.
func DefaultExecutionSettings() ExecutionSettings {
	return ExecutionSettings{MaxConcurrentTasks: 2}
}

// SchedulerStatus is exposed on the system overview.
type SchedulerStatus struct {
	MaxConcurrentTasks int `json:"maxConcurrentTasks"`
	ActiveTaskCount    int `json:"activeTaskCount"`
	QueuedTaskCount    int `json:"queuedTaskCount"`
}

// FlagFinding is a candidate flag discovered by the agent or detector.
type FlagFinding struct {
	ID            string  `json:"id"`
	Value         string  `json:"value"`
	Source        string  `json:"source"`
	Evidence      string  `json:"evidence,omitempty"`
	Confidence    float64 `json:"confidence"`
	FormatMatched bool    `json:"formatMatched"`
	Verified      bool    `json:"verified"`
	Status        string  `json:"status,omitempty"` // pending|accepted|rejected
}

// FlagFeedback is the operator review payload for a candidate.
type FlagFeedback struct {
	FlagID  string           `json:"flagId"`
	Action  FlagReviewAction `json:"action"`
	Note    string           `json:"note,omitempty"`
	Value   string           `json:"value,omitempty"`
}

// EventList is the response for event polling.
type EventList struct {
	Events []TaskEvent `json:"events"`
}

// TaskList is the response for listing tasks.
type TaskList struct {
	Tasks []Task `json:"tasks"`
}

// NowUTC returns the current time truncated to milliseconds in UTC.
func NowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

// FormatTime serializes t as RFC3339Nano in UTC.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ParseTime parses an RFC3339 / RFC3339Nano timestamp.
func ParseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
