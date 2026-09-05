package agentkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

type goalRunContextKey struct{}

// GoalRunInfo 描述当前工具调用所属的持久化目标尝试。
type GoalRunInfo struct {
	GoalID    string
	SessionID string
	Attempt   int
}

// CurrentGoalRun 返回当前工具调用所属的 Goal 信息。
// 普通 Agent 运行或没有 GoalRunner 上下文时返回 false。
func CurrentGoalRun(ctx context.Context) (GoalRunInfo, bool) {
	if ctx == nil {
		return GoalRunInfo{}, false
	}
	info, ok := ctx.Value(goalRunContextKey{}).(GoalRunInfo)
	if !ok || info.GoalID == "" || info.SessionID == "" || info.Attempt <= 0 {
		return GoalRunInfo{}, false
	}
	return info, true
}

// GoalOperationKey 为当前目标尝试和业务操作名生成稳定、不透明的幂等键。
// 同一目标尝试在进程恢复或显式 Retry 后会得到相同结果；operation 必须稳定且非空。
func GoalOperationKey(ctx context.Context, operation string) (string, bool) {
	info, ok := CurrentGoalRun(ctx)
	operation = strings.TrimSpace(operation)
	if !ok || operation == "" {
		return "", false
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(info.SessionID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(info.GoalID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(info.Attempt)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(operation))
	return hex.EncodeToString(hash.Sum(nil)), true
}

func withGoalRunContext(ctx context.Context, goal *Goal) context.Context {
	return context.WithValue(ctx, goalRunContextKey{}, GoalRunInfo{
		GoalID: goal.ID, SessionID: goal.SessionID, Attempt: goal.AttemptIteration,
	})
}
