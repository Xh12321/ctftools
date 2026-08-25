package workspace

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xh12321/ctftools/internal/platform"
)

var (
	ErrInvalidPath     = errors.New("invalid workspace path")
	ErrPathTraversal   = errors.New("path traversal rejected")
	ErrSymlinkEscape   = errors.New("symlink escapes workspace boundary")
	ErrFileNotFound    = errors.New("file not found")
	ErrFileTooLarge    = errors.New("file exceeds maximum allowed size")
	ErrInvalidFilename = errors.New("invalid filename")
)

const (
	DefaultMaxUploadBytes   int64 = 50 * 1024 * 1024 // 50 MB
	DefaultMaxFileReadBytes int64 = 10 * 1024 * 1024 // 10 MB
)

// Options configures the workspace manager.
type Options struct {
	BaseDir          string
	MaxUploadBytes   int64
	MaxFileReadBytes int64
}

// Manager manages per-task workspaces on disk with strict containment.
type Manager struct {
	baseDir          string
	maxUploadBytes   int64
	maxFileReadBytes int64
}

// New creates a new WorkspaceManager.
func New(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return nil, fmt.Errorf("workspace base dir is required")
	}
	absBase, err := filepath.Abs(opts.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace base dir: %w", err)
	}
	if err := os.MkdirAll(absBase, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace base dir: %w", err)
	}

	maxUpload := opts.MaxUploadBytes
	if maxUpload <= 0 {
		maxUpload = DefaultMaxUploadBytes
	}
	maxRead := opts.MaxFileReadBytes
	if maxRead <= 0 {
		maxRead = DefaultMaxFileReadBytes
	}

	return &Manager{
		baseDir:          absBase,
		maxUploadBytes:   maxUpload,
		maxFileReadBytes: maxRead,
	}, nil
}

// BaseDir returns the root workspace directory.
func (m *Manager) BaseDir() string {
	return m.baseDir
}

// WorkspacePath returns the absolute path for a task's workspace directory.
func (m *Manager) WorkspacePath(taskID string) string {
	cleanID := filepath.Base(taskID)
	return filepath.Join(m.baseDir, cleanID)
}

// InitWorkspace creates the task workspace and its standard subdirectories.
func (m *Manager) InitWorkspace(taskID string) error {
	wsDir := m.WorkspacePath(taskID)
	subdirs := []string{"attachments", "scripts", "evidence", "analysis"}
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(wsDir, sub), 0o755); err != nil {
			return fmt.Errorf("init workspace dir %s: %w", sub, err)
		}
	}

	// Create initial template WRITEUP.md if it doesn't already exist.
	writeupPath := filepath.Join(wsDir, "WRITEUP.md")
	if _, err := os.Stat(writeupPath); os.IsNotExist(err) {
		initialWriteup := fmt.Sprintf("# CTF Challenge Writeup: %s\n\n## 1. 题目概况\n\n## 2. 分析过程\n\n## 3. 关键脚本与证据\n\n## 4. Flag\n", taskID)
		if err := os.WriteFile(writeupPath, []byte(initialWriteup), 0o644); err != nil {
			return fmt.Errorf("init writeup template: %w", err)
		}
	}
	return nil
}

// ResolveSafePath validates that relPath stays strictly inside the task workspace.
// Returns (normalizedForwardSlashRelPath, absoluteDiskPath, error).
func (m *Manager) ResolveSafePath(taskID string, relPath string) (string, string, error) {
	if strings.Contains(relPath, "\x00") {
		return "", "", ErrInvalidPath
	}

	// Reject raw absolute paths.
	if strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "\\") || filepath.IsAbs(relPath) {
		return "", "", ErrPathTraversal
	}

	wsDir := m.WorkspacePath(taskID)
	// Clean and normalize separators.
	cleanRel := filepath.Clean(filepath.FromSlash(relPath))
	if cleanRel == "." || cleanRel == "" {
		return "", wsDir, nil
	}

	// Reject directory traversal segments.
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || strings.Contains(cleanRel, string(filepath.Separator)+"..") {
		return "", "", ErrPathTraversal
	}

	targetAbs := filepath.Join(wsDir, cleanRel)

	// Ensure prefix containment.
	expectedPrefix := wsDir + string(filepath.Separator)
	if targetAbs != wsDir && !strings.HasPrefix(targetAbs, expectedPrefix) {
		return "", "", ErrPathTraversal
	}

	// Symlink escape validation: check existing path components.
	if err := m.checkSymlinkEscape(wsDir, targetAbs); err != nil {
		return "", "", err
	}

	forwardRel := filepath.ToSlash(cleanRel)
	return forwardRel, targetAbs, nil
}

