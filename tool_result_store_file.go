package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const fileToolResultVersion = 1

type storedToolResultFile struct {
	Version int               `json:"version"`
	Result  *StoredToolResult `json:"result"`
}

// FileToolResultStore 将大型工具结果原子地保存为 JSON 文件。
// 它适合本地应用和单进程服务；多进程写入请实现数据库型 ToolResultStore。
type FileToolResultStore struct {
	dir string
	mu  sync.RWMutex
}

var _ ToolResultStore = (*FileToolResultStore)(nil)

// NewFileToolResultStore 创建文件结果存储。目录不存在时会自动创建。
func NewFileToolResultStore(dir string) (*FileToolResultStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("agentkit: tool result directory is required")
	}
	cleanDir := filepath.Clean(dir)
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentkit: create tool result directory: %w", err)
	}
	return &FileToolResultStore{dir: cleanDir}, nil
}

// Load 从文件加载完整工具结果。
func (s *FileToolResultStore) Load(ctx context.Context, id string) (*StoredToolResult, error) {
	if err := validateToolResultContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
}

// Save 原子地创建一个不可变工具结果文件。
func (s *FileToolResultStore) Save(ctx context.Context, result *StoredToolResult) error {
	if err := validateStoredToolResult(ctx, result); err != nil {
		return err
	}
	data, err := json.MarshalIndent(storedToolResultFile{
		Version: fileToolResultVersion,
		Result:  normalizedStoredToolResult(result),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("agentkit: encode tool result %q: %w", result.ID, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path(result.ID)); err == nil {
		return fmt.Errorf("%w: %s", ErrToolResultExists, result.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: inspect tool result %q: %w", result.ID, err)
	}
	temp, err := os.CreateTemp(s.dir, ".tool-result-*.tmp")
	if err != nil {
		return fmt.Errorf("agentkit: create temporary tool result file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if _, err = temp.Write(data); err != nil {
		return fmt.Errorf("agentkit: write tool result %q: %w", result.ID, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("agentkit: sync tool result %q: %w", result.ID, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("agentkit: close tool result %q: %w", result.ID, err)
	}
	if err = os.Rename(tempName, s.path(result.ID)); err != nil {
		return fmt.Errorf("agentkit: commit tool result %q: %w", result.ID, err)
	}
	committed = true
	return nil
}

// Delete 删除结果文件；结果不存在时也返回 nil。
func (s *FileToolResultStore) Delete(ctx context.Context, id string) error {
	if err := validateToolResultContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: delete tool result %q: %w", id, err)
	}
	return nil
}

// List 按创建时间从新到旧列出结果。
func (s *FileToolResultStore) List(ctx context.Context) ([]ToolResultInfo, error) {
	if err := validateToolResultContext(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("agentkit: list tool results: %w", err)
	}
	infos := make([]ToolResultInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return infos, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		result, err := s.loadPath(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return infos, err
		}
		infos = append(infos, storedToolResultInfo(result))
	}
	sortToolResultInfos(infos)
	return infos, nil
}

func (s *FileToolResultStore) load(id string) (*StoredToolResult, error) {
	result, err := s.loadPath(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrToolResultNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if result.ID != id {
		return nil, fmt.Errorf("agentkit: tool result file ID mismatch: got %q, want %q", result.ID, id)
	}
	return result, nil
}

func (s *FileToolResultStore) loadPath(path string) (*StoredToolResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stored storedToolResultFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("agentkit: decode tool result file %q: %w", filepath.Base(path), err)
	}
	if stored.Version != fileToolResultVersion {
		return nil, fmt.Errorf("agentkit: unsupported tool result file version %d", stored.Version)
	}
	if stored.Result == nil {
		return nil, fmt.Errorf("agentkit: invalid tool result file %q", filepath.Base(path))
	}
	if err := validateStoredToolResult(context.TODO(), stored.Result); err != nil {
		return nil, fmt.Errorf("agentkit: invalid tool result file %q: %w", filepath.Base(path), err)
	}
	return cloneStoredToolResult(stored.Result), nil
}

func (s *FileToolResultStore) path(id string) string {
	return filepath.Join(s.dir, sessionStorageKey(id)+".json")
}
