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

const fileGoalVersion = 1

type storedGoal struct {
	Version int   `json:"version"`
	Goal    *Goal `json:"goal"`
}

// FileGoalStore 将每个目标原子地保存为一个 JSON 文件。
// 它适合本地应用和单进程服务；多进程写入请实现数据库型 GoalStore。
type FileGoalStore struct {
	dir string
	mu  sync.RWMutex
}

var _ GoalStore = (*FileGoalStore)(nil)

// NewFileGoalStore 创建文件目标存储。目录不存在时会自动创建。
func NewFileGoalStore(dir string) (*FileGoalStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("agentkit: goal directory is required")
	}
	cleanDir := filepath.Clean(dir)
	if err := os.MkdirAll(cleanDir, 0o700); err != nil {
		return nil, fmt.Errorf("agentkit: create goal directory: %w", err)
	}
	return &FileGoalStore{dir: cleanDir}, nil
}

// Load 从文件加载目标快照。
func (s *FileGoalStore) Load(ctx context.Context, id string) (*Goal, error) {
	if err := validateGoalContextAndID(ctx, id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
}

// Save 通过原子文件替换保存目标快照。
func (s *FileGoalStore) Save(ctx context.Context, goal *Goal) error {
	if err := validateGoal(ctx, goal); err != nil {
		return err
	}
	data, err := json.MarshalIndent(storedGoal{
		Version: fileGoalVersion,
		Goal:    normalizedGoal(goal),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("agentkit: encode goal %q: %w", goal.ID, err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	temp, err := os.CreateTemp(s.dir, ".goal-*.tmp")
	if err != nil {
		return fmt.Errorf("agentkit: create temporary goal file: %w", err)
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
		return fmt.Errorf("agentkit: write goal %q: %w", goal.ID, err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("agentkit: sync goal %q: %w", goal.ID, err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("agentkit: close goal %q: %w", goal.ID, err)
	}
	if err = os.Rename(tempName, s.path(goal.ID)); err != nil {
		return fmt.Errorf("agentkit: commit goal %q: %w", goal.ID, err)
	}
	committed = true
	return nil
}

// Delete 删除目标文件。目标不存在时也返回 nil。
func (s *FileGoalStore) Delete(ctx context.Context, id string) error {
	if err := validateGoalContextAndID(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: delete goal %q: %w", id, err)
	}
	return nil
}

// List 按更新时间从新到旧列出目标。
func (s *FileGoalStore) List(ctx context.Context) ([]GoalInfo, error) {
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
		return nil, fmt.Errorf("agentkit: list goals: %w", err)
	}
	infos := make([]GoalInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return infos, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		goal, err := s.loadPath(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return infos, err
		}
		infos = append(infos, goalInfo(goal))
	}
	sortGoalInfos(infos)
	return infos, nil
}

func (s *FileGoalStore) load(id string) (*Goal, error) {
	goal, err := s.loadPath(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrGoalNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if goal.ID != id {
		return nil, fmt.Errorf("agentkit: goal file ID mismatch: got %q, want %q", goal.ID, id)
	}
	return goal, nil
}

func (s *FileGoalStore) loadPath(path string) (*Goal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stored storedGoal
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("agentkit: decode goal file %q: %w", filepath.Base(path), err)
	}
	if stored.Version != fileGoalVersion {
		return nil, fmt.Errorf("agentkit: unsupported goal file version %d", stored.Version)
	}
	if stored.Goal == nil {
		return nil, fmt.Errorf("agentkit: invalid goal file %q", filepath.Base(path))
	}
	if err := validateGoal(context.TODO(), stored.Goal); err != nil {
		return nil, fmt.Errorf("agentkit: invalid goal file %q: %w", filepath.Base(path), err)
	}
	return cloneGoal(stored.Goal), nil
}

func (s *FileGoalStore) path(id string) string {
	return filepath.Join(s.dir, sessionStorageKey(id)+".json")
}
