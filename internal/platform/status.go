package platform

import (
	"fmt"
	"strings"
)

// TaskStatus is the lifecycle state of a challenge task.
type TaskStatus string

const (
	StatusQueued       TaskStatus = "queued"
	StatusProvisioning TaskStatus = "provisioning"
	StatusRunning      TaskStatus = "running"
	StatusPaused       TaskStatus = "paused"
	StatusSettled      TaskStatus = "settled"
	StatusCancelled    TaskStatus = "cancelled"
	StatusFailed       TaskStatus = "failed"
	StatusDelegating   TaskStatus = "delegating"
)

var validStatuses = map[TaskStatus]struct{}{
	StatusQueued:       {},
	StatusProvisioning: {},
	StatusRunning:      {},
	StatusPaused:       {},
	StatusSettled:      {},
	StatusCancelled:    {},
	StatusFailed:       {},
	StatusDelegating:   {},
}

// ParseTaskStatus validates a status string.
func ParseTaskStatus(raw string) (TaskStatus, error) {
	s := TaskStatus(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := validStatuses[s]; !ok {
		return "", fmt.Errorf("invalid task status %q", raw)
	}
	return s, nil
}

// IsTerminal reports whether the status is a final lifecycle state.
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case StatusSettled, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

// IsActive reports whether the task is currently consuming runtime resources.
func (s TaskStatus) IsActive() bool {
	switch s {
	case StatusProvisioning, StatusRunning, StatusDelegating:
		return true
	default:
		return false
	}
}

// CanStart reports whether Start is allowed from this status.
func (s TaskStatus) CanStart() bool {
	return s == StatusQueued || s == StatusFailed || s == StatusCancelled
}

// CanPause reports whether Pause is allowed.
func (s TaskStatus) CanPause() bool {
	return s == StatusRunning || s == StatusDelegating || s == StatusProvisioning
}

// CanResume reports whether Resume is allowed.
func (s TaskStatus) CanResume() bool {
	return s == StatusPaused
}

// CanAbort reports whether Abort/Cancel is allowed.
func (s TaskStatus) CanAbort() bool {
	return !s.IsTerminal()
}

// CanRetry reports whether Retry is allowed.
func (s TaskStatus) CanRetry() bool {
	return s == StatusFailed || s == StatusCancelled || s == StatusSettled || s == StatusPaused
}

// CanCloseSandbox reports whether the sandbox may be closed.
func (s TaskStatus) CanCloseSandbox() bool {
	return s == StatusSettled || s == StatusCancelled || s == StatusFailed || s == StatusPaused
}

// String implements fmt.Stringer.
func (s TaskStatus) String() string { return string(s) }

// FlagReviewAction is the operator decision on a flag candidate.
type FlagReviewAction string

const (
	FlagAccept FlagReviewAction = "accept"
	FlagReject FlagReviewAction = "reject"
)

// ParseFlagReviewAction validates a review action.
func ParseFlagReviewAction(raw string) (FlagReviewAction, error) {
	a := FlagReviewAction(strings.ToLower(strings.TrimSpace(raw)))
	switch a {
	case FlagAccept, FlagReject:
		return a, nil
	default:
		return "", fmt.Errorf("invalid flag review action %q", raw)
	}
}
