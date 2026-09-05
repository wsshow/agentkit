package agentkit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const sessionMetadataUpdateAttempts = 8

var (
	// ErrSessionManagerClosed 表示会话管理器已经关闭。
	ErrSessionManagerClosed = errors.New("agentkit: session manager is closed")
	// ErrSessionAlreadyExists 表示创建会话时指定的 ID 已经存在。
	ErrSessionAlreadyExists = errors.New("agentkit: session already exists")
	// ErrSessionAccessDenied 表示会话不属于当前管理器配置的 OwnerID。
	ErrSessionAccessDenied = errors.New("agentkit: session access denied")
	// ErrSessionFactoryPanic 表示自定义会话 Agent 工厂发生 panic。
	ErrSessionFactoryPanic = errors.New("agentkit: session agent factory panicked")
	// ErrSessionIDGeneratorPanic 表示自定义会话 ID 生成器发生 panic。
	ErrSessionIDGeneratorPanic = errors.New("agentkit: session ID generator panicked")
)

// SessionAgentFactory 为一个已存在的会话创建 Agent。
// 实现必须使用传入的 SessionConfig，返回的 Agent 必须绑定相同的会话 ID。
type SessionAgentFactory func(ctx context.Context, session SessionConfig) (*Agent, error)

// SessionManagerConfig 配置多会话管理器。
// AgentConfig 适合共享同一套 Agent 配置的常见场景；AgentFactory 用于按会话创建模型等高级场景，二者必须且只能配置一个。
type SessionManagerConfig struct {
	Store        SessionStore
	AgentConfig  *Config
	AgentFactory SessionAgentFactory
	OwnerID      string
	IDGenerator  func() string
}

// CreateSessionOptions 配置一个新会话。ID 为空时自动生成 UUID。
type CreateSessionOptions struct {
	ID      string
	Title   string
	OwnerID string
	Tags    []string
}

// SessionManager 管理多个相互隔离的会话 Agent。
// 同一管理器内，一个会话 ID 最多只有一个活动 Agent；不同会话可并发运行。
type SessionManager struct {
	store       SessionStore
	ownerID     string
	factory     SessionAgentFactory
	idGenerator func() string

	mu         sync.Mutex
	active     map[string]*Agent
	gates      map[string]*sessionOperationGate
	closed     bool
	closing    bool
	closeDone  chan struct{}
	closeErr   error
	operations sync.WaitGroup
}

type sessionOperationGate struct {
	token chan struct{}
	refs  int
}

