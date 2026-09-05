# Skills

[中文](zh/skills.md) · [Documentation index](README.md)

Skills let an Agent discover reusable instructions without placing every instruction in the system prompt. AgentKit exposes a small `SkillsConfig` for the common case and leaves advanced Eino routing available through custom handlers.

## Directory Layout

Place each skill in its own directory:

```text
skills/
└── concise-answer/
    └── SKILL.md
```

A skill contains YAML frontmatter and instructions:

```markdown
---
name: concise-answer
description: Keep answers short and direct
---
Answer in no more than three short sentences.
```

The name is the stable identifier. The description should tell the model when the skill is useful; the body contains the instructions loaded on demand.

## Enable Local Skills

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	Skills: &agentkit.SkillsConfig{
		Paths: []string{"./skills"},
		// ToolName: "load_skill", // defaults to "skill"
	},
})
```

Each `Paths` entry may be:

- a `SKILL.md` file;
- one skill directory containing `SKILL.md`; or
- a collection directory whose immediate child directories contain skills.

Files are read again on every list or load operation, so edits take effect without rebuilding the Agent. Duplicate names, malformed frontmatter, empty instructions, and files larger than 1 MiB fail with explicit errors.

## Custom Storage

Use `Backend` instead of `Paths` for a database, remote service, or programmatically assembled catalog:

```go
backend := agentkit.NewMemorySkillBackend(skills...)

agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:   "assistant",
	Model:  chatModel,
	Skills: &agentkit.SkillsConfig{Backend: backend},
})
```

`NewMemorySkillBackend` is concurrency-safe. Custom implementations only need the small `SkillBackend` interface and must honor the supplied context. A backend panic during initial listing or a later model-requested load becomes an error wrapping `ErrSkillBackendPanic` instead of terminating the process.

## Intentional Boundaries

The simple configuration supports inline instruction skills. Skills requesting `context`, `agent`, or `model` overrides fail during validation because applying those features invisibly would make execution difficult to reason about.

Applications that need Eino's advanced skill forks or model routing can install a fully configured Eino skill middleware through `Config.Handlers`. This keeps the common path small without preventing advanced integration.

## Design Advice

- Give every skill one clear responsibility and a precise description.
- Keep stable safety or identity rules in `SystemPrompt`; use skills for task-specific procedures.
- Prefer a few meaningful skills over many overlapping ones.
- Treat skill files and remote backends as executable configuration: review changes and control who may write them.

## Related Guides

- [Runtime and events](runtime.md)
- [Tool management](tools.md)
- [MCP](mcp.md)
