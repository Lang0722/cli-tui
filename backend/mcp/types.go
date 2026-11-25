package mcp

import "encoding/json"

// The MCP message types this package uses (spec revision 2025-06-18, with
// what older servers send). Fields the client does not need are left out;
// unknown fields are ignored when decoding.

// ProtocolVersion is the revision the client asks for. Servers may answer
// with any of SupportedVersions.
const ProtocolVersion = "2025-06-18"

// SupportedVersions are the revisions the client accepts, newest first.
// Elicitation exists only in 2025-06-18.
var SupportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Implementation names a client or server (clientInfo / serverInfo).
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// ClientCapabilities are sent in initialize. The client offers elicitation
// when it has an elicitation handler.
type ClientCapabilities struct {
	Elicitation *struct{} `json:"elicitation,omitempty"`
	Roots       *struct {
		ListChanged bool `json:"listChanged,omitempty"`
	} `json:"roots,omitempty"`
}

// ServerCapabilities are what the server offers (initialize result).
type ServerCapabilities struct {
	Tools *struct {
		ListChanged bool `json:"listChanged,omitempty"`
	} `json:"tools,omitempty"`
	Resources *struct {
		Subscribe   bool `json:"subscribe,omitempty"`
		ListChanged bool `json:"listChanged,omitempty"`
	} `json:"resources,omitempty"`
	Prompts *struct {
		ListChanged bool `json:"listChanged,omitempty"`
	} `json:"prompts,omitempty"`
	Logging     *struct{} `json:"logging,omitempty"`
	Completions *struct{} `json:"completions,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the server's answer to initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// Tool is one entry of tools/list.
type Tool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// InputSchema is the JSON schema of the arguments (an object schema).
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations are the server's hints about a tool (untrusted: a client
// decides on approvals with them but must not rely on them).
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// DisplayTitle is the tool's title: Title, else the annotation's, else Name.
func (t Tool) DisplayTitle() string {
	switch {
	case t.Title != "":
		return t.Title
	case t.Annotations != nil && t.Annotations.Title != "":
		return t.Annotations.Title
	}
	return t.Name
}

// ReadOnly reports whether the tool says it does not change anything.
func (t Tool) ReadOnly() bool {
	return t.Annotations != nil && t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint
}

type listParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult is the answer to tools/call. A tool that failed reports
// IsError with the reason in Content; protocol errors (unknown tool, bad
// arguments the server rejects outright) are an *RPCError instead.
type CallToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// Content is one content block: Type "text" (Text), "image" or "audio"
// (Data base64, MimeType), "resource_link" (URI, Name, ...) or "resource"
// (Resource, an embedded resource).
type Content struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Data        string            `json:"data,omitempty"`
	MimeType    string            `json:"mimeType,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Resource    *ResourceContents `json:"resource,omitempty"`
}

// TextContent is a text content block.
func TextContent(s string) Content { return Content{Type: "text", Text: s} }

// Resource is one entry of resources/list.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type listResourcesResult struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// ResourceTemplate is one entry of resources/templates/list.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type listTemplatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	NextCursor        string             `json:"nextCursor,omitempty"`
}

type readResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult is the answer to resources/read.
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// ResourceContents is a resource's text or (base64) blob.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ElicitRequest is a server's elicitation/create: it asks the user for
// RequestedSchema's fields (a flat object schema of strings, numbers,
// booleans and enums).
type ElicitRequest struct {
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	// Meta is the request's _meta (e.g. Codex's codex_approval_kind).
	Meta json.RawMessage `json:"_meta,omitempty"`
}

// ElicitResult answers an elicitation: Action "accept" (with Content, the
// answers by property name), "decline" or "cancel".
type ElicitResult struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

// Elicitation actions.
const (
	ElicitAccept  = "accept"
	ElicitDecline = "decline"
	ElicitCancel  = "cancel"
)

// LogMessage is a notifications/message (server logging).
type LogMessage struct {
	Level  string          `json:"level"`
	Logger string          `json:"logger,omitempty"`
	Data   json.RawMessage `json:"data"`
}

type cancelledParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Reason    string          `json:"reason,omitempty"`
}