func (m *Manager) checkSymlinkEscape(wsDir, targetAbs string) error {
	// Walk upwards until we find an existing ancestor on disk.
	curr := targetAbs
	for {
		fi, err := os.Lstat(curr)
		if err == nil {
			// Path component exists; evaluate its symlinks.
			realPath, err := filepath.EvalSymlinks(curr)
			if err != nil {
				return err
			}
			realWsDir, err := filepath.EvalSymlinks(wsDir)
			if err != nil {
				realWsDir = wsDir
			}
			if realPath != realWsDir && !strings.HasPrefix(realPath, realWsDir+string(filepath.Separator)) {
				return ErrSymlinkEscape
			}
			// If curr is a symlink, verify its target.
			if fi.Mode()&os.ModeSymlink != 0 {
				linkTarget, err := os.Readlink(curr)
				if err == nil {
					if filepath.IsAbs(linkTarget) {
						if !strings.HasPrefix(linkTarget, realWsDir+string(filepath.Separator)) {
							return ErrSymlinkEscape
						}
					}
				}
			}
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." {
			break
		}
		curr = parent
	}
	return nil
}

// ListFiles lists all files and directories in the task workspace.
func (m *Manager) ListFiles(taskID string) ([]platform.FileInfo, error) {
	wsDir := m.WorkspacePath(taskID)
	if _, err := os.Stat(wsDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: workspace not initialized", ErrFileNotFound)
	}

	var results []platform.FileInfo
	err := filepath.Walk(wsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == wsDir {
			return nil
		}

		rel, err := filepath.Rel(wsDir, path)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)

		// Determine content type for files.
		contentType := ""
		if !info.IsDir() {
			contentType = detectContentType(path)
		}

		results = append(results, platform.FileInfo{
			Path:        slashRel,
			Name:        info.Name(),
			Size:        info.Size(),
			IsDir:       info.IsDir(),
			ModTime:     info.ModTime().UTC().Truncate(time.Millisecond),
			ContentType: contentType,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})
	return results, nil
}

// ReadFile reads a file from the workspace, subject to max size.
func (m *Manager) ReadFile(taskID string, relPath string) ([]byte, platform.FileInfo, error) {
	slashRel, absPath, err := m.ResolveSafePath(taskID, relPath)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, platform.FileInfo{}, ErrFileNotFound
		}
		return nil, platform.FileInfo{}, err
	}
	if info.IsDir() {
		return nil, platform.FileInfo{}, fmt.Errorf("%w: cannot read directory as file", ErrInvalidPath)
	}
	if info.Size() > m.maxFileReadBytes {
		return nil, platform.FileInfo{}, fmt.Errorf("%w (%d > %d)", ErrFileTooLarge, info.Size(), m.maxFileReadBytes)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}

	fileInfo := platform.FileInfo{
		Path:        slashRel,
		Name:        info.Name(),
		Size:        info.Size(),
		IsDir:       false,
		ModTime:     info.ModTime().UTC().Truncate(time.Millisecond),
		ContentType: detectContentTypeFromBytes(data, absPath),
	}
	return data, fileInfo, nil
}

