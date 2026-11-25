// Package mcpconn connects to the MCP servers of a Codex-style
// [mcp_servers] config with the official Go SDK
// (github.com/modelcontextprotocol/go-sdk/mcp), which does the protocol.
// It adds what the SDK leaves to the application:
//
//   - ServerConfig: Codex's config keys (command, args, env, env_vars,
//     cwd, url, bearer_token_env_var, http_headers, env_http_headers,
//     enabled, startup_timeout_sec, tool_timeout_sec, enabled_tools,
//     disabled_tools) and their defaults.
//   - ServerConfig.Transport: the stdio process (its own process group,
//     the spec's shutdown, its stderr kept) or the streamable HTTP
//     connection with the configured headers and token.
//   - Transport.Explain and Ended: why connecting failed or a session
//     ended (exit status and last stderr line; *AuthError for a 401/403,
//     which the SDK reports only as text).
//   - Transport.OrderedSchema: an elicitation form's properties in the
//     server's order (the SDK decodes the schema into a map).
//
// Connecting:
//
//	t, err := cfg.Transport(logFile)
//	client := mcp.NewClient(&mcp.Implementation{Name: "my-agent", Version: "1.0"},
//		&mcp.ClientOptions{ElicitationHandler: ask})
//	ctx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout())
//	cs, err := client.Connect(ctx, t, nil)
//	cancel()
//	if err != nil {
//		return t.Explain(err)
//	}
//
// The elicitation handler's context: with a server on protocol
// 2026-07-28 or later a form comes back inside the tool call's result
// and the SDK calls the handler with CallTool's context, so a value put
// there tells which conversation asks. Older servers send
// elicitation/create on their own, and the handler gets the session's
// context: route it to the tool call in flight on that server.
package mcpconn
