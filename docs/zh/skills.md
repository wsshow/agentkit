# 技能管理

[English](../skills.md) · [文档索引](README.md)

技能让 Agent 按需发现可复用指令，不必把所有说明都塞进系统提示词。AgentKit 为常见场景提供小型 `SkillsConfig`，高级 Eino 路由仍可通过自定义 Handler 接入。

## 目录结构

每个技能放在独立目录中：

```text
skills/
└── concise-answer/
    └── SKILL.md
```

技能文件由 YAML frontmatter 和指令正文组成：

```markdown
---
name: concise-answer
description: 让回答保持简短直接
---
回答不超过三个短句。
```

`name` 是稳定标识；`description` 应告诉模型何时使用该技能；正文是按需加载的实际指令。

## 启用本地技能

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	Skills: &agentkit.SkillsConfig{
		Paths: []string{"./skills"},
		// ToolName: "load_skill", // 默认为 "skill"
	},
})
```

每个 `Paths` 条目可以是：

- 一个 `SKILL.md` 文件；
- 一个包含 `SKILL.md` 的技能目录；或
- 一个集合目录，其一级子目录分别包含技能。

每次列出或加载时都会重新读取文件，因此修改后无需重建 Agent。名称重复、frontmatter 错误、指令为空或文件超过 1 MiB 时都会返回明确错误。

自定义 `ToolName` 时只能使用 ASCII 字母、数字、`_`、`-` 或 `.`，且最多 128 字节。AgentKit 会在构造阶段校验这个生成工具名，不会等到第一次模型请求时才暴露提供商无法接受的名称。

## 自定义存储

数据库、远程服务或程序动态构建的目录可用 `Backend` 代替 `Paths`：

```go
backend := agentkit.NewMemorySkillBackend(skills...)

agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:   "assistant",
	Model:  chatModel,
	Skills: &agentkit.SkillsConfig{Backend: backend},
})
```

`NewMemorySkillBackend` 并发安全。自定义实现只需满足精简的 `SkillBackend` 接口，并遵循传入的 context。初始化列举或模型后续加载时发生的 backend panic 会转换为包装 `ErrSkillBackendPanic` 的错误，不会终止进程。

## 刻意保留的边界

简易配置支持内联指令技能。请求 `context`、`agent` 或 `model` 覆盖的技能会在校验阶段失败，因为静默应用这些能力会让执行行为难以推断。

确实需要 Eino 高级技能 fork 或模型路由的应用，可通过 `Config.Handlers` 安装完整配置的 Eino 技能中间件。这样常见路径保持简单，同时不会阻断高级扩展。

## 设计建议

- 每个技能只负责一个清晰任务，并写出准确描述。
- 稳定的身份和安全规则放在 `SystemPrompt`；任务流程放在技能中。
- 少量边界清晰的技能通常优于大量相互重叠的技能。
- 把技能文件和远端后端视为可执行配置：评审变更并限制写权限。

## 相关指南

- [运行时与事件](runtime.md)
- [工具管理](tools.md)
- [MCP 管理](mcp.md)
