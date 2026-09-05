package agentkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	einoskill "github.com/cloudwego/eino/adk/middlewares/skill"
	"gopkg.in/yaml.v3"
)

const (
	skillFileName           = "SKILL.md"
	defaultMaxSkillFileSize = 1 << 20
)

var (
	// ErrSkillNotFound 表示指定技能不存在。
	ErrSkillNotFound = errors.New("agentkit: skill not found")
	// ErrSkillBackendPanic 表示自定义 SkillBackend 发生 panic。
	ErrSkillBackendPanic = errors.New("agentkit: skill backend panicked")
)

// Skill 是从 SKILL.md 加载的技能。
type Skill = einoskill.Skill

// SkillInfo 是技能 YAML frontmatter 中的名称和描述等元数据。
type SkillInfo = einoskill.FrontMatter

// SkillBackend 提供技能列表和按名称加载能力。
type SkillBackend = einoskill.Backend

// SkillsConfig 配置 Agent 可按需加载的技能。
// Paths 与 Backend 二选一；Paths 可指向 SKILL.md、单个技能目录或技能集合目录。
type SkillsConfig struct {
	Paths    []string
	Backend  SkillBackend
	ToolName string // 模型用于加载技能的工具名称，默认 "skill"
}

// FileSkillBackend 从本地 SKILL.md 文件动态加载技能。
// 每次 List/Get 都重新读取文件，因此开发时修改技能无需重建 Agent。
type FileSkillBackend struct {
	paths []string
}

var _ SkillBackend = (*FileSkillBackend)(nil)

// NewFileSkillBackend 创建本地技能后端。
// 每个路径可以是 SKILL.md、包含该文件的目录，或包含多个技能子目录的目录。
func NewFileSkillBackend(paths ...string) (*FileSkillBackend, error) {
	if len(paths) == 0 {
		return nil, errors.New("agentkit: at least one skill path is required")
	}
	absPaths := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("agentkit: skill path must not be empty")
		}
		absPath, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("agentkit: resolve skill path %q: %w", path, err)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("agentkit: access skill path %q: %w", path, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agentkit: skill path must be a directory or regular file: %s", path)
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}
		absPaths = append(absPaths, absPath)
	}
	return &FileSkillBackend{paths: absPaths}, nil
}

// List 列出所有技能元数据，结果按名称排序。
func (b *FileSkillBackend) List(ctx context.Context) ([]SkillInfo, error) {
	skills, err := b.load(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]SkillInfo, len(skills))
	for i := range skills {
		infos[i] = skills[i].FrontMatter
	}
	return infos, nil
}

// Get 按 frontmatter 中的 name 加载技能。
func (b *FileSkillBackend) Get(ctx context.Context, name string) (Skill, error) {
	if strings.TrimSpace(name) == "" {
		return Skill{}, errors.New("agentkit: skill name is required")
	}
	skills, err := b.load(ctx)
	if err != nil {
		return Skill{}, err
	}
	for _, loaded := range skills {
		if loaded.Name == name {
			return loaded, nil
		}
	}
	return Skill{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
}

func (b *FileSkillBackend) load(ctx context.Context) ([]Skill, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	files := make(map[string]struct{})
	for _, path := range b.paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("agentkit: access skill path %q: %w", path, err)
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("agentkit: skill path must be a directory or regular file: %s", path)
			}
			if filepath.Base(path) != skillFileName {
				return nil, fmt.Errorf("agentkit: skill file must be named %s: %s", skillFileName, path)
			}
			files[path] = struct{}{}
			continue
		}
		if err := discoverSkillFiles(path, files); err != nil {
			return nil, err
		}
	}

	filePaths := make([]string, 0, len(files))
	for path := range files {
		filePaths = append(filePaths, path)
	}
	sort.Strings(filePaths)

	skills := make([]Skill, 0, len(filePaths))
	byName := make(map[string]string, len(filePaths))
	for _, path := range filePaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		loaded, err := loadSkillFile(path)
		if err != nil {
			return nil, err
		}
		if previous, ok := byName[loaded.Name]; ok {
			return nil, fmt.Errorf("agentkit: duplicate skill name %q in %s and %s", loaded.Name, previous, path)
		}
		byName[loaded.Name] = path
		skills = append(skills, loaded)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

