package workspace_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xh12321/ctftools/internal/workspace"
)

func setupTestWorkspace(t *testing.T) (*workspace.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := workspace.New(workspace.Options{
		BaseDir:          dir,
		MaxUploadBytes:   1024 * 1024, // 1MB for test
		MaxFileReadBytes: 512 * 1024,  // 512KB for test
	})
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	return mgr, dir
}

func TestWorkspaceInit(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-01"

	if err := mgr.InitWorkspace(taskID); err != nil {
		t.Fatalf("InitWorkspace failed: %v", err)
	}

	wsDir := mgr.WorkspacePath(taskID)
	for _, sub := range []string{"attachments", "scripts", "evidence", "analysis"} {
		path := filepath.Join(wsDir, sub)
		fi, err := os.Stat(path)
		if err != nil || !fi.IsDir() {
			t.Fatalf("expected directory %s to exist: %v", sub, err)
		}
	}

	writeup, err := mgr.GetWriteup(taskID)
	if err != nil {
		t.Fatalf("GetWriteup: %v", err)
	}
	if !writeup.Exists || !strings.Contains(writeup.Content, "CTF Challenge Writeup") {
		t.Fatalf("unexpected writeup: %+v", writeup)
	}
}

func TestPathTraversalRejections(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-security"
	_ = mgr.InitWorkspace(taskID)

	traversalCases := []string{
		"../escape.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"\\Windows\\System32\\cmd.exe",
		"attachments/../../outside.txt",
		"scripts/../../../etc/shadow",
		"evidence/..\\..\\escape.txt",
		"null\x00byte",
	}

	for _, tc := range traversalCases {
		_, _, err := mgr.ResolveSafePath(taskID, tc)
		if err == nil {
			t.Errorf("expected error for traversal path %q, got nil", tc)
		}
		if !errors.Is(err, workspace.ErrPathTraversal) && !errors.Is(err, workspace.ErrInvalidPath) {
			t.Errorf("expected ErrPathTraversal or ErrInvalidPath for %q, got: %v", tc, err)
		}

		err = mgr.WriteFile(taskID, tc, []byte("bad"))
		if err == nil {
			t.Errorf("expected WriteFile rejection for %q, got nil", tc)
		}

		_, _, err = mgr.ReadFile(taskID, tc)
		if err == nil {
			t.Errorf("expected ReadFile rejection for %q, got nil", tc)
		}
	}
}

func TestSymlinkEscapeRejection(t *testing.T) {
	mgr, baseDir := setupTestWorkspace(t)
	taskID := "test-task-symlink"
	_ = mgr.InitWorkspace(taskID)

	// Create a secret file outside the workspace
	outsideDir := filepath.Join(baseDir, "outside_secret")
	_ = os.MkdirAll(outsideDir, 0o755)
	secretFile := filepath.Join(outsideDir, "secret.key")
	_ = os.WriteFile(secretFile, []byte("TOP_SECRET"), 0o644)

	// Create a symlink inside task workspace pointing to outside
	wsDir := mgr.WorkspacePath(taskID)
	linkPath := filepath.Join(wsDir, "scripts", "leak_link")
	err := os.Symlink(outsideDir, linkPath)
	if err != nil {
		t.Skip("symlink not supported on this platform:", err)
	}

	// Attempting to access file through escaping symlink should be rejected
	_, _, err = mgr.ResolveSafePath(taskID, "scripts/leak_link/secret.key")
	if err == nil {
		t.Fatal("expected symlink escape error, got nil")
	}
	if !errors.Is(err, workspace.ErrSymlinkEscape) {
		t.Fatalf("expected ErrSymlinkEscape, got: %v", err)
	}
}

func TestFileCRUDAndList(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-crud"
	_ = mgr.InitWorkspace(taskID)

	// Write files
	if err := mgr.WriteFile(taskID, "scripts/solve.py", []byte("print('hello')\n")); err != nil {
		t.Fatalf("WriteFile scripts/solve.py: %v", err)
	}
	if err := mgr.WriteFile(taskID, "evidence/log.txt", []byte("found endpoint")); err != nil {
		t.Fatalf("WriteFile evidence/log.txt: %v", err)
	}

	// Read file
	data, info, err := mgr.ReadFile(taskID, "scripts/solve.py")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "print('hello')\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
	if info.Path != "scripts/solve.py" || info.Size != int64(len(data)) {
		t.Fatalf("unexpected info: %+v", info)
	}

	// List files
	files, err := mgr.ListFiles(taskID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	foundSolve := false
	foundEvidence := false
	for _, f := range files {
		if f.Path == "scripts/solve.py" {
			foundSolve = true
		}
		if f.Path == "evidence/log.txt" {
			foundEvidence = true
		}
	}
	if !foundSolve || !foundEvidence {
		t.Fatalf("missing expected files in listing: %+v", files)
	}

	// Delete file
	if err := mgr.DeleteFile(taskID, "evidence/log.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	_, _, err = mgr.ReadFile(taskID, "evidence/log.txt")
	if !errors.Is(err, workspace.ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound after delete, got: %v", err)
	}
}

func TestAttachmentUpload(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-attach"
	_ = mgr.InitWorkspace(taskID)

	payload := []byte("challenge binary content or pcap")
	info, err := mgr.SaveAttachment(taskID, "chall.pcap", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("SaveAttachment: %v", err)
	}
	if info.Path != "attachments/chall.pcap" || info.Size != int64(len(payload)) {
		t.Fatalf("unexpected attachment info: %+v", info)
	}

	// Verify file is readable
	readData, _, err := mgr.ReadFile(taskID, "attachments/chall.pcap")
	if err != nil {
		t.Fatalf("ReadFile attachment: %v", err)
	}
	if !bytes.Equal(readData, payload) {
		t.Fatal("attachment content mismatch")
	}

	// Test exceeding size limit
	oversized := bytes.Repeat([]byte("A"), 2*1024*1024)
	_, err = mgr.SaveAttachment(taskID, "big.bin", bytes.NewReader(oversized), int64(len(oversized)))
	if !errors.Is(err, workspace.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got: %v", err)
	}
}

func TestWriteupOperations(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-writeup"
	_ = mgr.InitWorkspace(taskID)

	updatedContent := "# Final CTF Writeup\n\nSolved with reverse engineering!\n\nFlag: `flag{Milestone2_Succeeded}`"
	if err := mgr.SaveWriteup(taskID, updatedContent); err != nil {
		t.Fatalf("SaveWriteup: %v", err)
	}

	wu, err := mgr.GetWriteup(taskID)
	if err != nil {
		t.Fatalf("GetWriteup: %v", err)
	}
	if !wu.Exists || wu.Content != updatedContent {
		t.Fatalf("writeup content mismatch: %+v", wu)
	}
}

func TestExportZip(t *testing.T) {
	mgr, _ := setupTestWorkspace(t)
	taskID := "test-task-zip"
	_ = mgr.InitWorkspace(taskID)

	_ = mgr.WriteFile(taskID, "scripts/solve.py", []byte("# python solve"))
	_ = mgr.WriteFile(taskID, "evidence/flag.txt", []byte("flag{123}"))

	var buf bytes.Buffer
	if err := mgr.ExportZip(taskID, &buf); err != nil {
		t.Fatalf("ExportZip: %v", err)
	}

	// Verify zip contents
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}

	if !names["scripts/solve.py"] || !names["evidence/flag.txt"] || !names["WRITEUP.md"] {
		t.Fatalf("missing files in zip archive: %+v", names)
	}
}
