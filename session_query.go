package agentkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultSessionPageSize 是未指定分页大小时返回的会话数量。
	DefaultSessionPageSize = 50
	// MaxSessionPageSize 防止一次查询意外加载过多会话元数据。
	MaxSessionPageSize = 200
)

var (
	// ErrInvalidSessionQuery 表示会话查询参数无效。
	ErrInvalidSessionQuery = errors.New("agentkit: invalid session query")
	// ErrInvalidSessionCursor 表示分页游标无效或已损坏。
	ErrInvalidSessionCursor = errors.New("agentkit: invalid session cursor")
)

// SessionQuery 描述一次会话目录查询。
// Tags 使用 AND 语义；Archived 为 nil 时同时返回归档和未归档会话。
type SessionQuery struct {
	OwnerID  string
	Tags     []string
	Archived *bool
	Limit    int
	Cursor   string
}

// SessionPage 是按更新时间从新到旧排列的一页会话。
// NextCursor 为空表示没有下一页；游标是不透明值，调用方不应解析或修改。
type SessionPage struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// SessionQueryStore 是 SessionStore 的可选高效查询扩展。
// 数据库实现应在存储端完成筛选和分页；未实现时 QuerySessions 会通过 List 兼容回退。
type SessionQueryStore interface {
	QuerySessions(ctx context.Context, query SessionQuery) (SessionPage, error)
}

var (
	_ SessionQueryStore = (*MemorySessionStore)(nil)
	_ SessionQueryStore = (*FileSessionStore)(nil)
)

// QuerySessions 查询任意 SessionStore。
// 支持 SessionQueryStore 的后端会直接执行查询，旧后端自动通过 List 兼容回退。
func QuerySessions(ctx context.Context, store SessionStore, query SessionQuery) (SessionPage, error) {
	if ctx == nil {
		return SessionPage{}, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return SessionPage{}, err
	}
	if store == nil {
		return SessionPage{}, errors.New("agentkit: session store is required")
	}
	query, cursor, err := validateSessionQuery(query)
	if err != nil {
		return SessionPage{}, err
	}
	if backend, ok := store.(SessionQueryStore); ok {
		page, err := callPersistence("session query", func() (SessionPage, error) {
			return backend.QuerySessions(ctx, query)
		})
		if err != nil {
			return SessionPage{}, err
		}
		return validateSessionPage(page, query, cursor)
	}
	infos, err := sessionStoreList(ctx, store)
	if err != nil {
		return SessionPage{}, err
	}
	sortSessionInfos(infos)
	return paginateSessionInfos(ctx, infos, query, cursor)
}

func validateSessionPage(page SessionPage, query SessionQuery, cursor *sessionPageCursor) (SessionPage, error) {
	if len(page.Sessions) > query.Limit {
		return SessionPage{}, fmt.Errorf("%w: session query returned %d entries for limit %d",
			ErrInvalidPersistenceData, len(page.Sessions), query.Limit)
	}
	cloned := SessionPage{NextCursor: page.NextCursor}
	if page.Sessions != nil {
		cloned.Sessions = make([]SessionInfo, len(page.Sessions))
	}
	seen := make(map[string]struct{}, len(page.Sessions))
	for index, info := range page.Sessions {
		if err := validateSessionInfo(info); err != nil {
			return SessionPage{}, fmt.Errorf("%w: invalid session query entry %d: %w",
				ErrInvalidPersistenceData, index, err)
		}
		if _, exists := seen[info.ID]; exists {
			return SessionPage{}, fmt.Errorf("%w: session query returned duplicate ID %q",
				ErrInvalidPersistenceData, info.ID)
		}
		seen[info.ID] = struct{}{}
		if !sessionMatchesQuery(info, query) || !sessionAfterCursor(info, cursor) {
			return SessionPage{}, fmt.Errorf("%w: session query entry %q does not match the request",
				ErrInvalidPersistenceData, info.ID)
		}
		if index > 0 && !sessionInfosOrdered(page.Sessions[index-1], info) {
			return SessionPage{}, fmt.Errorf("%w: session query entries are not ordered at ID %q",
				ErrInvalidPersistenceData, info.ID)
		}
		info.Tags = append([]string(nil), info.Tags...)
		cloned.Sessions[index] = info
	}
	if page.NextCursor == "" {
		return cloned, nil
	}
	if len(page.Sessions) == 0 {
		return SessionPage{}, fmt.Errorf("%w: empty session page has a next cursor", ErrInvalidPersistenceData)
	}
	_, next, err := validateSessionQuery(SessionQuery{Cursor: page.NextCursor})
	if err != nil {
		return SessionPage{}, fmt.Errorf("%w: invalid session query next cursor: %w",
			ErrInvalidPersistenceData, err)
	}
	last := page.Sessions[len(page.Sessions)-1]
	if next == nil || next.ID != last.ID || !next.UpdatedAt.Equal(last.UpdatedAt) {
		return SessionPage{}, fmt.Errorf("%w: session query next cursor does not match its last entry",
			ErrInvalidPersistenceData)
	}
	return cloned, nil
}

