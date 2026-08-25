package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/Xh12321/ctftools/internal/platform"
)

// FakeRunnerOptions controls FakeRunner behaviour.
type FakeRunnerOptions struct {
	// StepDelay is the pause between synthetic events.
	StepDelay time.Duration
	// FlagValue is the candidate flag to emit (default flag{fake-agent-ok}).
	FlagValue string
	// FailAfter, if > 0, causes the runner to error after that many steps.
	FailAfter int
	// OnFinding is invoked when a flag candidate is produced.
	OnFinding func(taskID string, f platform.FlagFinding)
}

// FakeRunner simulates an agent solving a challenge without Docker or models.
type FakeRunner struct {
	opts FakeRunnerOptions
}

// NewFakeRunner builds a FakeRunner with defaults.
func NewFakeRunner(opts FakeRunnerOptions) *FakeRunner {
	if opts.StepDelay <= 0 {
		opts.StepDelay = 20 * time.Millisecond
	}
	if opts.FlagValue == "" {
		opts.FlagValue = "flag{fake-agent-ok}"
	}
	return &FakeRunner{opts: opts}
}

// Run implements Runner.
func (r *FakeRunner) Run(ctx context.Context, task platform.Task, emit EmitFunc) error {
	sleep := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.opts.StepDelay):
			return nil
		}
	}

	turnID := platform.NewID()
	if _, err := emit(platform.SourceAgent, platform.EventAgentTurnStarted, turnID, "", map[string]any{
		"turnId": turnID,
		"message": fmt.Sprintf("Analyzing %s challenge %q", task.Category, task.Title),
	}); err != nil {
		return err
	}
	if err := sleep(); err != nil {
		return err
	}

	if r.opts.FailAfter == 1 {
		return fmt.Errorf("fake agent forced failure")
	}

	// Tool call: file inventory.
	toolID := platform.NewID()
	if _, err := emit(platform.SourceTool, platform.EventToolStarted, turnID, toolID, map[string]any{
		"name":   "list_files",
		"input":  map[string]any{"path": "/workspace"},
		"toolCallId": toolID,
	}); err != nil {
		return err
	}
	if err := sleep(); err != nil {
		return err
	}

	if _, err := emit(platform.SourceTool, platform.EventToolOutput, turnID, toolID, map[string]any{
		"name":   "list_files",
		"output": "challenge.txt\nREADME.md\n",
	}); err != nil {
		return err
	}
	if _, err := emit(platform.SourceTool, platform.EventToolCompleted, turnID, toolID, map[string]any{
		"name":     "list_files",
		"exitCode": 0,
	}); err != nil {
		return err
	}
	if err := sleep(); err != nil {
		return err
	}

	if r.opts.FailAfter == 2 {
		return fmt.Errorf("fake agent forced failure after tool")
	}

	// Assistant message with a suspicious flag.
	if _, err := emit(platform.SourceAgent, platform.EventAgentMessageDelta, turnID, "", map[string]any{
		"delta": fmt.Sprintf("I found a candidate: %s\n", r.opts.FlagValue),
	}); err != nil {
		return err
	}
	if _, err := emit(platform.SourceAgent, platform.EventAgentMessageDone, turnID, "", map[string]any{
		"content": fmt.Sprintf("Analysis complete. Candidate flag: %s", r.opts.FlagValue),
	}); err != nil {
		return err
	}

	finding := platform.FlagFinding{
		ID:            platform.NewID(),
		Value:         r.opts.FlagValue,
		Source:        "fake-agent",
		Evidence:      "synthetic candidate from FakeRunner",
		Confidence:    0.91,
		FormatMatched: true,
		Verified:      false,
		Status:        "pending",
	}
	if r.opts.OnFinding != nil {
		r.opts.OnFinding(task.ID, finding)
	}
	if _, err := emit(platform.SourceAgent, platform.EventFlagCandidate, turnID, "", map[string]any{
		"id":            finding.ID,
		"value":         finding.Value,
		"source":        finding.Source,
		"evidence":      finding.Evidence,
		"confidence":    finding.Confidence,
		"formatMatched": finding.FormatMatched,
		"status":        finding.Status,
	}); err != nil {
		return err
	}

	if _, err := emit(platform.SourceAgent, platform.EventAgentTurnCompleted, turnID, "", map[string]any{
		"turnId": turnID,
	}); err != nil {
		return err
	}

	// Stay "running" briefly so tests can observe the running state and
	// review flags before auto-settle. Production agent would keep looping.
	if err := sleep(); err != nil {
		return err
	}
	if err := sleep(); err != nil {
		return err
	}

	return nil
}