// NewSessionManager 创建多会话管理器，不会连接模型或打开会话。
func NewSessionManager(cfg *SessionManagerConfig) (*SessionManager, error) {
	if cfg == nil {
		return nil, errors.New("agentkit: session manager config is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("agentkit: session store is required")
	}
	if cfg.OwnerID != strings.TrimSpace(cfg.OwnerID) {
		return nil, errors.New("agentkit: session manager owner ID must not have surrounding whitespace")
	}
	if (cfg.AgentConfig == nil) == (cfg.AgentFactory == nil) {
		return nil, errors.New("agentkit: session manager requires exactly one of agent config or agent factory")
	}

	factory := cfg.AgentFactory
	if cfg.AgentConfig != nil {
		if cfg.AgentConfig.Session != nil {
			return nil, errors.New("agentkit: session manager agent config must not set Session")
		}
		if cfg.AgentConfig.History != nil {
			return nil, errors.New("agentkit: session manager agent config must not set History")
		}
		if err := validateReusableSessionAgentConfig(cfg.AgentConfig); err != nil {
			return nil, err
		}
		template := *cfg.AgentConfig
		validation := template
		validation.Session = &SessionConfig{ID: "agentkit-manager-validation", Store: cfg.Store}
		if err := validateConfig(context.TODO(), &validation); err != nil {
			return nil, fmt.Errorf("agentkit: invalid session manager agent config: %w", err)
		}
		factory = func(ctx context.Context, session SessionConfig) (*Agent, error) {
			config := template
			config.Session = &session
			return New(ctx, &config)
		}
	}

	idGenerator := cfg.IDGenerator
	if idGenerator == nil {
		idGenerator = uuid.NewString
	}
	return &SessionManager{
		store:       cfg.Store,
		ownerID:     cfg.OwnerID,
		factory:     factory,
		idGenerator: idGenerator,
		active:      make(map[string]*Agent),
		gates:       make(map[string]*sessionOperationGate),
	}, nil
}

// Create 创建并打开一个使用自动 ID 的会话。
func (m *SessionManager) Create(ctx context.Context) (*Agent, error) {
	return m.CreateWithOptions(ctx, CreateSessionOptions{})
}

// CreateWithOptions 创建、持久化并打开一个会话。
// Agent 初始化失败时会话仍会保留，调用方可修复外部依赖后通过 Open 重试。
func (m *SessionManager) CreateWithOptions(ctx context.Context, options CreateSessionOptions) (*Agent, error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, err
	}
	defer m.operations.Done()

	session, err := m.newSession(options)
	if err != nil {
		return nil, err
	}
	release, err := m.acquireSession(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	agent, _, err := m.createAndOpen(ctx, session)
	return agent, err
}

// OpenOrCreate 打开指定会话；不存在时使用 options 创建它。
// created 仅在本次调用成功持久化新会话时为 true。ID 为空时总是生成新 ID。
func (m *SessionManager) OpenOrCreate(ctx context.Context, options CreateSessionOptions) (agent *Agent, created bool, err error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, false, err
	}
	defer m.operations.Done()

	if options.ID == "" {
		session, err := m.newSession(options)
		if err != nil {
			return nil, false, err
		}
		release, err := m.acquireSession(ctx, session.ID)
		if err != nil {
			return nil, false, err
		}
		defer release()
		return m.createAndOpen(ctx, session)
	}
	if err := validateManagedSessionID(options.ID); err != nil {
		return nil, false, err
	}
	release, err := m.acquireSession(ctx, options.ID)
	if err != nil {
		return nil, false, err
	}
	defer release()

	if active, err := m.usableActiveLocked(ctx, options.ID); err != nil {
		return nil, false, err
	} else if active != nil {
		return active, false, nil
	}
	stored, err := sessionStoreLoad(ctx, m.store, options.ID)
	if err == nil {
		if err := m.authorize(stored); err != nil {
			return nil, false, err
		}
		if stored.Archived {
			return nil, false, fmt.Errorf("%w: %s", ErrSessionArchived, options.ID)
		}
		agent, err := m.buildAgent(ctx, options.ID)
		if err != nil {
			return nil, false, err
		}
		m.mu.Lock()
		m.active[options.ID] = agent
		m.mu.Unlock()
		return agent, false, nil
	}
	if !errors.Is(err, ErrSessionNotFound) {
		return nil, false, err
	}
	session, err := m.newSession(options)
	if err != nil {
		return nil, false, err
	}
	return m.createAndOpen(ctx, session)
}

// Open 打开已存在的会话。同一管理器重复打开相同 ID 会返回同一个活动 Agent。
func (m *SessionManager) Open(ctx context.Context, id string) (*Agent, error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return nil, err
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.openLocked(ctx, id)
}

// Get 返回活动 Agent 的最新内存快照，或加载未打开会话的持久化快照。
func (m *SessionManager) Get(ctx context.Context, id string) (*Session, error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return nil, err
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.getLocked(ctx, id)
}

// List 按条件分页列出会话。配置 OwnerID 后，查询会被强制限制在该 Owner 内。
func (m *SessionManager) List(ctx context.Context, query SessionQuery) (SessionPage, error) {
	if err := m.beginOperation(ctx); err != nil {
		return SessionPage{}, err
	}
	defer m.operations.Done()
	if m.ownerID != "" {
		if query.OwnerID != "" && query.OwnerID != m.ownerID {
			return SessionPage{}, ErrSessionAccessDenied
		}
		query.OwnerID = m.ownerID
	}
	page, err := QuerySessions(ctx, m.store, query)
	if err != nil {
		return SessionPage{}, err
	}
	if m.ownerID != "" {
		for _, session := range page.Sessions {
			if session.OwnerID != m.ownerID {
				return SessionPage{}, fmt.Errorf("%w: query backend returned another owner's session", ErrSessionAccessDenied)
			}
		}
	}
	return page, nil
}

// UpdateMetadata 原子替换会话标题、OwnerID 和标签，并保留对话与运行状态。
func (m *SessionManager) UpdateMetadata(ctx context.Context, id string, metadata SessionMetadata) (*Session, error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return nil, err
	}
	metadata, err := normalizeSessionMetadata(metadata)
	if err != nil {
		return nil, err
	}
	if m.ownerID != "" {
		if metadata.OwnerID != "" && metadata.OwnerID != m.ownerID {
			return nil, ErrSessionAccessDenied
		}
		metadata.OwnerID = m.ownerID
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()

	agent, err := m.usableActiveLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent != nil {
		current := agent.Session()
		if err := m.authorize(current); err != nil {
			return nil, err
		}
		if err := agent.saveSession(ctx, &metadata); err != nil {
			return nil, err
		}
		return agent.Session(), nil
	}
	return m.updateStoredSession(ctx, id, func(session *Session) {
		session.SessionMetadata = cloneSessionMetadata(metadata)
	})
}

