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

const fileSessionVersion = 1

type storedSession struct {
	Version int      `json:"version"`
	Session *Session `json:"session"`
}

// FileSessionStore 将每个会话原子地保存为一个 JSON 文件。
// 它适合本地应用和单进程服务；多进程写入请实现数据库型 SessionStore。
type FileSessionStore struct {
	dir         string
	mu          sync.RWMutex
	checkpoints *FileCheckpointStore
	goals       *FileGoalStore
}

var (
	_ SessionStore            = (*FileSessionStore)(nil)
	_ CheckpointStoreProvider = (*FileSessionStore)(nil)
	_ GoalStoreProvider       = (*FileSessionStore)(nil)
)

// NewFileSessionStore 创建文件会话存储。目录不存在时会自动创建。
func NewFileSessionStore(dir string) (*FileSessionStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("agentkit: session directory is required")
	}
	cleanDir := filepath.Clean(dir)
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentkit: create session directory: %w", err)
	}
	checkpoints, err := NewFileCheckpointStore(filepath.Join(cleanDir, ".checkpoints"))
	if err != nil {
		return nil, err
	}
	goals, err := NewFileGoalStore(filepath.Join(cleanDir, ".goals"))
	if err != nil {
		return nil, err
	}
	return &FileSessionStore{dir: cleanDir, checkpoints: checkpoints, goals: goals}, nil
}

// CheckpointStore 返回与会话目录配套的文件检查点存储。
func (s *FileSessionStore) CheckpointStore() CheckpointStore {
	return s.checkpoints
}

// GoalStore 返回与会话目录配套的文件目标存储。
func (s *FileSessionStore) GoalStore() GoalStore {
	return s.goals
}

// Load 从文件加载会话快照。
func (s *FileSessionStore) Load(ctx context.Context, id string) (*Session, error) {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
}

// Save 通过原子文件替换保存会话快照。
func (s *FileSessionStore) Save(ctx context.Context, session *Session) error {
	if err := validateSession(ctx, session); err != nil {
		return err
	}

	data, err := json.MarshalIndent(storedSession{
		Version: fileSessionVersion,
		Session: normalizedSession(session),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("agentkit: encode session %q: %w", session.ID, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	temp, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("agentkit: create temporary session file: %w", err)
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
		return fmt.Errorf("agentkit: write session %q: %w", session.ID, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("agentkit: sync session %q: %w", session.ID, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("agentkit: close session %q: %w", session.ID, err)
	}
	if err = os.Rename(tempName, s.path(session.ID)); err != nil {
		return fmt.Errorf("agentkit: commit session %q: %w", session.ID, err)
	}
	committed = true
	return nil
}

// Delete 删除会话文件。会话不存在时也返回 nil。
func (s *FileSessionStore) Delete(ctx context.Context, id string) error {
	if err := validateSessionContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	session, loadErr := s.load(id)
	if errors.Is(loadErr, ErrSessionNotFound) {
		loadErr = nil
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.mu.Unlock()
		return fmt.Errorf("agentkit: delete session %q: %w", id, err)
	}
	s.mu.Unlock()

	var checkpointErr error
	if session != nil && session.CheckpointID != "" {
		checkpointErr = s.checkpoints.Delete(ctx, session.CheckpointID)
	}
	return errors.Join(loadErr, checkpointErr)
}

// List 按更新时间从新到旧列出会话。
func (s *FileSessionStore) List(ctx context.Context) ([]SessionInfo, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("agentkit: list sessions: %w", err)
	}

	infos := make([]SessionInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return infos, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		session, err := s.loadPath(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return infos, err
		}
		infos = append(infos, sessionInfo(session))
	}
	sortSessionInfos(infos)
	return infos, nil
}

func (s *FileSessionStore) load(id string) (*Session, error) {
	session, err := s.loadPath(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if session.ID != id {
		return nil, fmt.Errorf("agentkit: session file ID mismatch: got %q, want %q", session.ID, id)
	}
	return session, nil
}

func (s *FileSessionStore) loadPath(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("agentkit: decode session file %q: %w", filepath.Base(path), err)
	}
	if stored.Version != fileSessionVersion {
		return nil, fmt.Errorf("agentkit: unsupported session file version %d", stored.Version)
	}
	if stored.Session == nil || stored.Session.ID == "" {
		return nil, fmt.Errorf("agentkit: invalid session file %q", filepath.Base(path))
	}
	return cloneSession(stored.Session), nil
}

func (s *FileSessionStore) path(id string) string {
	return filepath.Join(s.dir, sessionStorageKey(id)+".json")
}
