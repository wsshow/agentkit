package agentkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileCheckpointStore 将每个检查点原子地保存为一个二进制文件。
// 它适合本地应用和单进程服务；多进程写入请实现数据库型 CheckpointStore。
type FileCheckpointStore struct {
	dir string
	mu  *sync.RWMutex
}

var (
	_ CheckpointStore   = (*FileCheckpointStore)(nil)
	_ CheckpointDeleter = (*FileCheckpointStore)(nil)
)

// NewFileCheckpointStore 创建文件检查点存储。目录不存在时会自动创建。
func NewFileCheckpointStore(dir string) (*FileCheckpointStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("agentkit: checkpoint directory is required")
	}
	cleanDir := filepath.Clean(dir)
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentkit: create checkpoint directory: %w", err)
	}
	mu, err := fileStoreDirectoryLock(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("agentkit: resolve checkpoint directory: %w", err)
	}
	return &FileCheckpointStore{dir: cleanDir, mu: mu}, nil
}

// Set 通过原子文件替换保存检查点。
func (s *FileCheckpointStore) Set(ctx context.Context, id string, value []byte) error {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	temp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("agentkit: create temporary checkpoint file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()

	if _, err = temp.Write(value); err != nil {
		return fmt.Errorf("agentkit: write checkpoint %q: %w", id, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("agentkit: sync checkpoint %q: %w", id, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("agentkit: close checkpoint %q: %w", id, err)
	}
	if err = os.Rename(tempName, s.path(id)); err != nil {
		return fmt.Errorf("agentkit: commit checkpoint %q: %w", id, err)
	}
	committed = true
	if err = syncFileStoreDirectory(s.dir); err != nil {
		return fmt.Errorf("agentkit: sync checkpoint directory: %w", err)
	}
	return nil
}

// Get 加载检查点。检查点不存在时 existed 为 false。
func (s *FileCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	data, err := os.ReadFile(s.path(id))
	s.mu.RUnlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("agentkit: read checkpoint %q: %w", id, err)
	}
	return data, true, nil
}

// Delete 删除检查点。检查点不存在时也返回 nil。
func (s *FileCheckpointStore) Delete(ctx context.Context, id string) error {
	if err := validateCheckpointContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("agentkit: delete checkpoint %q: %w", id, err)
	}
	if err := syncFileStoreDirectory(s.dir); err != nil {
		return fmt.Errorf("agentkit: sync checkpoint directory: %w", err)
	}
	return nil
}

func (s *FileCheckpointStore) path(id string) string {
	return filepath.Join(s.dir, sessionStorageKey(id)+".checkpoint")
}
