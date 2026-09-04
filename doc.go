// Package agentkit provides a small, event-stream-driven runtime for building
// tool-using agents on CloudWeGo Eino ADK.
//
// An Agent can run text or multimodal prompts, expose Eino-compatible tools,
// persist sessions, compact long contexts, load reusable skills, and manage MCP
// connections. Prompt, Send, Continue, and Resume block until a run completes
// and are mutually exclusive for each Agent. Subscribe delivers ordered,
// synchronous event snapshots for streaming output and lifecycle observation.
//
// Call Close when an Agent is no longer needed. Use Cancel for non-blocking
// cancellation from an event subscriber, or Abort outside a subscriber when the
// caller must wait for the active run to finish.
package agentkit
