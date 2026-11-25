package protocol

import "encoding/json"

// MCP servers (Codex /mcp, MCP startup status and MCP elicitation).
//
// The MCP servers are the backend's: it configures them, starts them and
// calls their tools (shown with ToolCallBegin/ToolCallEnd as "server.tool").
// The UI lists them (/mcp, /mcp verbose), shows their startup in the status
// row, and answers their elicitation requests: a server asking the user for
// information, or for leave to run one of its tools.
//
// Startup
//
//   - Send McpStartupUpdate{Status: starting} for each server you start,
//     then one with its outcome: ready, failed (Error says why) or
//     cancelled. Every server that starts must get an outcome.
//   - While any server is starting the UI shows "Booting MCP server: x"
//     (one server) or "Starting MCP servers (done/total): x, y" in its
//     status row, and holds messages the user submits until the round is
//     over (Codex): they are queued and sent once every server finished.
//   - A failure prints a warning with Error ("MCP client for `x` failed
//     to start" without one). When the round is over, the UI adds "MCP
//     startup incomplete (failed: …)" and, for cancelled servers, "MCP
//     startup interrupted. The following servers were not initialized: …".
//     The same failure reported again is not repeated until the server
//     got ready or started again in between.
//   - An outcome without a start (the UI attached after the start) only
//     reports its own warnings.
//   - Startup updates are about the backend, not a session (see Unscoped):
//     the UI never drops them, whichever session is attached.
//
// Elicitation
//
//   - ElicitationRequest is session- and thread-scoped like an approval:
//     answer arrives as ResolveElicitation naming the same ServerName and
//     RequestID, plus ThreadID/SessionID of the thread and session that
//     asked. A pending request belongs in the attach catch-up (see
//     sessions_live.go) and makes the task wait for the user.
//   - RequestedSchema is a JSON schema for a flat form:
//     {"type":"object","properties":{…},"required":[…]}. Supported
//     properties: "string" (a text answer), "boolean" (True/False) and
//     single-select enums ("enum" + optional "enumNames", or "oneOf" of
//     {"const","title"}), each with optional "title", "description" and
//     "default". The user answers field by field; Content holds the
//     answered fields by property name (strings or booleans).
//   - No schema, or an object without properties, asks for permission:
//     Allow / Deny / Cancel (Kind tool_call: Allow / Cancel, with the
//     first three ToolParams under the message). Persist adds "Allow for
//     this session" and "Always allow", echoed back in Persist.
//   - A schema the UI can't show as a form (numbers, multi-select) falls
//     back to "<server> needs your approval." with "Yes, provide the
//     requested info" (accept, no Content), "No, but continue without
//     it" (decline) and "Cancel this request" (cancel).
//   - Esc always cancels; it never declines.

// ListMcpServers asks for the MCP servers and what they offer. Reply with
// McpServerList.
type ListMcpServers struct{}

// McpServerList answers ListMcpServers. The UI sorts it by name.
type McpServerList struct {
	Servers []McpServerStatus `json:"servers"`
}

// McpServerStatus is one server in /mcp.
type McpServerStatus struct {
	Name string `json:"name"`
	// Status is one of the McpServer* statuses; "" when unknown (shown as
	// "authentication required" if Auth is not_logged_in).
	Status string `json:"status,omitempty"`
	// Auth is one of the McpAuth* values ("" = unknown).
	Auth              string                `json:"auth,omitempty"`
	Tools             []McpTool             `json:"tools,omitempty"`
	Resources         []McpResource         `json:"resources,omitempty"`
	ResourceTemplates []McpResourceTemplate `json:"resource_templates,omitempty"`
}

// MCP server statuses (McpServerStatus.Status).
const (
	McpServerConnected    = "connected"
	McpServerStarting     = "starting"
	McpServerAuthRequired = "auth_required"
	McpServerFailed       = "failed"
	McpServerNotStarted   = "not_started"
	McpServerDisabled     = "disabled"
	McpServerCancelled    = "cancelled"
)

// MCP authentication states (McpServerStatus.Auth).
const (
	McpAuthUnknown     = "unknown"
	McpAuthUnsupported = "unsupported"
	McpAuthNotLoggedIn = "not_logged_in"
	McpAuthBearerToken = "bearer_token"
	McpAuthOAuth       = "oauth"
)

// McpTool is a tool a server offers.
type McpTool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// McpResource is a resource a server offers (shown by /mcp verbose).
type McpResource struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	URI   string `json:"uri"`
}

// McpResourceTemplate is a parameterized resource (/mcp verbose).
type McpResourceTemplate struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	URITemplate string `json:"uri_template"`
}

// McpStartupUpdate reports one server's startup (see the rules above).
type McpStartupUpdate struct {
	Server string `json:"server"`
	Status string `json:"status"` // one of the McpStartup* states
	Error  string `json:"error,omitempty"`
}

// MCP startup states (McpStartupUpdate.Status).
const (
	McpStartupStarting  = "starting"
	McpStartupReady     = "ready"
	McpStartupFailed    = "failed"
	McpStartupCancelled = "cancelled"
)

// ElicitationRequest is an MCP server asking the user (see the rules
// above). Answer with ResolveElicitation.
type ElicitationRequest struct {
	ServerName string `json:"server_name"`
	RequestID  string `json:"request_id"`
	Message    string `json:"message"`
	// RequestedSchema is the form's JSON schema; empty for a permission
	// request.
	RequestedSchema json.RawMessage `json:"requested_schema,omitempty"`
	// Kind is "" or ElicitationToolCall (asking to run a tool).
	Kind string `json:"kind,omitempty"`
	// ToolParams are the tool call's arguments, shown under the message.
	ToolParams []ElicitationParam `json:"tool_params,omitempty"`
	// Persist lists the remembered answers offered on a permission
	// request: PersistSession and/or PersistAlways.
	Persist []string `json:"persist,omitempty"`
}

// ElicitationToolCall marks a request to run an MCP tool.
const ElicitationToolCall = "tool_call"

// Remembered answers (ElicitationRequest.Persist, ResolveElicitation.Persist).
const (
	PersistSession = "session"
	PersistAlways  = "always"
)

// ElicitationParam is one tool argument shown on a tool_call request.
type ElicitationParam struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"` // default Name
	// Value is any JSON value: a string is shown as text, anything else as
	// compact JSON.
	Value json.RawMessage `json:"value"`
}

// ResolveElicitation answers an ElicitationRequest.
type ResolveElicitation struct {
	ServerName string `json:"server_name"`
	RequestID  string `json:"request_id"`
	Action     string `json:"action"` // one of the Elicitation* actions
	// Content is the accepted form's answers, a JSON object of property
	// name to string or boolean ({} when only optional fields were left
	// empty). Empty for permission requests and every other action.
	Content json.RawMessage `json:"content,omitempty"`
	// Persist is PersistSession or PersistAlways when the user chose
	// "Allow for this session" or "Always allow".
	Persist   string `json:"persist,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`  // thread that asked
	SessionID string `json:"session_id,omitempty"` // session that asked
}

// Elicitation actions (ResolveElicitation.Action).
const (
	ElicitationAccept  = "accept"
	ElicitationDecline = "decline"
	ElicitationCancel  = "cancel"
)

func (ListMcpServers) opType() string        { return "list_mcp_servers" }
func (ResolveElicitation) opType() string    { return "resolve_elicitation" }
func (McpServerList) eventType() string      { return "mcp_server_list" }
func (McpStartupUpdate) eventType() string   { return "mcp_startup_update" }
func (ElicitationRequest) eventType() string { return "elicitation_request" }
