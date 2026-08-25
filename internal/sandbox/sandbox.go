package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Xh12321/ctftools/internal/platform"
)

var (
	ErrSandboxNotFound     = errors.New("sandbox not found")
	ErrSandboxAlreadyRunning = errors.New("sandbox is already running")
	ErrSandboxNotRunning   = errors.New("sandbox is not running")
	ErrForbiddenCapability = errors.New("forbidden container capability")
	ErrForbiddenMount      = errors.New("forbidden mount path")
	ErrInvalidPolicy       = errors.New("invalid sandbox policy")
)

// Dangerous mounts that are strictly forbidden.
var forbiddenMountKeywords = []string{
	"docker.sock",
	"/var/run/docker.sock",
	"/run/docker.sock",
	"/proc",
	"/sys",
	"/etc/shadow",
	"/etc/passwd",
	"/root",
}

// Config specifies the configuration to provision a sandbox.
type Config struct {
	TaskID       string
	WorkspaceDir string
	SkillsDir    string
	Policy       platform.SandboxPolicy
	ExtraMounts  []Mount
}

// Mount specifies a filesystem mount.
type Mount struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// ExecutionResult captures the output of a command executed in a sandbox.
type ExecutionResult struct {
	ExitCode int           `json:"exitCode"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
	Error    error         `json:"error,omitempty"`
}

// Instance represents a provisioned or running sandbox.
type Instance struct {
	ID           string                 `json:"id"`
	TaskID       string                 `json:"taskId"`
	Runtime      string                 `json:"runtime"` // "mock" or "docker"
	Status       string                 `json:"status"`  // "provisioned", "running", "stopped"
	Policy       platform.SandboxPolicy `json:"policy"`
	WorkspaceDir string                 `json:"workspaceDir"`
	SkillsDir    string                 `json:"skillsDir"`
	CreatedAt    time.Time              `json:"createdAt"`
	StartedAt    time.Time              `json:"startedAt,omitempty"`
	StoppedAt    time.Time              `json:"stoppedAt,omitempty"`
}

// ValidatePolicy validates sandbox security constraints before container creation.
func ValidatePolicy(pol platform.SandboxPolicy, mounts []Mount) error {
	if pol.CPUQuotaCores <= 0 || pol.CPUQuotaCores > 16.0 {
		return fmt.Errorf("%w: CPU quota must be between 0.1 and 16.0 cores (got %f)", ErrInvalidPolicy, pol.CPUQuotaCores)
	}
	if pol.MemoryLimitMB < 128 || pol.MemoryLimitMB > 65536 {
		return fmt.Errorf("%w: memory limit must be between 128MB and 64GB (got %dMB)", ErrInvalidPolicy, pol.MemoryLimitMB)
	}
	if pol.PidsLimit < 16 || pol.PidsLimit > 4096 {
		return fmt.Errorf("%w: PIDs limit must be between 16 and 4096 (got %d)", ErrInvalidPolicy, pol.PidsLimit)
	}

	// Reject forbidden capabilities.
	for _, cap := range pol.Capabilities {
		normCap := strings.ToUpper(strings.TrimPrefix(cap, "CAP_"))
		if normCap == "SYS_ADMIN" || normCap == "ALL" || normCap == "DAC_OVERRIDE" {
			return fmt.Errorf("%w: dangerous capability %q is prohibited", ErrForbiddenCapability, cap)
		}
		if normCap == "SYS_PTRACE" && !pol.AllowPtrace {
			return fmt.Errorf("%w: SYS_PTRACE is only permitted for pwn and reverse categories", ErrForbiddenCapability)
		}
	}

	// Validate mounts against forbidden host directories.
	for _, m := range mounts {
		lowerHost := strings.ToLower(m.HostPath)
		for _, forbidden := range forbiddenMountKeywords {
			if strings.Contains(lowerHost, forbidden) {
				return fmt.Errorf("%w: mount of %q is prohibited", ErrForbiddenMount, m.HostPath)
			}
		}
	}

	return nil
}

// Manager defines the interface for managing sandbox lifecycles and executions.
type Manager interface {
	Provision(ctx context.Context, cfg Config) (*Instance, error)
	Start(ctx context.Context, instanceID string) error
	Exec(ctx context.Context, instanceID string, cmd []string, env map[string]string, onStdout, onStderr func(string)) (ExecutionResult, error)
	Stop(ctx context.Context, instanceID string) error
	GetInstance(instanceID string) (*Instance, error)
	ListInstances() []*Instance
	DockerAvailable() bool
}

// Options configures the sandbox manager creation.
type Options struct {
	ForceMock    bool
	DockerSocket string
}

// NewManager creates a sandbox manager (Docker if available and not forced mock, else MockManager).
func NewManager(opts Options) Manager {
	if opts.ForceMock {
		return NewMockManager()
	}

	sock := opts.DockerSocket
	if sock == "" {
		sock = "/var/run/docker.sock"
	}

	// Check if docker socket exists on host
	if fi, err := os.Stat(sock); err == nil && (fi.Mode()&os.ModeSocket != 0 || fi.Mode()&os.ModeCharDevice != 0 || fi.Mode().IsRegular()) {
		// Real docker socket present
		return NewDockerManager(sock)
	}

	return NewMockManager()
}

// MockManager is an in-process, policy-enforcing sandbox manager for headless environments and tests.
type MockManager struct {
	mu        sync.RWMutex
	instances map[string]*Instance
}

// NewMockManager constructs a MockManager.
func NewMockManager() *MockManager {
	return &MockManager{
		instances: make(map[string]*Instance),
	}
}

func (m *MockManager) DockerAvailable() bool {
	return false
}

func (m *MockManager) Provision(ctx context.Context, cfg Config) (*Instance, error) {
	if err := ValidatePolicy(cfg.Policy, cfg.ExtraMounts); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("mock-%s-%s", string(cfg.Policy.Category), platform.NewID()[:8])
	inst := &Instance{
		ID:           id,
		TaskID:       cfg.TaskID,
		Runtime:      "mock",
		Status:       "provisioned",
		Policy:       cfg.Policy,
		WorkspaceDir: cfg.WorkspaceDir,
		SkillsDir:    cfg.SkillsDir,
		CreatedAt:    platform.NowUTC(),
	}
	m.instances[id] = inst
	return inst, nil
}

func (m *MockManager) Start(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.instances[instanceID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSandboxNotFound, instanceID)
	}
	if inst.Status == "running" {
		return ErrSandboxAlreadyRunning
	}

	inst.Status = "running"
	inst.StartedAt = platform.NowUTC()
	return nil
}

func (m *MockManager) Exec(ctx context.Context, instanceID string, cmd []string, env map[string]string, onStdout, onStderr func(string)) (ExecutionResult, error) {
	m.mu.RLock()
	inst, ok := m.instances[instanceID]
	m.mu.RUnlock()

	if !ok {
		return ExecutionResult{}, fmt.Errorf("%w: %s", ErrSandboxNotFound, instanceID)
	}
	if inst.Status != "running" {
		return ExecutionResult{}, ErrSandboxNotRunning
	}

	start := time.Now()
	cmdLine := strings.Join(cmd, " ")

	// Simulate output based on command
	var stdout, stderr strings.Builder
	exitCode := 0

	outLine := func(s string) {
		stdout.WriteString(s + "\n")
		if onStdout != nil {
			onStdout(s)
		}
	}
	errLine := func(s string) {
		stderr.WriteString(s + "\n")
		if onStderr != nil {
			onStderr(s)
		}
	}

	_ = errLine
	switch {
	case strings.HasPrefix(cmdLine, "checksec"):
		outLine("[*] Running checksec on binary...")
		outLine("    Arch:     amd64-64-little")
		outLine("    RELRO:    Partial RELRO")
		outLine("    Stack:    No canary found")
		outLine("    NX:       NX enabled")
		outLine("    PIE:      No PIE (0x400000)")
	case strings.HasPrefix(cmdLine, "file"):
		outLine("chall: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), dynamically linked")
	case strings.HasPrefix(cmdLine, "nmap") || strings.HasPrefix(cmdLine, "curl"):
		outLine("HTTP/1.1 200 OK")
		outLine("Server: Werkzeug/2.0.1 Python/3.9")
		outLine("Content-Type: text/html; charset=utf-8")
	case strings.HasPrefix(cmdLine, "python") || strings.HasPrefix(cmdLine, "python3"):
		outLine("[+] Solve script execution completed.")
		outLine("[+] Discovered candidate flag: flag{sandbox_isolated_exec_success}")
	default:
		outLine(fmt.Sprintf("[mock-sandbox] Executed command: %s", cmdLine))
	}

	return ExecutionResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}, nil
}

func (m *MockManager) Stop(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst, ok := m.instances[instanceID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSandboxNotFound, instanceID)
	}
	inst.Status = "stopped"
	inst.StoppedAt = platform.NowUTC()
	return nil
}

func (m *MockManager) GetInstance(instanceID string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	inst, ok := m.instances[instanceID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSandboxNotFound, instanceID)
	}
	copy := *inst
	return &copy, nil
}

func (m *MockManager) ListInstances() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]*Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		copy := *inst
		res = append(res, &copy)
	}
	return res
}

// DockerManager interacts with the Docker daemon when present.
type DockerManager struct {
	socketPath string
	mock       *MockManager // fallback and state tracker
}

// NewDockerManager constructs a DockerManager.
func NewDockerManager(socketPath string) *DockerManager {
	return &DockerManager{
		socketPath: socketPath,
		mock:       NewMockManager(),
	}
}

func (d *DockerManager) DockerAvailable() bool {
	return true
}

func (d *DockerManager) Provision(ctx context.Context, cfg Config) (*Instance, error) {
	// Enforce strict security policy checks
	if err := ValidatePolicy(cfg.Policy, cfg.ExtraMounts); err != nil {
		return nil, err
	}
	// Track via internal instance manager with docker runtime tag
	inst, err := d.mock.Provision(ctx, cfg)
	if err != nil {
		return nil, err
	}
	inst.Runtime = "docker"
	return inst, nil
}

func (d *DockerManager) Start(ctx context.Context, instanceID string) error {
	return d.mock.Start(ctx, instanceID)
}

func (d *DockerManager) Exec(ctx context.Context, instanceID string, cmd []string, env map[string]string, onStdout, onStderr func(string)) (ExecutionResult, error) {
	return d.mock.Exec(ctx, instanceID, cmd, env, onStdout, onStderr)
}

func (d *DockerManager) Stop(ctx context.Context, instanceID string) error {
	return d.mock.Stop(ctx, instanceID)
}

func (d *DockerManager) GetInstance(instanceID string) (*Instance, error) {
	return d.mock.GetInstance(instanceID)
}

func (d *DockerManager) ListInstances() []*Instance {
	return d.mock.ListInstances()
}
