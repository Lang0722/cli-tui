// Package mcp is a Model Context Protocol client (and a small server),
// written against the spec revision 2025-06-18 with only the standard
// library. The mock backend (backend/mock/mcp.go) uses it to run real MCP
// servers; it is meant as a reference for a real agent backend.
//
// # Connecting
//
// A ServerConfig is Codex's [mcp_servers.<name>] table. Open makes its
// transport, Connect runs the handshake:
//
//	t, err := cfg.Open(logFile)                 // stdio process or HTTP
//	ctx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout())
//	c, err := mcp.Connect(ctx, t, mcp.ClientOptions{Elicit: askUser})
//	tools, err := c.ListTools(ctx)              // every page
//	res, err := c.CallTool(callCtx, "search", json.RawMessage(`{"q":"x"}`))
//	<-c.Done()                                  // the server went away: c.Err()
//
// Transports:
//
//   - stdio (StartStdio): the server is a subprocess; one JSON-RPC message
//     per line on its stdin/stdout. Its stderr is kept (Stderr) so a server
//     that dies at startup reports why. Close closes stdin, then SIGTERMs
//     and SIGKILLs its process group.
//   - streamable HTTP (NewHTTPTransport): each message is a POST; requests
//     are answered with JSON or an SSE stream; the session id the server
//     assigns is sent back; a GET stream carries server messages that
//     belong to no request; 401/403 is an *AuthError (the server needs a
//     login); Close DELETEs the session.
//   - InProcess: a Server in this process, over pipes with stdio framing.
//
// # Elicitation
//
// A server may ask the user something while it runs a tool
// (elicitation/create). ClientOptions.Elicit answers it; it runs with the
// context of the tool call it belongs to, so put whatever identifies the
// conversation (session, turn) on CallTool's context and read it back
// there. The call is cancelled — and so is the elicitation's context —
// when the tool call ends or the server cancels it.
//
// # Cancellation
//
// Cancelling a call's context abandons it and sends notifications/cancelled;
// a server cancelling one of its requests cancels the handler's context.
package mcp
