# MCP Management

[中文](zh/mcp.md) · [Documentation index](README.md)

AgentKit can own MCP connections from initialization through cleanup: it connects servers, performs fully paginated tool discovery, filters and namespaces tools, exposes them to the model, reconnects managed transports after connection-level failures, and closes sessions with the Agent.

## Streamable HTTP

```go
agent, err := agentkit.New(ctx, &agentkit.Config{
	Name:  "assistant",
	Model: chatModel,
	MCP: &agentkit.MCPConfig{
		InitializationTimeout: 30 * time.Second,
		Servers: []agentkit.MCPServerConfig{
			{
				Name:       "search",
				Transport:  agentkit.MCPTransportStreamableHTTP,
				URL:        "https://mcp.example.com/mcp",
				Headers:    map[string]string{"Authorization": "Bearer " + token},
				ToolNames:  []string{"search", "fetch"},
				ToolPrefix: "search__",
			},
		},
	},
})
if err != nil {
	log.Fatal(err)
}
defer agent.Close()
```

`ToolNames` is an optional allowlist. A requested name that the server does not expose is an initialization error, not a silent omission. Exposed tool names must be globally unique across local, skill, search, and MCP tools; use `ToolPrefix` to establish a namespace.

## Local stdio Server

```go
MCP: &agentkit.MCPConfig{
	Servers: []agentkit.MCPServerConfig{
		{
			Name:       "filesystem",
			Transport:  agentkit.MCPTransportStdio,
			Command:    "filesystem-mcp",
			Args:       []string{"--root", workspace},
			Env:        map[string]string{"LOG_LEVEL": "warn"},
			WorkingDir: workspace,
			ToolPrefix: "fs__",
		},
	},
}
```

The configured environment is merged with the current process environment. `MCPTransportSSE` is also available for legacy SSE servers.

## Initialization and Discovery

Connection and initial fully paginated discovery share a 30-second default deadline per server. `InitializationTimeout` changes that bound; an earlier caller context deadline still wins. If a later server fails, cleanup of sessions already opened uses the same bounded duration. Cleanup continues in the background if a custom session ignores `Close`, so `New` does not wait forever.

Tool discovery happens once during `agentkit.New`. Recreate the Agent when a server adds or removes tools.

## Result and Description Limits

MCP tool results default to `DefaultMCPMaxResultChars` (100,000 Unicode characters), and descriptions default to `DefaultMCPMaxDescriptionChars` (4,000 characters). Set the corresponding `MCPConfig` field to:

- a positive value for a custom limit;
- `-1` to disable the limit; or
- zero to use the default.

When [tool result reduction](tools.md#large-tool-result-reduction) is enabled, AgentKit disables the default MCP result cap so the complete output can be persisted. An explicitly configured positive MCP limit remains authoritative and intentionally discards content beyond it.

## Authentication

Static `Headers` are copied during initialization. Use a custom `HTTPClient` with an authentication `RoundTripper` when credentials rotate or must be refreshed dynamically. Avoid committing credentials in source code or configuration files.

## Reconnection and Ownership Boundary

For transport configurations created by AgentKit, connection-level tool failures invalidate the managed session and allow it to reconnect. The Agent owns these sessions and closes them during initialization failure or `Agent.Close`/`CloseContext`.

Advanced callers may provide an already-connected `MCPClientSession` instead of transport settings. AgentKit still takes ownership and closes that session, but reconnection remains the custom session implementation's responsibility. Supplying a custom session is not an instruction for AgentKit to reconstruct an unknown transport.

Use `CloseContext` during graceful shutdown when both an active run and MCP cleanup must have a bounded wait. If a custom close operation ignores its context, one-time cleanup continues in the background after the deadline.

## Related Guides

- [Tool management](tools.md)
- [Runtime and events](runtime.md)
- [Skills](skills.md)
