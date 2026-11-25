package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
)

// Server is a small MCP server: tools (which may ask the user through
// elicitation), resources and resource templates. It serves any number of
// sessions, over a byte stream (stdio) or streamable HTTP. It backs the
// mock's demo servers and the tests, and shows the server side of the
// protocol.
type Server struct {
	Info         Implementation
	Instructions string
	// PageSize > 0 splits lists into pages of that size (cursors).
	PageSize int
	// Authorize, if set, checks each HTTP request (e.g. its bearer token);
	// false answers 401.
	Authorize func(*http.Request) bool

	mu        sync.Mutex
	tools     []serverTool
	resources []serverResource
	templates []ResourceTemplate
	sessions  map[*ServerSession]bool
	http      map[string]*httpSession
}

type serverTool struct {
	Tool
	h ToolHandler
}

type serverResource struct {
	Resource
	read func(ctx context.Context) (ResourceContents, error)
}

// ToolHandler runs a tool call. Returning an error reports a tool failure
// (a result with IsError), not a protocol error.
type ToolHandler func(ctx context.Context, req *ToolRequest) (*CallToolResult, error)

// ToolRequest is one tool call.
type ToolRequest struct {
	Name      string
	Arguments json.RawMessage
	Session   *ServerSession
}

// Elicit asks the user through the client (elicitation/create) and waits.
func (r *ToolRequest) Elicit(ctx context.Context, message string, schema json.RawMessage) (ElicitResult, error) {
	return r.Session.Elicit(ctx, message, schema)
}

// NewServer returns a server named name.
func NewServer(name, version string) *Server {
	return &Server{Info: Implementation{Name: name, Version: version}, sessions: map[*ServerSession]bool{}, http: map[string]*httpSession{}}
}

// AddTool registers a tool (InputSchema defaults to an empty object schema).
func (s *Server) AddTool(t Tool, h ToolHandler) {
	if len(t.InputSchema) == 0 {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	s.mu.Lock()
	s.tools = append(s.tools, serverTool{t, h})
	s.mu.Unlock()
}

// RemoveTool unregisters a tool. Call NotifyToolsChanged to tell clients.
func (s *Server) RemoveTool(name string) {
	s.mu.Lock()
	s.tools = slices.DeleteFunc(s.tools, func(t serverTool) bool { return t.Name == name })
	s.mu.Unlock()
}

// AddResource registers a resource whose contents read returns.
func (s *Server) AddResource(r Resource, read func(ctx context.Context) (ResourceContents, error)) {
	s.mu.Lock()
	s.resources = append(s.resources, serverResource{r, read})
	s.mu.Unlock()
}

// AddResourceTemplate registers a resource template (listed only).
func (s *Server) AddResourceTemplate(t ResourceTemplate) {
	s.mu.Lock()
	s.templates = append(s.templates, t)
	s.mu.Unlock()
}

// NotifyToolsChanged sends notifications/tools/list_changed to every
// session (on HTTP, to those with a GET stream).
func (s *Server) NotifyToolsChanged() {
	s.mu.Lock()
	var all []*ServerSession
	for ss := range s.sessions {
		all = append(all, ss)
	}
	s.mu.Unlock()
	for _, ss := range all {
		_ = ss.p.notify(context.Background(), "notifications/tools/list_changed", nil)
	}
}

// ServerSession is one client's session.
type ServerSession struct {
	s    *Server
	p    *peer
	mu   sync.Mutex
	caps ClientCapabilities
	info Implementation
}

// Client is the client's clientInfo.
func (ss *ServerSession) Client() Implementation {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.info
}

// Elicit sends elicitation/create and waits for the answer. ctx should be
// the tool call's (so on HTTP the request goes out on its stream).
func (ss *ServerSession) Elicit(ctx context.Context, message string, schema json.RawMessage) (ElicitResult, error) {
	ss.mu.Lock()
	ok := ss.caps.Elicitation != nil
	ss.mu.Unlock()
	if !ok {
		return ElicitResult{}, errors.New("the client does not support elicitation")
	}
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var r ElicitResult
	err := ss.p.call(ctx, "elicitation/create", ElicitRequest{Message: message, RequestedSchema: schema}, &r)
	return r, err
}

func (s *Server) newSession(w wire) *ServerSession {
	ss := &ServerSession{s: s}
	ss.p = newPeer(w, ss.handle, nil, nil)
	s.mu.Lock()
	s.sessions[ss] = true
	s.mu.Unlock()
	return ss
}

func (s *Server) endSession(ss *ServerSession) {
	s.mu.Lock()
	delete(s.sessions, ss)
	s.mu.Unlock()
	ss.p.shutdown(ErrClosed)
}

// serverStream is the stdio framing on the server side.
type serverStream struct{ t Transport }

func (w serverStream) send(ctx context.Context, data []byte, _ string) error {
	return w.t.Send(ctx, data)
}
func (w serverStream) recv() (Incoming, error) { return w.t.Recv() }
func (w serverStream) close() error            { return w.t.Close() }

// ServeStream serves one session over r and w (stdio: os.Stdin, os.Stdout)
// until r ends or ctx is cancelled.
func (s *Server) ServeStream(ctx context.Context, r io.Reader, w io.Writer) error {
	ss := s.newSession(serverStream{NewStreamTransport(r, w, nil)})
	stop := context.AfterFunc(ctx, func() { ss.p.shutdown(ctx.Err()) })
	defer stop()
	ss.p.run()
	s.endSession(ss)
	if errors.Is(ss.p.err, io.EOF) {
		return nil
	}
	return ss.p.err
}

func paginate[T any](items []T, cursor string, size int) ([]T, string, error) {
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(items) {
			return nil, "", &RPCError{Code: CodeInvalidParams, Message: "invalid cursor"}
		}
		start = n
	}
	if size <= 0 || start+size >= len(items) {
		return items[start:], "", nil
	}
	return items[start : start+size], strconv.Itoa(start + size), nil
}