func validateSessionInfo(info SessionInfo) error {
	if err := validateManagedSessionID(info.ID); err != nil {
		return err
	}
	if info.CreatedAt.IsZero() {
		return errors.New("session creation time is required")
	}
	if info.UpdatedAt.IsZero() {
		return errors.New("session update time is required")
	}
	if info.MessageCount < 0 || info.ContextMessageCount < 0 || info.PendingInterruptCount < 0 {
		return errors.New("session counts must not be negative")
	}
	return nil
}

func sessionInfosOrdered(previous, current SessionInfo) bool {
	if previous.UpdatedAt.After(current.UpdatedAt) {
		return true
	}
	return previous.UpdatedAt.Equal(current.UpdatedAt) && previous.ID < current.ID
}

// QuerySessions 在内存快照上完成筛选和分页。
func (s *MemorySessionStore) QuerySessions(ctx context.Context, query SessionQuery) (SessionPage, error) {
	query, cursor, err := validateSessionQueryWithContext(ctx, query)
	if err != nil {
		return SessionPage{}, err
	}
	s.mu.RLock()
	infos := make([]SessionInfo, 0, len(s.sessions))
	for _, session := range s.sessions {
		infos = append(infos, sessionInfo(session))
	}
	s.mu.RUnlock()
	sortSessionInfos(infos)
	return paginateSessionInfos(ctx, infos, query, cursor)
}

// QuerySessions 查询文件存储中的会话。文件存储面向本地规模，会先读取元数据再分页。
func (s *FileSessionStore) QuerySessions(ctx context.Context, query SessionQuery) (SessionPage, error) {
	query, cursor, err := validateSessionQueryWithContext(ctx, query)
	if err != nil {
		return SessionPage{}, err
	}
	infos, err := s.List(ctx)
	if err != nil {
		return SessionPage{}, err
	}
	return paginateSessionInfos(ctx, infos, query, cursor)
}

type sessionPageCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

func validateSessionQueryWithContext(ctx context.Context, query SessionQuery) (SessionQuery, *sessionPageCursor, error) {
	if ctx == nil {
		return SessionQuery{}, nil, errors.New("agentkit: context is required")
	}
	if err := ctx.Err(); err != nil {
		return SessionQuery{}, nil, err
	}
	return validateSessionQuery(query)
}

func validateSessionQuery(query SessionQuery) (SessionQuery, *sessionPageCursor, error) {
	if query.Limit < 0 || query.Limit > MaxSessionPageSize {
		return SessionQuery{}, nil, fmt.Errorf("%w: limit must be between 0 and %d", ErrInvalidSessionQuery, MaxSessionPageSize)
	}
	if query.Limit == 0 {
		query.Limit = DefaultSessionPageSize
	}
	if query.OwnerID != strings.TrimSpace(query.OwnerID) {
		return SessionQuery{}, nil, fmt.Errorf("%w: owner ID must not have surrounding whitespace", ErrInvalidSessionQuery)
	}
	tags, err := normalizeSessionTags(query.Tags)
	if err != nil {
		return SessionQuery{}, nil, fmt.Errorf("%w: %v", ErrInvalidSessionQuery, err)
	}
	query.Tags = tags
	if query.Cursor == "" {
		return query, nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return SessionQuery{}, nil, fmt.Errorf("%w: %v", ErrInvalidSessionCursor, err)
	}
	var cursor sessionPageCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return SessionQuery{}, nil, fmt.Errorf("%w: %v", ErrInvalidSessionCursor, err)
	}
	if cursor.UpdatedAt.IsZero() || cursor.ID == "" {
		return SessionQuery{}, nil, ErrInvalidSessionCursor
	}
	return query, &cursor, nil
}

func paginateSessionInfos(
	ctx context.Context,
	infos []SessionInfo,
	query SessionQuery,
	cursor *sessionPageCursor,
) (SessionPage, error) {
	page := SessionPage{Sessions: make([]SessionInfo, 0, query.Limit)}
	hasMore := false
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if !sessionMatchesQuery(info, query) || !sessionAfterCursor(info, cursor) {
			continue
		}
		if len(page.Sessions) == query.Limit {
			hasMore = true
			break
		}
		info.Tags = append([]string(nil), info.Tags...)
		page.Sessions = append(page.Sessions, info)
	}
	if hasMore {
		last := page.Sessions[len(page.Sessions)-1]
		encoded, err := json.Marshal(sessionPageCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return SessionPage{}, fmt.Errorf("agentkit: encode session cursor: %w", err)
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func sessionMatchesQuery(info SessionInfo, query SessionQuery) bool {
	if query.OwnerID != "" && info.OwnerID != query.OwnerID {
		return false
	}
	if query.Archived != nil && info.Archived != *query.Archived {
		return false
	}
	if len(query.Tags) == 0 {
		return true
	}
	owned := make(map[string]struct{}, len(info.Tags))
	for _, tag := range info.Tags {
		owned[tag] = struct{}{}
	}
	for _, tag := range query.Tags {
		if _, ok := owned[tag]; !ok {
			return false
		}
	}
	return true
}

func sessionAfterCursor(info SessionInfo, cursor *sessionPageCursor) bool {
	if cursor == nil {
		return true
	}
	if info.UpdatedAt.Before(cursor.UpdatedAt) {
		return true
	}
	return info.UpdatedAt.Equal(cursor.UpdatedAt) && info.ID > cursor.ID
}

func normalizeSessionTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return nil, errors.New("session tag must not be blank")
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	return normalized, nil
}