// WriteFile writes a file to the workspace with safe containment.
func (m *Manager) WriteFile(taskID string, relPath string, content []byte) error {
	_, absPath, err := m.ResolveSafePath(taskID, relPath)
	if err != nil {
		return err
	}

	if int64(len(content)) > m.maxUploadBytes {
		return fmt.Errorf("%w: payload exceeds limit", ErrFileTooLarge)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	return os.WriteFile(absPath, content, 0o644)
}

// DeleteFile deletes a file or empty directory in the workspace.
func (m *Manager) DeleteFile(taskID string, relPath string) error {
	slashRel, absPath, err := m.ResolveSafePath(taskID, relPath)
	if err != nil {
		return err
	}
	if slashRel == "" || slashRel == "." {
		return fmt.Errorf("%w: cannot delete workspace root", ErrInvalidPath)
	}
	if err := os.Remove(absPath); err != nil {
		if os.IsNotExist(err) {
			return ErrFileNotFound
		}
		return err
	}
	return nil
}

// SaveAttachment saves an uploaded stream into the workspace `attachments/` folder.
func (m *Manager) SaveAttachment(taskID string, filename string, r io.Reader, size int64) (platform.FileInfo, error) {
	if size > m.maxUploadBytes {
		return platform.FileInfo{}, fmt.Errorf("%w (%d > %d)", ErrFileTooLarge, size, m.maxUploadBytes)
	}

	cleanName := filepath.Base(filepath.Clean(filename))
	cleanName = strings.TrimSpace(cleanName)
	cleanName = strings.ReplaceAll(cleanName, "/", "_")
	cleanName = strings.ReplaceAll(cleanName, "\\", "_")
	if cleanName == "" || cleanName == "." || cleanName == ".." {
		return platform.FileInfo{}, ErrInvalidFilename
	}

	relPath := filepath.Join("attachments", cleanName)
	slashRel, absPath, err := m.ResolveSafePath(taskID, relPath)
	if err != nil {
		return platform.FileInfo{}, err
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return platform.FileInfo{}, fmt.Errorf("create attachments dir: %w", err)
	}

	f, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return platform.FileInfo{}, err
	}
	defer f.Close()

	limitedReader := io.LimitReader(r, m.maxUploadBytes+1)
	written, err := io.Copy(f, limitedReader)
	if err != nil {
		_ = os.Remove(absPath)
		return platform.FileInfo{}, err
	}
	if written > m.maxUploadBytes {
		_ = os.Remove(absPath)
		return platform.FileInfo{}, fmt.Errorf("%w: uploaded data exceeds limit", ErrFileTooLarge)
	}

	stat, err := f.Stat()
	if err != nil {
		return platform.FileInfo{}, err
	}

	return platform.FileInfo{
		Path:        slashRel,
		Name:        cleanName,
		Size:        written,
		IsDir:       false,
		ModTime:     stat.ModTime().UTC().Truncate(time.Millisecond),
		ContentType: detectContentType(absPath),
	}, nil
}

// GetWriteup retrieves the WRITEUP.md content.
func (m *Manager) GetWriteup(taskID string) (platform.Writeup, error) {
	data, info, err := m.ReadFile(taskID, "WRITEUP.md")
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return platform.Writeup{Exists: false}, nil
		}
		return platform.Writeup{}, err
	}
	return platform.Writeup{
		Content:   string(data),
		UpdatedAt: info.ModTime,
		Exists:    true,
	}, nil
}

// SaveWriteup writes/updates the WRITEUP.md file.
func (m *Manager) SaveWriteup(taskID string, content string) error {
	return m.WriteFile(taskID, "WRITEUP.md", []byte(content))
}

// ExportZip creates a zip archive of the entire task workspace.
func (m *Manager) ExportZip(taskID string, w io.Writer) error {
	wsDir := m.WorkspacePath(taskID)
	if _, err := os.Stat(wsDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: workspace not initialized", ErrFileNotFound)
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	err := filepath.Walk(wsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == wsDir {
			return nil
		}
		rel, err := filepath.Rel(wsDir, path)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = slashRel
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}

		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(writer, f)
		return err
	})
	return err
}

func detectContentType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(buf[:n])
}

func detectContentTypeFromBytes(data []byte, filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	if len(data) == 0 {
		return "text/plain; charset=utf-8"
	}
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	// Check if pure ASCII/UTF-8 text
	if isText(sample) {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(sample)
}

func isText(b []byte) bool {
	if bytes.IndexByte(b, 0) != -1 {
		return false
	}
	return true
}