func discoverSkillFiles(dir string, files map[string]struct{}) error {
	rootFile := filepath.Join(dir, skillFileName)
	if info, err := os.Stat(rootFile); err == nil && info.Mode().IsRegular() {
		files[rootFile] = struct{}{}
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentkit: access skill file %q: %w", rootFile, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("agentkit: read skill directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), skillFileName)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("agentkit: access skill file %q: %w", path, err)
		}
		if info.Mode().IsRegular() {
			files[path] = struct{}{}
		}
	}
	return nil
}

func loadSkillFile(path string) (Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return Skill{}, fmt.Errorf("agentkit: open skill file %q: %w", path, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, defaultMaxSkillFileSize+1))
	if err != nil {
		return Skill{}, fmt.Errorf("agentkit: read skill file %q: %w", path, err)
	}
	if len(data) > defaultMaxSkillFileSize {
		return Skill{}, fmt.Errorf("agentkit: skill file exceeds %d bytes: %s", defaultMaxSkillFileSize, path)
	}

	frontmatter, content, err := parseSkillDocument(string(data))
	if err != nil {
		return Skill{}, fmt.Errorf("agentkit: parse skill file %q: %w", path, err)
	}
	var info SkillInfo
	if err := yaml.Unmarshal([]byte(frontmatter), &info); err != nil {
		return Skill{}, fmt.Errorf("agentkit: parse skill frontmatter %q: %w", path, err)
	}
	info.Name = strings.TrimSpace(info.Name)
	info.Description = strings.TrimSpace(info.Description)
	content = strings.TrimSpace(content)
	if info.Name == "" {
		return Skill{}, fmt.Errorf("agentkit: skill name is required: %s", path)
	}
	if info.Description == "" {
		return Skill{}, fmt.Errorf("agentkit: skill description is required: %s", path)
	}
	if content == "" {
		return Skill{}, fmt.Errorf("agentkit: skill instructions are required: %s", path)
	}
	return Skill{
		FrontMatter:   info,
		Content:       content,
		BaseDirectory: filepath.Dir(path),
	}, nil
}

func parseSkillDocument(document string) (frontmatter, content string, err error) {
	document = strings.ReplaceAll(document, "\r\n", "\n")
	lines := strings.Split(document, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("document must start with YAML frontmatter delimiter ---")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return "", "", errors.New("YAML frontmatter closing delimiter --- is missing")
}

// MemorySkillBackend 是可动态增删技能的并发安全内存后端。
type MemorySkillBackend struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

var _ SkillBackend = (*MemorySkillBackend)(nil)

// NewMemorySkillBackend 创建内存技能后端。
func NewMemorySkillBackend(skills ...Skill) (*MemorySkillBackend, error) {
	backend := &MemorySkillBackend{skills: make(map[string]Skill, len(skills))}
	for _, item := range skills {
		if err := backend.Set(item); err != nil {
			return nil, err
		}
	}
	return backend, nil
}

// Set 新增或替换一个技能。
func (b *MemorySkillBackend) Set(item Skill) error {
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Content = strings.TrimSpace(item.Content)
	if item.Name == "" {
		return errors.New("agentkit: skill name is required")
	}
	if item.Description == "" {
		return errors.New("agentkit: skill description is required")
	}
	if item.Content == "" {
		return errors.New("agentkit: skill instructions are required")
	}
	b.mu.Lock()
	if b.skills == nil {
		b.skills = make(map[string]Skill)
	}
	b.skills[item.Name] = item
	b.mu.Unlock()
	return nil
}

// Delete 删除技能。技能不存在时也返回 nil。
func (b *MemorySkillBackend) Delete(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("agentkit: skill name is required")
	}
	b.mu.Lock()
	delete(b.skills, name)
	b.mu.Unlock()
	return nil
}