func (ss *ServerSession) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	s := ss.s
	var lp listParams
	if len(params) > 0 && method != "initialize" {
		_ = json.Unmarshal(params, &lp)
	}
	switch method {
	case "initialize":
		var p initializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		ss.mu.Lock()
		ss.caps, ss.info = p.Capabilities, p.ClientInfo
		ss.mu.Unlock()
		v := ProtocolVersion
		if slices.Contains(SupportedVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		r := InitializeResult{ProtocolVersion: v, ServerInfo: s.Info, Instructions: s.Instructions}
		r.Capabilities.Tools = &struct {
			ListChanged bool `json:"listChanged,omitempty"`
		}{ListChanged: true}
		s.mu.Lock()
		if len(s.resources) > 0 || len(s.templates) > 0 {
			r.Capabilities.Resources = &struct {
				Subscribe   bool `json:"subscribe,omitempty"`
				ListChanged bool `json:"listChanged,omitempty"`
			}{}
		}
		s.mu.Unlock()
		return r, nil
	case "tools/list":
		s.mu.Lock()
		tools := make([]Tool, len(s.tools))
		for i, t := range s.tools {
			tools[i] = t.Tool
		}
		s.mu.Unlock()
		page, next, err := paginate(tools, lp.Cursor, s.PageSize)
		if err != nil {
			return nil, err
		}
		return listToolsResult{Tools: page, NextCursor: next}, nil
	case "tools/call":
		var p callToolParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		s.mu.Lock()
		i := slices.IndexFunc(s.tools, func(t serverTool) bool { return t.Name == p.Name })
		var h ToolHandler
		if i >= 0 {
			h = s.tools[i].h
		}
		s.mu.Unlock()
		if h == nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "Unknown tool: " + p.Name}
		}
		res, err := h(ctx, &ToolRequest{Name: p.Name, Arguments: p.Arguments, Session: ss})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return &CallToolResult{Content: []Content{TextContent(err.Error())}, IsError: true}, nil
		}
		if res.Content == nil {
			res.Content = []Content{}
		}
		return res, nil
	case "resources/list":
		s.mu.Lock()
		rs := make([]Resource, len(s.resources))
		for i, r := range s.resources {
			rs[i] = r.Resource
		}
		s.mu.Unlock()
		page, next, err := paginate(rs, lp.Cursor, s.PageSize)
		if err != nil {
			return nil, err
		}
		return listResourcesResult{Resources: page, NextCursor: next}, nil
	case "resources/templates/list":
		s.mu.Lock()
		ts := slices.Clone(s.templates)
		s.mu.Unlock()
		page, next, err := paginate(ts, lp.Cursor, s.PageSize)
		if err != nil {
			return nil, err
		}
		return listTemplatesResult{ResourceTemplates: page, NextCursor: next}, nil
	case "resources/read":
		var p readResourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		s.mu.Lock()
		i := slices.IndexFunc(s.resources, func(r serverResource) bool { return r.URI == p.URI })
		var read func(context.Context) (ResourceContents, error)
		if i >= 0 {
			read = s.resources[i].read
		}
		s.mu.Unlock()
		if read == nil {
			return nil, &RPCError{Code: -32002, Message: "Resource not found", Data: json.RawMessage(strconv.Quote(p.URI))}
		}
		c, err := read(ctx)
		if err != nil {
			return nil, err
		}
		if c.URI == "" {
			c.URI = p.URI
		}
		return ReadResourceResult{Contents: []ResourceContents{c}}, nil
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

