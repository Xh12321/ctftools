package sandbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/sandbox"
)

func TestPolicyValidationLimits(t *testing.T) {
	// Valid Web policy
	validPol := platform.DefaultSandboxPolicy(platform.CategoryWeb, "")
	if err := sandbox.ValidatePolicy(validPol, nil); err != nil {
		t.Fatalf("expected valid policy, got: %v", err)
	}

	// Invalid CPU
	badCPU := validPol
	badCPU.CPUQuotaCores = -1.0
	if err := sandbox.ValidatePolicy(badCPU, nil); !errors.Is(err, sandbox.ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy for negative CPU, got: %v", err)
	}

	// Invalid Memory (too small)
	badMem := validPol
	badMem.MemoryLimitMB = 32
	if err := sandbox.ValidatePolicy(badMem, nil); !errors.Is(err, sandbox.ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy for 32MB memory, got: %v", err)
	}

	// Invalid PIDs
	badPids := validPol
	badPids.PidsLimit = 5
	if err := sandbox.ValidatePolicy(badPids, nil); !errors.Is(err, sandbox.ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy for 5 PIDs, got: %v", err)
	}
}

func TestCapabilitySecurityRestrictions(t *testing.T) {
	// SYS_ADMIN should always be rejected
	polAdmin := platform.DefaultSandboxPolicy(platform.CategoryWeb, "")
	polAdmin.Capabilities = []string{"SYS_ADMIN"}
	if err := sandbox.ValidatePolicy(polAdmin, nil); !errors.Is(err, sandbox.ErrForbiddenCapability) {
		t.Fatalf("expected ErrForbiddenCapability for SYS_ADMIN, got: %v", err)
	}

	// SYS_PTRACE on Web should be rejected because AllowPtrace is false
	polWebPtrace := platform.DefaultSandboxPolicy(platform.CategoryWeb, "")
	polWebPtrace.Capabilities = []string{"SYS_PTRACE"}
	if err := sandbox.ValidatePolicy(polWebPtrace, nil); !errors.Is(err, sandbox.ErrForbiddenCapability) {
		t.Fatalf("expected ErrForbiddenCapability for SYS_PTRACE on web, got: %v", err)
	}

	// SYS_PTRACE on Pwn is allowed because AllowPtrace is true
	polPwn := platform.DefaultSandboxPolicy(platform.CategoryPwn, "")
	if err := sandbox.ValidatePolicy(polPwn, nil); err != nil {
		t.Fatalf("SYS_PTRACE on pwn should be allowed: %v", err)
	}
}

func TestForbiddenMountRestrictions(t *testing.T) {
	validPol := platform.DefaultSandboxPolicy(platform.CategoryWeb, "")

	badMounts := []sandbox.Mount{
		{HostPath: "/var/run/docker.sock", ContainerPath: "/var/run/docker.sock", ReadOnly: false},
		{HostPath: "/etc/shadow", ContainerPath: "/shadow", ReadOnly: true},
		{HostPath: "/proc", ContainerPath: "/proc", ReadOnly: true},
	}

	for _, bm := range badMounts {
		err := sandbox.ValidatePolicy(validPol, []sandbox.Mount{bm})
		if !errors.Is(err, sandbox.ErrForbiddenMount) {
			t.Errorf("expected ErrForbiddenMount for %s, got: %v", bm.HostPath, err)
		}
	}
}

func TestSandboxLifecycleAndExec(t *testing.T) {
	mgr := sandbox.NewMockManager()
	ctx := context.Background()

	cfg := sandbox.Config{
		TaskID:       "task-sb-01",
		WorkspaceDir: "/tmp/workspaces/task-sb-01",
		SkillsDir:    "/opt/cpi/ctf-skills",
		Policy:       platform.DefaultSandboxPolicy(platform.CategoryPwn, ""),
	}

	inst, err := mgr.Provision(ctx, cfg)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}
	if inst.Status != "provisioned" || inst.TaskID != cfg.TaskID {
		t.Fatalf("unexpected instance: %+v", inst)
	}

	// Exec before start should fail
	_, err = mgr.Exec(ctx, inst.ID, []string{"checksec", "chall"}, nil, nil, nil)
	if !errors.Is(err, sandbox.ErrSandboxNotRunning) {
		t.Fatalf("expected ErrSandboxNotRunning, got: %v", err)
	}

	// Start sandbox
	if err := mgr.Start(ctx, inst.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify status
	runningInst, err := mgr.GetInstance(inst.ID)
	if err != nil || runningInst.Status != "running" {
		t.Fatalf("expected status running, got %+v (err: %v)", runningInst, err)
	}

	// Exec command with output callbacks
	var stdoutLines []string
	res, err := mgr.Exec(ctx, inst.ID, []string{"checksec", "chall"}, nil, func(line string) {
		stdoutLines = append(stdoutLines, line)
	}, nil)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "NX enabled") {
		t.Fatalf("unexpected stdout: %s", res.Stdout)
	}
	if len(stdoutLines) == 0 {
		t.Fatal("expected stdout callbacks")
	}

	// Stop sandbox
	if err := mgr.Stop(ctx, inst.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	stoppedInst, err := mgr.GetInstance(inst.ID)
	if err != nil || stoppedInst.Status != "stopped" {
		t.Fatalf("expected status stopped, got %+v", stoppedInst)
	}
}
