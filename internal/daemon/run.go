package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xh12321/ctftools/internal/agent"
	"github.com/Xh12321/ctftools/internal/api"
	"github.com/Xh12321/ctftools/internal/eventhub"
	"github.com/Xh12321/ctftools/internal/platform"
	"github.com/Xh12321/ctftools/internal/sandbox"
	"github.com/Xh12321/ctftools/internal/storage"
	"github.com/Xh12321/ctftools/internal/workspace"
)

// Config controls daemon startup.
type Config struct {
	DataDir      string
	Addr         string
	Token        string
	DockerSocket string
	ForceMock    bool
	// StepDelay controls FakeRunner pacing (tests may shorten it).
	StepDelay time.Duration
}

// DefaultConfig resolves paths from the environment.
func DefaultConfig() Config {
	dataDir := os.Getenv("CTF_AGENT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".ctf-btfly")
	}
	addr := os.Getenv("CTF_DAEMON_ADDR")
	if addr == "" {
		addr = "127.0.0.1:7521"
	}
	token := os.Getenv("CTF_DAEMON_TOKEN")
	dockerSock := os.Getenv("DOCKER_HOST")
	return Config{
		DataDir:      dataDir,
		Addr:         addr,
		Token:        token,
		DockerSocket: dockerSock,
		StepDelay:    40 * time.Millisecond,
	}
}

// Start boots storage, workspace, sandbox manager, fake agent service and the HTTP API,
// then blocks until ctx is cancelled.
func Start(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	token, err := ensureToken(cfg)
	if err != nil {
		return err
	}

	dbPath := filepath.Join(cfg.DataDir, "platform.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	hub := eventhub.New()
	defer hub.Close()

	wsDir := filepath.Join(cfg.DataDir, "workspaces")
	wsMgr, err := workspace.New(workspace.Options{
		BaseDir: wsDir,
	})
	if err != nil {
		return fmt.Errorf("init workspace manager: %w", err)
	}

	sbMgr := sandbox.NewManager(sandbox.Options{
		ForceMock:    cfg.ForceMock,
		DockerSocket: cfg.DockerSocket,
	})

	var svc *agent.Service
	delay := cfg.StepDelay
	if delay <= 0 {
		delay = 40 * time.Millisecond
	}
	runner := agent.NewFakeRunner(agent.FakeRunnerOptions{
		StepDelay: delay,
		Workspace: wsMgr,
		OnFinding: func(taskID string, f platform.FlagFinding) {
			if svc != nil {
				svc.RegisterFinding(taskID, f)
			}
		},
	})
	svc = agent.NewService(store, hub, runner, wsMgr, sbMgr)

	srv := api.NewServer(svc, token)
	addr, err := srv.ListenAndServe(cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	log.Printf("ctfagent-daemon listening on http://%s", addr.String())
	log.Printf("data dir: %s", cfg.DataDir)
	log.Printf("workspaces dir: %s", wsDir)
	log.Printf("docker available: %v", sbMgr.DockerAvailable())
	log.Printf("auth token stored at %s", filepath.Join(cfg.DataDir, "daemon.token"))
	log.Printf("mode: fake-agent (Milestone 2)")

	_ = os.WriteFile(filepath.Join(cfg.DataDir, "daemon.json"), []byte(fmt.Sprintf(
		`{"addr":%q,"mode":"fake-agent","tokenFile":"daemon.token","workspacesDir":%q}`+"\n", addr.String(), wsDir,
	)), 0o600)

	<-ctx.Done()
	log.Printf("shutting down...")
	svc.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

func ensureToken(cfg Config) (string, error) {
	tokenPath := filepath.Join(cfg.DataDir, "daemon.token")
	if strings.TrimSpace(cfg.Token) != "" {
		if err := os.WriteFile(tokenPath, []byte(cfg.Token), 0o600); err != nil {
			return "", fmt.Errorf("write token: %w", err)
		}
		return cfg.Token, nil
	}
	if b, err := os.ReadFile(tokenPath); err == nil {
		t := strings.TrimSpace(string(b))
		if t != "" {
			return t, nil
		}
	}
	token := newToken()
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return token, nil
}

func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
