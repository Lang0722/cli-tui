package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ClientOptions configures a Client.
type ClientOptions struct {
	// Info is sent as clientInfo (default: agenttui).
	Info Implementation
	// Elicit answers a server's elicitation/create. ctx is the context of
	// the tool call the request arrived during (the one whose response
	// stream carried it, or on stdio the newest tool call in flight), so a
	// value the caller put on CallTool's context tells which conversation
	// asks; ctx is cancelled when that call ends or the server cancels.
	// With Elicit nil the client does not offer elicitation and servers
	// must not ask.
	Elicit func(ctx context.Context, req ElicitRequest) (ElicitResult, error)
	// OnNotification receives the server's notifications other than log
	// messages and cancellations (notifications/tools/list_changed,
	// notifications/progress, ...). It runs on the reading goroutine: keep
	// it short.
	OnNotification func(method string, params json.RawMessage)
	// Logf receives the server's log messages and protocol problems.
	Logf func(format string, args ...any)
}

// Client is a connection to one MCP server, ready for use (initialized).
// Its methods are safe for concurrent use.
type Client struct {
	p    *peer
	t    Transport
	init InitializeResult
	opts ClientOptions
}

// Connect runs the initialize handshake over t. ctx bounds the handshake
// (use the server's startup timeout); on failure t is closed.
func Connect(ctx context.Context, t Transport, opts ClientOptions) (*Client, error) {
	if opts.Info.Name == "" {
		opts.Info = Implementation{Name: "agenttui", Version: "0.1.0"}
	}
	c := &Client{t: t, opts: opts}
	c.p = newPeer(clientWire{t}, c.serveRequest, c.notification, opts.Logf)
	go c.p.run()

	caps := ClientCapabilities{}
	if opts.Elicit != nil {
		caps.Elicitation = &struct{}{}
	}
	err := c.p.call(ctx, "initialize", initializeParams{ProtocolVersion: ProtocolVersion, Capabilities: caps, ClientInfo: opts.Info}, &c.init)
	if err == nil && !slices.Contains(SupportedVersions, c.init.ProtocolVersion) {
		err = fmt.Errorf("mcp: server speaks protocol %q, which this client does not support", c.init.ProtocolVersion)
	}
	if err == nil {
		if v, ok := t.(interface{ setProtocolVersion(string) }); ok {
			v.setProtocolVersion(c.init.ProtocolVersion)
		}
		err = c.p.notify(ctx, "notifications/initialized", nil)
	}
	if err != nil {
		if errors.Is(err, ErrClosed) {
			<-c.p.done
			if c.p.err != nil && !errors.Is(c.p.err, ErrClosed) && !errors.Is(c.p.err, context.Canceled) {
				err = c.p.err // e.g. "server exited with exit status 2: ..."
			}
		}
		c.p.shutdown(ErrClosed)
		return nil, fmt.Errorf("handshaking with MCP server failed: %w", err)
	}
	return c, nil
}

// Init is the server's initialize result (its info, capabilities and
// instructions).
func (c *Client) Init() InitializeResult { return c.init }

// Done is closed when the connection is gone (Close, or the server went
// away); Err then says why.
func (c *Client) Done() <-chan struct{} { return c.p.done }

// Err is why the connection ended (nil while it is open; ErrClosed after
// Close).
func (c *Client) Err() error {
	select {
	case <-c.p.done:
		return c.p.closedErr()
	default:
		return nil
	}
}

// Close ends the connection (for stdio: stops the server).
func (c *Client) Close() error {
	c.p.shutdown(ErrClosed)
	return nil
}

// Transport is the client's transport (e.g. a *StdioTransport for its
// stderr).
func (c *Client) Transport() Transport { return c.t }

// Ping checks that the server answers.
func (c *Client) Ping(ctx context.Context) error { return c.p.call(ctx, "ping", nil, nil) }

// maxPages guards against a server whose cursors never end.
const maxPages = 1000

// ListTools returns all of the server's tools (every page).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if c.init.Capabilities.Tools == nil {
		return nil, nil
	}
	var all []Tool
	cursor := ""
	for range maxPages {
		var r listToolsResult
		if err := c.p.call(ctx, "tools/list", listParams{Cursor: cursor}, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Tools...)
		if cursor = r.NextCursor; cursor == "" {
			return all, nil
		}
	}
	return nil, errors.New("mcp: tools/list: too many pages")
}

// ListResources returns all of the server's resources.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	if c.init.Capabilities.Resources == nil {
		return nil, nil
	}
	var all []Resource
	cursor := ""
	for range maxPages {
		var r listResourcesResult
		if err := c.p.call(ctx, "resources/list", listParams{Cursor: cursor}, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Resources...)
		if cursor = r.NextCursor; cursor == "" {
			return all, nil
		}
	}
	return nil, errors.New("mcp: resources/list: too many pages")
}

// ListResourceTemplates returns all of the server's resource templates.
func (c *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	if c.init.Capabilities.Resources == nil {
		return nil, nil
	}
	var all []ResourceTemplate
	cursor := ""
	for range maxPages {
		var r listTemplatesResult
		if err := c.p.call(ctx, "resources/templates/list", listParams{Cursor: cursor}, &r); err != nil {
			return nil, err
		}
		all = append(all, r.ResourceTemplates...)
		if cursor = r.NextCursor; cursor == "" {
			return all, nil
		}
	}
	return nil, errors.New("mcp: resources/templates/list: too many pages")
}

// ReadResource reads one resource.
func (c *Client) ReadResource(ctx context.Context, uri string) (*ReadResourceResult, error) {
	var r ReadResourceResult
	if err := c.p.call(ctx, "resources/read", readResourceParams{URI: uri}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CallTool runs a tool. args is a JSON object (nil: no arguments). A tool
// that ran and failed is a result with IsError, not an error. Cancelling
// ctx abandons the call and tells the server (notifications/cancelled).
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var r CallToolResult
	if err := c.p.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *Client) serveRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "elicitation/create":
		if c.opts.Elicit == nil {
			return nil, &RPCError{Code: CodeMethodNotFound, Message: "elicitation is not supported"}
		}
		var req ElicitRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := c.opts.Elicit(ctx, req)
		if err != nil {
			return nil, err
		}
		switch res.Action {
		case ElicitAccept:
			if len(res.Content) == 0 {
				res.Content = json.RawMessage(`{}`)
			}
		case ElicitDecline, ElicitCancel:
			res.Content = nil
		default:
			return nil, fmt.Errorf("mcp: bad elicitation action %q", res.Action)
		}
		return res, nil
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

func (c *Client) notification(method string, params json.RawMessage) {
	if method == "notifications/message" {
		if c.opts.Logf != nil {
			var m LogMessage
			_ = json.Unmarshal(params, &m)
			c.opts.Logf("mcp server %s [%s] %s", c.init.ServerInfo.Name, m.Level, m.Data)
		}
		return
	}
	if c.opts.OnNotification != nil {
		c.opts.OnNotification(method, params)
	}
}
