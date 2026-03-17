package agentkit

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// Interrupt 在工具执行中触发 HITL 中断。
// 调用后工具应立即返回，Agent 将暂停执行并发出 EventInterrupted 事件。
// 用户可通过 Agent.Resume 恢复执行。
//
// info 是面向用户的中断原因描述，会通过 InterruptPoint.Info 传递给订阅者。
//
// 用法：
//
//	func myTool(ctx context.Context, input string) (string, error) {
//	    if needsConfirmation(input) {
//	        return "", agentkit.Interrupt(ctx, "请确认是否继续")
//	    }
//	    return doWork(input), nil
//	}
func Interrupt(ctx context.Context, info any) error {
	return tool.Interrupt(ctx, info)
}

// StatefulInterrupt 在工具执行中触发带状态保存的 HITL 中断。
// 恢复时可通过 GetInterruptState 取回保存的状态。
//
// state 必须是可通过 gob 序列化的类型。
//
// 用法：
//
//	type MyState struct { Step int }
//
//	func myTool(ctx context.Context, input string) (string, error) {
//	    wasInterrupted, hasState, state := agentkit.GetInterruptState[MyState](ctx)
//	    if !wasInterrupted {
//	        return "", agentkit.StatefulInterrupt(ctx, "处理中", MyState{Step: 1})
//	    }
//	    if hasState {
//	        return fmt.Sprintf("从步骤 %d 恢复", state.Step), nil
//	    }
//	    return "已恢复", nil
//	}
func StatefulInterrupt(ctx context.Context, info any, state any) error {
	return tool.StatefulInterrupt(ctx, info, state)
}

// GetInterruptState 在工具中检查是否从中断恢复，并获取之前保存的状态。
//
// 返回值：
//   - wasInterrupted: 此工具是否曾被中断
//   - hasState: 是否有保存的状态且成功转换为类型 T
//   - state: 保存的状态（hasState 为 false 时为零值）
func GetInterruptState[T any](ctx context.Context) (wasInterrupted bool, hasState bool, state T) {
	return tool.GetInterruptState[T](ctx)
}

// GetResumeContext 在工具中检查是否是 Resume 的显式目标，并获取恢复数据。
//
// 返回值：
//   - isResumeTarget: 此工具是否被显式指定为恢复目标
//   - hasData: 是否提供了恢复数据
//   - data: 恢复数据（hasData 为 false 时为零值）
//
// 用于区分：
//   - 作为恢复目标被调用（应继续执行）
//   - 因兄弟工具被恢复而重新执行（应再次中断）
//
// 用法：
//
//	func myTool(ctx context.Context, input string) (string, error) {
//	    wasInterrupted, _, _ := agentkit.GetInterruptState[any](ctx)
//	    if !wasInterrupted {
//	        return "", agentkit.Interrupt(ctx, "需要用户输入")
//	    }
//	    isTarget, hasData, data := agentkit.GetResumeContext[string](ctx)
//	    if !isTarget {
//	        return "", agentkit.Interrupt(ctx, nil) // 非目标，重新中断
//	    }
//	    if hasData {
//	        return "用户输入: " + data, nil
//	    }
//	    return "已确认", nil
//	}
func GetResumeContext[T any](ctx context.Context) (isResumeTarget bool, hasData bool, data T) {
	return tool.GetResumeContext[T](ctx)
}
