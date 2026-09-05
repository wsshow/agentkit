# Tool Management

[中文](zh/tools.md) · [Documentation index](README.md)

AgentKit accepts Eino-compatible tools directly and adds a single policy layer for dispatch, safety, observability, large results, and large catalogs. The common path requires no custom Eino `ToolsNode` or middleware wiring.

Every model-facing tool name is validated during `agentkit.New`: names must be at most 128 bytes and contain only ASCII letters, digits, `_`, `-`, or `.`. Invalid local, MCP, and generated Skill tool names therefore fail before any model request.

## Tool Policy

```go
ToolPolicy: &agentkit.ToolPolicy{
	Sequential:   true,
	Timeout:      30 * time.Second,
	MaxResultChars: 50_000,
	Aliases: map[string]agentkit.ToolAlias{
		"web_search": {
			Names: []string{"search"},
			Arguments: map[string][]string{
				"query": {"q", "keywords"},
			},
		},
	},
	RewriteArguments: func(ctx context.Context, name, arguments string) (string, error) {
		return validateAndNormalize(arguments)
	},
	UnknownTool: func(ctx context.Context, name, arguments string) (string, error) {
		return "That tool is unavailable; choose a registered tool.", nil
	},
	BeforeTool: func(ctx context.Context, call agentkit.ToolInvocation) error {
		return authorize(call.Name, call.Arguments)
	},
	AfterTool: func(ctx context.Context, call agentkit.ToolInvocation, outcome agentkit.ToolOutcome) {
		recordToolRun(call, outcome)
	},
}
```

Tools run in parallel by default; set `Sequential` only when order matters or a dependency cannot handle concurrency. Hooks must be concurrency-safe. `BeforeTool` may reject an invocation. `AfterTool` receives duration, error, retained text size, and truncation metadata.

`Timeout` uses cooperative context cancellation. A custom tool must stop promptly when `ctx.Done()` closes. Do not pass a nil context into downstream work; use the tool context or `context.TODO()` if no meaningful lifetime is available.

## Aliases and Unknown Tools

Aliases can normalize a model's alternate tool names and argument names. During `agentkit.New`, aliases are validated against every local, skill, MCP, and searched tool. Collisions and missing canonical targets fail immediately.

`RewriteArguments` is suitable for validation and normalization that applies across tools. `UnknownTool` can return a model-readable recovery message instead of immediately ending the run.

## Result Limits and Panic Isolation

Text results default to `DefaultToolResultMaxChars` (100,000 Unicode characters) and receive a truncation marker when cut. Set `MaxResultChars` to `-1` to disable the limit. If preserving the complete content matters, use [large-result reduction](#large-tool-result-reduction) instead of raising the limit indefinitely.

A panic from a tool implementation or middleware endpoint—including while reading its returned stream—becomes an error wrapping `ErrToolExecutionPanic`. Policy control callback and middleware-factory panics become errors wrapping `ErrToolPolicyPanic`. An `AfterTool` panic is emitted as `EventError` without replacing a successful result. These protections cover ordinary, streaming, and multimodal tools.

Advanced callers can add `agentkit.ToolMiddleware` through `ToolPolicy.Middlewares`.

## Automatic Dangling-Call Repair

AgentKit repairs dangling tool calls before every model request. If cancellation or interruption leaves an assistant tool call without a matching tool-result message, a synthetic result is added so OpenAI-compatible APIs receive valid history.

This behavior is always enabled because it repairs protocol structure and carries no application storage choice. Applications do not need to configure an equivalent `patchtoolcalls` middleware.

## Large Tool Result Reduction

Enable persistent offloading with a zero-value-safe option:

```go
ToolReduction: &agentkit.ToolReductionConfig{}
```

A single result over 50,000 bytes is replaced in model context with a short preview and opaque result ID. AgentKit registers a safe, read-only `read_tool_result` tool. It returns at most 20,000 Unicode characters per call and supplies `next_offset` for continuation. The reader also requires the stored result's `SessionID` to exactly match the current Agent session; an ID from another session returns `ErrToolResultAccessDenied`.

When estimated context exceeds 160,000 tokens, older tool rounds are also offloaded while the most recent round remains intact. Adjust behavior with `MaxResultBytes`, `MaxContextTokens`, and `KeepRecentToolRounds`.

### Storage behavior

No storage wiring is needed:

1. reduction uses the `ToolResultStoreProvider` supplied by `Session`; or
2. it falls back to a concurrency-safe memory store.

`NewFileSessionStore` therefore makes reduced results survive process restarts. Set `Store` only for a custom backend. `agent.ToolResultStore()` gives applications direct access for administrative retention work; that application-facing store is not scoped automatically. Built-in session deletion removes session-owned results; manually saved results with an empty `SessionID` remain independent and can only be read by the model-side reader of an Agent without a session.

Result IDs are exact durable keys and cannot be blank or contain surrounding whitespace. A non-empty owning `SessionID` cannot contain surrounding whitespace either. Built-in and custom-store boundaries enforce these rules so reconnect, access control, and retention cannot disagree about visually identical records.

Reduction owns result sizing while enabled, while timeouts, hooks, aliases, and other policy behavior remain active. It runs before full [context compaction](context.md), avoiding a needless summary-model cost. For MCP-specific limit interaction, see [MCP result limits](mcp.md#result-and-description-limits).

## On-Demand Tool Search

Keep frequent tools in `Config.Tools`. Put a large, specialized catalog behind one optional configuration:

```go
ToolSearch: &agentkit.ToolSearchConfig{
	Tools: []agentkit.Tool{
		lookupWeather,
		searchTickets,
		queryWarehouse,
	},
}
```

The model initially sees only the `tool_search` meta-tool for that catalog. Matching tools become visible after a search and still pass through the same timeouts, aliases, hooks, result handling, and middleware. Ordinary `Config.Tools` always remain visible.

Tool search adds a model decision. Keep small catalogs directly in `Tools`; enable search only when tool schemas consume meaningful context or confuse selection. Set `UseModelNative: true` only when the provider implements native tool search. The name `tool_search` is reserved while the feature is enabled.

## Which Features Should Be Enabled?

| Capability | Default | Enable when |
| --- | --- | --- |
| Dangling-call repair | Always on | Every Agent benefits from valid protocol history |
| `ToolPolicy` | Optional | You need timeouts, sequential execution, aliases, authorization, audit, or custom result limits |
| `ToolReduction` | Optional | Results can be too large but must remain recoverable |
| `ToolSearch` | Optional | A large catalog would otherwise occupy meaningful model context |

## Related Guides

- [MCP](mcp.md)
- [Context management](context.md)
- [Runtime and events](runtime.md)