// List 列出所有技能元数据，结果按名称排序。
func (b *MemorySkillBackend) List(ctx context.Context) ([]SkillInfo, error) {
	if ctx == nil {
		return nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	infos := make([]SkillInfo, 0, len(b.skills))
	for _, item := range b.skills {
		infos = append(infos, item.FrontMatter)
	}
	b.mu.RUnlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// Get 按名称加载技能。
func (b *MemorySkillBackend) Get(ctx context.Context, name string) (Skill, error) {
	if ctx == nil {
		return Skill{}, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Skill{}, errors.New("agentkit: skill name is required")
	}
	b.mu.RLock()
	item, ok := b.skills[name]
	b.mu.RUnlock()
	if !ok {
		return Skill{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	return item, nil
}

func validateSkillsConfig(cfg *SkillsConfig) error {
	if cfg == nil {
		return nil
	}
	hasPaths := len(cfg.Paths) > 0
	hasBackend := cfg.Backend != nil
	if hasPaths == hasBackend {
		return errors.New("agentkit: skills require exactly one of paths or backend")
	}
	if cfg.ToolName != "" && strings.TrimSpace(cfg.ToolName) == "" {
		return errors.New("agentkit: skill tool name must not be blank")
	}
	if cfg.ToolName != "" {
		if err := validateToolName(cfg.ToolName); err != nil {
			return fmt.Errorf("agentkit: invalid skill tool name %q: %w", cfg.ToolName, err)
		}
	}
	return nil
}

type validatingSkillBackend struct {
	backend SkillBackend
}

func (b *validatingSkillBackend) List(ctx context.Context) (infos []SkillInfo, err error) {
	defer recoverSkillBackendPanic("List", &err)
	infos, err = b.backend.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		if err := validateSkillInfo(info); err != nil {
			return nil, err
		}
		if _, ok := seen[info.Name]; ok {
			return nil, fmt.Errorf("agentkit: duplicate skill name %q", info.Name)
		}
		seen[info.Name] = struct{}{}
	}
	return infos, nil
}

func (b *validatingSkillBackend) Get(ctx context.Context, name string) (item Skill, err error) {
	defer recoverSkillBackendPanic("Get", &err)
	item, err = b.backend.Get(ctx, name)
	if err != nil {
		return Skill{}, err
	}
	if err := validateSkillInfo(item.FrontMatter); err != nil {
		return Skill{}, err
	}
	if item.Name != name {
		return Skill{}, fmt.Errorf("agentkit: skill backend returned %q for requested skill %q", item.Name, name)
	}
	if strings.TrimSpace(item.Content) == "" {
		return Skill{}, fmt.Errorf("agentkit: skill %q instructions are required", item.Name)
	}
	return item, nil
}

func recoverSkillBackendPanic(operation string, err *error) {
	if value := recover(); value != nil {
		*err = fmt.Errorf("%w in %s: %v", ErrSkillBackendPanic, operation, value)
	}
}

func validateSkillInfo(info SkillInfo) error {
	if strings.TrimSpace(info.Name) == "" {
		return errors.New("agentkit: skill name is required")
	}
	if strings.TrimSpace(info.Description) == "" {
		return fmt.Errorf("agentkit: skill %q description is required", info.Name)
	}
	if info.Name != strings.TrimSpace(info.Name) {
		return fmt.Errorf("agentkit: skill name must not have surrounding whitespace: %q", info.Name)
	}
	if info.Context != "" || info.Agent != "" || info.Model != "" {
		return fmt.Errorf("agentkit: skill %q requests fork, agent, or model overrides; configure advanced Eino skill middleware through Handlers", info.Name)
	}
	return nil
}

func newSkillsMiddleware(ctx context.Context, cfg *SkillsConfig) (ChatModelAgentMiddleware, error) {
	backend := cfg.Backend
	if backend == nil {
		var err error
		backend, err = NewFileSkillBackend(cfg.Paths...)
		if err != nil {
			return nil, err
		}
	}
	backend = &validatingSkillBackend{backend: backend}
	infos, err := backend.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("agentkit: list skills: %w", err)
	}
	if len(infos) == 0 {
		return nil, errors.New("agentkit: no skills found")
	}

	var toolName *string
	if cfg.ToolName != "" {
		name := cfg.ToolName
		toolName = &name
	}
	middleware, err := einoskill.NewMiddleware(ctx, &einoskill.Config{
		Backend:       backend,
		SkillToolName: toolName,
	})
	if err != nil {
		return nil, fmt.Errorf("agentkit: configure skills: %w", err)
	}
	return middleware, nil
}