// Archive 关闭活动 Agent 并归档会话。归档会话在 Unarchive 前不能 Open。
func (m *SessionManager) Archive(ctx context.Context, id string) error {
	return m.setArchived(ctx, id, true)
}

// Unarchive 恢复归档会话，使其可以再次 Open。
func (m *SessionManager) Unarchive(ctx context.Context, id string) error {
	return m.setArchived(ctx, id, false)
}

// Fork 复制一个会话的对话历史和压缩上下文，并创建独立的新 Agent。
// 检查点、待处理中断、目标和大型工具结果不会复制。
func (m *SessionManager) Fork(ctx context.Context, sourceID string, options CreateSessionOptions) (*Agent, error) {
	if err := m.beginOperation(ctx); err != nil {
		return nil, err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(sourceID); err != nil {
		return nil, err
	}
	sourceRelease, err := m.acquireSession(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	source, err := m.getLocked(ctx, sourceID)
	sourceRelease()
	if err != nil {
		return nil, err
	}

	target, err := m.newSession(options)
	if err != nil {
		return nil, err
	}
	target.Messages = cloneHistoryMessages(source.Messages)
	target.Context = cloneHistoryMessages(source.Context)
	targetRelease, err := m.acquireSession(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	defer targetRelease()
	agent, _, err := m.createAndOpen(ctx, target)
	return agent, err
}

// CloseSession 关闭并移除一个活动 Agent，但保留持久化会话。重复调用是安全的。
func (m *SessionManager) CloseSession(ctx context.Context, id string) error {
	if err := m.beginOperation(ctx); err != nil {
		return err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return err
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	return m.closeActiveLocked(ctx, id)
}

// Delete 先关闭活动 Agent，再删除持久化会话及其配套资源。删除不存在的会话成功。
func (m *SessionManager) Delete(ctx context.Context, id string) error {
	if err := m.beginOperation(ctx); err != nil {
		return err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return err
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	session, err := sessionStoreLoad(ctx, m.store, id)
	if errors.Is(err, ErrSessionNotFound) {
		if m.ownerID != "" {
			return nil
		}
	} else if err != nil {
		return err
	} else if err := m.authorize(session); err != nil {
		return err
	}
	if err := m.closeActiveLocked(ctx, id); err != nil {
		return err
	}
	return sessionStoreDelete(ctx, m.store, id)
}

// ActiveSessionIDs 返回当前打开的会话 ID，按字典序排列。
func (m *SessionManager) ActiveSessionIDs() []string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id, agent := range m.active {
		if !agentCloseStarted(agent) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(ids)
	return ids
}

// Close 关闭管理器和全部活动 Agent。实现 io.Closer 接口。
func (m *SessionManager) Close() error {
	return m.CloseContext(context.Background())
}

// CloseContext 关闭管理器和全部活动 Agent，并等待资源释放或 ctx 结束。
// 等待超时后，关闭仍会在后台继续。关闭管理器不会删除任何持久化会话。
func (m *SessionManager) CloseContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	done := m.startClose()
	select {
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *SessionManager) newSession(options CreateSessionOptions) (*Session, error) {
	id := options.ID
	if id == "" {
		var err error
		id, err = callSessionIDGenerator(m.idGenerator)
		if err != nil {
			return nil, err
		}
	}
	if err := validateManagedSessionID(id); err != nil {
		return nil, err
	}
	metadata, err := normalizeSessionMetadata(SessionMetadata{
		Title: options.Title, OwnerID: options.OwnerID, Tags: options.Tags,
	})
	if err != nil {
		return nil, err
	}
	if m.ownerID != "" {
		if metadata.OwnerID != "" && metadata.OwnerID != m.ownerID {
			return nil, ErrSessionAccessDenied
		}
		metadata.OwnerID = m.ownerID
	}
	now := time.Now().UTC()
	return &Session{ID: id, SessionMetadata: metadata, CreatedAt: now, UpdatedAt: now}, nil
}

func (m *SessionManager) createAndOpen(ctx context.Context, session *Session) (*Agent, bool, error) {
	if active, err := m.usableActiveLocked(ctx, session.ID); err != nil {
		return nil, false, err
	} else if active != nil {
		return nil, false, fmt.Errorf("%w: %s", ErrSessionAlreadyExists, session.ID)
	}
	_, err := sessionStoreLoad(ctx, m.store, session.ID)
	if err == nil {
		return nil, false, fmt.Errorf("%w: %s", ErrSessionAlreadyExists, session.ID)
	}
	if !errors.Is(err, ErrSessionNotFound) {
		return nil, false, err
	}
	if err := sessionStoreSave(ctx, m.store, session); err != nil {
		if errors.Is(err, ErrSessionConflict) {
			return nil, false, errors.Join(fmt.Errorf("%w: %s", ErrSessionAlreadyExists, session.ID), err)
		}
		return nil, false, err
	}
	agent, err := m.buildAgent(ctx, session.ID)
	if err != nil {
		return nil, true, err
	}
	m.mu.Lock()
	m.active[session.ID] = agent
	m.mu.Unlock()
	return agent, true, nil
}

func (m *SessionManager) openLocked(ctx context.Context, id string) (*Agent, error) {
	active, err := m.usableActiveLocked(ctx, id)
	if err != nil || active != nil {
		return active, err
	}
	session, err := sessionStoreLoad(ctx, m.store, id)
	if err != nil {
		return nil, err
	}
	if err := m.authorize(session); err != nil {
		return nil, err
	}
	if session.Archived {
		return nil, fmt.Errorf("%w: %s", ErrSessionArchived, id)
	}
	agent, err := m.buildAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.active[id] = agent
	m.mu.Unlock()
	return agent, nil
}

func (m *SessionManager) buildAgent(ctx context.Context, id string) (agent *Agent, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if agent != nil {
				_ = agent.Close()
			}
			agent = nil
			err = fmt.Errorf("%w for session %q: %v", ErrSessionFactoryPanic, id, recovered)
		}
	}()
	agent, err = m.factory(ctx, SessionConfig{ID: id, Store: m.store})
	if err != nil {
		if agent != nil {
			_ = agent.Close()
		}
		return nil, fmt.Errorf("agentkit: open session %q: %w", id, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agentkit: session agent factory returned nil for %q", id)
	}
	snapshot := agent.Session()
	if snapshot == nil || snapshot.ID != id {
		_ = agent.Close()
		return nil, fmt.Errorf("agentkit: session agent factory returned an agent not bound to session %q", id)
	}
	if err := m.authorize(snapshot); err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("agentkit: session agent factory returned an unauthorized agent: %w", err)
	}
	if agentCloseStarted(agent) {
		_ = agent.Close()
		return nil, fmt.Errorf("agentkit: session agent factory returned a closed agent for %q", id)
	}
	return agent, nil
}

func (m *SessionManager) getLocked(ctx context.Context, id string) (*Session, error) {
	agent, err := m.usableActiveLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if agent != nil {
		session := agent.Session()
		if err := m.authorize(session); err != nil {
			return nil, err
		}
		return session, nil
	}
	session, err := sessionStoreLoad(ctx, m.store, id)
	if err != nil {
		return nil, err
	}
	if err := m.authorize(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (m *SessionManager) usableActiveLocked(ctx context.Context, id string) (*Agent, error) {
	m.mu.Lock()
	agent := m.active[id]
	m.mu.Unlock()
	if agent == nil {
		return nil, nil
	}
	if !agentCloseStarted(agent) {
		return agent, nil
	}
	if err := agent.CloseContext(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.active[id] == agent {
		delete(m.active, id)
	}
	m.mu.Unlock()
	return nil, nil
}

func (m *SessionManager) closeActiveLocked(ctx context.Context, id string) error {
	m.mu.Lock()
	agent := m.active[id]
	m.mu.Unlock()
	if agent == nil {
		return nil
	}
	if err := agent.CloseContext(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	if m.active[id] == agent {
		delete(m.active, id)
	}
	m.mu.Unlock()
	return nil
}

func (m *SessionManager) setArchived(ctx context.Context, id string, archived bool) error {
	if err := m.beginOperation(ctx); err != nil {
		return err
	}
	defer m.operations.Done()
	if err := validateManagedSessionID(id); err != nil {
		return err
	}
	release, err := m.acquireSession(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	current, err := m.getLocked(ctx, id)
	if err != nil {
		return err
	}
	if current.Archived == archived {
		return nil
	}
	if err := m.closeActiveLocked(ctx, id); err != nil {
		return err
	}
	_, err = m.updateStoredSession(ctx, id, func(session *Session) {
		session.Archived = archived
	})
	return err
}

func (m *SessionManager) updateStoredSession(
	ctx context.Context,
	id string,
	update func(*Session),
) (*Session, error) {
	var conflict error
	for range sessionMetadataUpdateAttempts {
		session, err := sessionStoreLoad(ctx, m.store, id)
		if err != nil {
			return nil, err
		}
		if err := m.authorize(session); err != nil {
			return nil, err
		}
		update(session)
		session.UpdatedAt = time.Now().UTC()
		if err := sessionStoreSave(ctx, m.store, session); err != nil {
			if errors.Is(err, ErrSessionConflict) {
				conflict = err
				continue
			}
			return nil, err
		}
		session.Revision++
		return cloneSession(session), nil
	}
	return nil, conflict
}

func (m *SessionManager) authorize(session *Session) error {
	if session == nil {
		return errors.New("agentkit: session is required")
	}
	if m.ownerID != "" && session.OwnerID != m.ownerID {
		return fmt.Errorf("%w: session %q", ErrSessionAccessDenied, session.ID)
	}
	return nil
}

func (m *SessionManager) beginOperation(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrSessionManagerClosed
	}
	m.operations.Add(1)
	return nil
}

func (m *SessionManager) acquireSession(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	gate := m.gates[id]
	if gate == nil {
		gate = &sessionOperationGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		m.gates[id] = gate
	}
	gate.refs++
	m.mu.Unlock()

	select {
	case <-gate.token:
		return func() {
			gate.token <- struct{}{}
			m.releaseSessionReference(id, gate)
		}, nil
	case <-ctx.Done():
		m.releaseSessionReference(id, gate)
		return nil, ctx.Err()
	}
}

func (m *SessionManager) releaseSessionReference(id string, gate *sessionOperationGate) {
	m.mu.Lock()
	gate.refs--
	if gate.refs == 0 && m.gates[id] == gate {
		delete(m.gates, id)
	}
	m.mu.Unlock()
}

func (m *SessionManager) startClose() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closing {
		m.closing = true
		m.closed = true
		m.closeDone = make(chan struct{})
		go m.finishClose()
	}
	return m.closeDone
}

func (m *SessionManager) finishClose() {
	m.operations.Wait()
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	agents := make([]*Agent, len(ids))
	for index, id := range ids {
		agents[index] = m.active[id]
		delete(m.active, id)
	}
	m.mu.Unlock()

	errs := make([]error, len(agents))
	var wait sync.WaitGroup
	wait.Add(len(agents))
	for index, agent := range agents {
		go func() {
			defer wait.Done()
			if err := agent.Close(); err != nil {
				errs[index] = fmt.Errorf("agentkit: close session %q: %w", ids[index], err)
			}
		}()
	}
	wait.Wait()

	m.mu.Lock()
	m.closeErr = errors.Join(errs...)
	close(m.closeDone)
	m.mu.Unlock()
}

func validateManagedSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("agentkit: session ID is required")
	}
	if id != strings.TrimSpace(id) {
		return errors.New("agentkit: session ID must not have surrounding whitespace")
	}
	return nil
}

func normalizeSessionMetadata(metadata SessionMetadata) (SessionMetadata, error) {
	metadata.Title = strings.TrimSpace(metadata.Title)
	if metadata.OwnerID != strings.TrimSpace(metadata.OwnerID) {
		return SessionMetadata{}, errors.New("agentkit: session owner ID must not have surrounding whitespace")
	}
	tags, err := normalizeSessionTags(metadata.Tags)
	if err != nil {
		return SessionMetadata{}, fmt.Errorf("agentkit: %w", err)
	}
	metadata.Tags = tags
	return metadata, nil
}

func callSessionIDGenerator(generator func() string) (id string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			id = ""
			err = fmt.Errorf("%w: %v", ErrSessionIDGeneratorPanic, recovered)
		}
	}()
	id = generator()
	return id, nil
}

func agentCloseStarted(agent *Agent) bool {
	agent.closeMu.Lock()
	defer agent.closeMu.Unlock()
	return agent.closeStarted
}

func validateReusableSessionAgentConfig(config *Config) error {
	if hasOwnedMCPSession(config.MCP) {
		return errors.New("agentkit: session manager agent config cannot reuse an owned MCP session; use transport settings or AgentFactory")
	}
	for _, subAgent := range config.SubAgents {
		if hasOwnedMCPSession(subAgent.MCP) {
			return fmt.Errorf("agentkit: session manager sub-agent %q cannot reuse an owned MCP session; use transport settings or AgentFactory", subAgent.Name)
		}
	}
	return nil
}

func hasOwnedMCPSession(config *MCPConfig) bool {
	if config == nil {
		return false
	}
	for _, server := range config.Servers {
		if server.Session != nil {
			return true
		}
	}
	return false
}