// --- streamable HTTP ----------------------------------------------------

// httpSession is one HTTP client's session: incoming messages are queued
// for its peer; outgoing ones go to the stream of the request they belong
// to, or else to the GET stream.
type httpSession struct {
	id  string
	ss  *ServerSession
	in  chan Incoming
	mu  sync.Mutex
	req map[string]*sseStream // by request id
	get *sseStream
	end chan struct{}
	o   sync.Once
}

type sseStream struct {
	ch     chan []byte
	closed chan struct{} // the HTTP handler returned
	last   string        // the request id whose response ends it ("" for GET)
}

func (h *httpSession) send(ctx context.Context, data []byte, related string) error {
	h.mu.Lock()
	st := h.req[related]
	if related == "" || st == nil {
		st = h.get
	}
	h.mu.Unlock()
	if st == nil {
		return nil // nowhere to send it (no GET stream): dropped, as the spec allows
	}
	select {
	case st.ch <- data:
		return nil
	case <-st.closed:
		return errors.New("mcp: the client's stream is gone")
	case <-h.end:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *httpSession) recv() (Incoming, error) {
	select {
	case in := <-h.in:
		return in, nil
	case <-h.end:
		return Incoming{}, io.EOF
	}
}

func (h *httpSession) close() error {
	h.o.Do(func() { close(h.end) })
	return nil
}

// HTTPHandler serves the streamable HTTP transport at whatever path it is
// mounted on: POST messages, GET for the server's stream, DELETE to end a
// session. Requests are answered with SSE streams.
func (s *Server) HTTPHandler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Authorize != nil && !s.Authorize(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	h := s.http[sid]
	s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if h == nil {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		st := &sseStream{ch: make(chan []byte, 16), closed: make(chan struct{})}
		h.mu.Lock()
		if h.get != nil {
			h.mu.Unlock()
			http.Error(w, "a stream is already open", http.StatusConflict)
			return
		}
		h.get = st
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			if h.get == st {
				h.get = nil
			}
			h.mu.Unlock()
			close(st.closed)
		}()
		s.stream(w, r, h, st)
	case http.MethodDelete:
		if h == nil {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		s.mu.Lock()
		delete(s.http, sid)
		s.mu.Unlock()
		s.endSession(h.ss)
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		s.post(w, r, sid, h)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, sid string, h *httpSession) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessage))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}
	if m.Method == "initialize" && m.isRequest() {
		if sid != "" {
			http.Error(w, "initialize must not carry a session", http.StatusBadRequest)
			return
		}
		h = &httpSession{id: newSessionID(), in: make(chan Incoming, 16), req: map[string]*sseStream{}, end: make(chan struct{})}
		h.ss = s.newSession(h)
		go func() {
			h.ss.p.run()
			s.mu.Lock()
			delete(s.http, h.id)
			s.mu.Unlock()
			s.endSession(h.ss)
		}()
		s.mu.Lock()
		s.http[h.id] = h
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", h.id)
	} else if h == nil {
		if sid == "" {
			http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		} else {
			http.Error(w, "unknown session", http.StatusNotFound)
		}
		return
	}
	if !m.isRequest() {
		select {
		case h.in <- Incoming{Data: body}:
		case <-h.end:
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	key := idKey(m.ID)
	st := &sseStream{ch: make(chan []byte, 16), closed: make(chan struct{}), last: key}
	h.mu.Lock()
	h.req[key] = st
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.req, key)
		h.mu.Unlock()
		close(st.closed)
	}()
	select {
	case h.in <- Incoming{Data: body}:
	case <-h.end:
		http.Error(w, "session closed", http.StatusNotFound)
		return
	}
	s.stream(w, r, h, st)
}

// stream writes st's messages as SSE until the response to st.last went
// out, the client left or the session ended.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, h *httpSession, st *sseStream) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if fl != nil {
		fl.Flush()
	}
	for {
		select {
		case data := <-st.ch:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			if fl != nil {
				fl.Flush()
			}
			if st.last != "" {
				var m message
				if json.Unmarshal(data, &m) == nil && !m.isRequest() && !m.isNotification() && idKey(m.ID) == st.last {
					return
				}
			}
		case <-r.Context().Done():
			return
		case <-h.end:
			return
		}
	}
}
