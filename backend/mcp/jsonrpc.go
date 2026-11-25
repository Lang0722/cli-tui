package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// JSON-RPC 2.0 over a wire (one message per Send / Recv), shared by Client
// and Server: outgoing requests wait for their response, incoming requests
// run concurrently and can be cancelled with notifications/cancelled.

// RPCError is a JSON-RPC error response.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ErrClosed is returned for calls on a closed connection.
var ErrClosed = errors.New("mcp: connection closed")

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m *message) isRequest() bool      { return m.Method != "" && hasID(m.ID) }
func (m *message) isNotification() bool { return m.Method != "" && !hasID(m.ID) }

func hasID(id json.RawMessage) bool { return len(id) > 0 && string(id) != "null" }

// idKey is a request id as a map key: 7 and "7" are different ids.
func idKey(id json.RawMessage) string {
	var buf bytes.Buffer
	if json.Compact(&buf, id) != nil {
		return string(id)
	}
	return buf.String()
}

// Incoming is one received message. InReplyTo is the id (idKey) of the
// request whose response stream carried it, when the transport knows
// (streamable HTTP); stdio leaves it empty.
type Incoming struct {
	Data      []byte
	InReplyTo string
}

// wire is what a peer talks over. related is the id of the incoming
// request a message belongs to (a response, or a request a handler makes
// while serving it); the HTTP server uses it to pick the response stream.
type wire interface {
	send(ctx context.Context, data []byte, related string) error
	recv() (Incoming, error)
	close() error
}

type pendingCall struct {
	ch     chan message
	ctx    context.Context // cancelled when the call ends
	method string
	seq    int64
}

// requestHandler serves an incoming request; the result is marshalled
// (nil means {}).
type requestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type peer struct {
	w        wire
	onReq    requestHandler
	onNotify func(method string, params json.RawMessage)
	logf     func(format string, args ...any)

	next    atomic.Int64
	mu      sync.Mutex
	pending map[string]*pendingCall
	inbound map[string]context.CancelFunc

	base      context.Context // cancelled on close
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	err       error
}

func newPeer(w wire, onReq requestHandler, onNotify func(string, json.RawMessage), logf func(string, ...any)) *peer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	base, cancel := context.WithCancel(context.Background())
	return &peer{w: w, onReq: onReq, onNotify: onNotify, logf: logf,
		pending: map[string]*pendingCall{}, inbound: map[string]context.CancelFunc{},
		base: base, cancel: cancel, done: make(chan struct{})}
}

type relatedKey struct{}

// withRelated marks ctx as serving incoming request id: messages sent with
// it belong to that request (see wire).
func withRelated(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, relatedKey{}, id)
}

func relatedOf(ctx context.Context) string {
	s, _ := ctx.Value(relatedKey{}).(string)
	return s
}

// run reads until the wire fails, then closes the peer.
func (p *peer) run() {
	for {
		in, err := p.w.recv()
		if err != nil {
			p.shutdown(err)
			return
		}
		data := bytes.TrimSpace(in.Data)
		if len(data) == 0 {
			continue
		}
		if data[0] == '[' { // a batch (protocol revisions before 2025-06-18)
			var batch []json.RawMessage
			if err := json.Unmarshal(data, &batch); err != nil {
				p.logf("mcp: bad batch: %v", err)
				continue
			}
			for _, m := range batch {
				p.dispatch(m, in.InReplyTo)
			}
			continue
		}
		p.dispatch(data, in.InReplyTo)
	}
}

func (p *peer) dispatch(data []byte, inReplyTo string) {
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		p.logf("mcp: unparseable message: %v", err)
		_ = p.write(context.Background(), message{ID: json.RawMessage("null"), Error: &RPCError{Code: CodeParseError, Message: "parse error"}}, "")
		return
	}
	switch {
	case m.isRequest():
		go p.serve(m, inReplyTo)
	case m.isNotification():
		if m.Method == "notifications/cancelled" {
			var c cancelledParams
			if json.Unmarshal(m.Params, &c) == nil {
				p.mu.Lock()
				cancel := p.inbound[idKey(c.RequestID)]
				p.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			return
		}
		if p.onNotify != nil {
			p.onNotify(m.Method, m.Params)
		}
	case hasID(m.ID):
		p.mu.Lock()
		c := p.pending[idKey(m.ID)]
		delete(p.pending, idKey(m.ID))
		p.mu.Unlock()
		if c != nil {
			c.ch <- m
		} else {
			p.logf("mcp: response to unknown request %s", m.ID)
		}
	default:
		p.logf("mcp: ignoring message without method or id")
	}
}

// parentFor picks the context an incoming request runs in: the outgoing
// request whose response stream carried it, else the newest outgoing
// tools/call (on stdio a server's elicitation during a tool call has no
// other link to it), else the connection's.
func (p *peer) parentFor(inReplyTo string) context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.pending[inReplyTo]; inReplyTo != "" && c != nil {
		return c.ctx
	}
	var best *pendingCall
	for _, c := range p.pending {
		if c.method == "tools/call" && (best == nil || c.seq > best.seq) {
			best = c
		}
	}
	if best != nil {
		return best.ctx
	}
	return p.base
}

func (p *peer) serve(m message, inReplyTo string) {
	key := idKey(m.ID)
	ctx, cancel := context.WithCancel(p.parentFor(inReplyTo))
	stop := context.AfterFunc(p.base, cancel)
	defer stop()
	defer cancel()
	p.mu.Lock()
	p.inbound[key] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.inbound, key)
		p.mu.Unlock()
	}()
	ctx = withRelated(ctx, key)

	var result any
	var err error
	if m.Method == "ping" {
		result = struct{}{}
	} else if p.onReq == nil {
		err = &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + m.Method}
	} else {
		result, err = p.onReq(ctx, m.Method, m.Params)
	}
	if ctx.Err() != nil && p.base.Err() == nil && err != nil {
		return // cancelled by the other side: no response (spec)
	}
	resp := message{ID: m.ID}
	if err != nil {
		var re *RPCError
		if !errors.As(err, &re) {
			re = &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
		resp.Error = re
	} else {
		if result == nil {
			result = struct{}{}
		}
		raw, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = &RPCError{Code: CodeInternalError, Message: merr.Error()}
		} else {
			resp.Result = raw
		}
	}
	if werr := p.write(context.Background(), resp, key); werr != nil {
		p.logf("mcp: reply to %s: %v", m.Method, werr)
	}
}

func (p *peer) write(ctx context.Context, m message, related string) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.w.send(ctx, data, related)
}

// call sends a request and decodes its result into out (if non-nil).
func (p *peer) call(ctx context.Context, method string, params any, out any) error {
	select {
	case <-p.done:
		return p.closedErr()
	default:
	}
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	seq := p.next.Add(1)
	id := json.RawMessage(strconv.FormatInt(seq, 10))
	key := idKey(id)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &pendingCall{ch: make(chan message, 1), ctx: cctx, method: method, seq: seq}
	p.mu.Lock()
	p.pending[key] = c
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
	}
	if err := p.write(ctx, message{ID: id, Method: method, Params: raw}, relatedOf(ctx)); err != nil {
		forget()
		return err
	}
	select {
	case m := <-c.ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("mcp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		forget()
		if method != "initialize" { // the spec forbids cancelling initialize
			nctx, ncancel := context.WithTimeout(context.Background(), time.Second)
			_ = p.notify(nctx, "notifications/cancelled", cancelledParams{RequestID: id, Reason: ctx.Err().Error()})
			ncancel()
		}
		return ctx.Err()
	case <-p.done:
		forget()
		return p.closedErr()
	}
}

func (p *peer) notify(ctx context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return p.write(ctx, message{Method: method, Params: raw}, relatedOf(ctx))
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if r, ok := params.(json.RawMessage); ok {
		return r, nil
	}
	return json.Marshal(params)
}

func (p *peer) closedErr() error {
	if p.err != nil && !errors.Is(p.err, ErrClosed) {
		return fmt.Errorf("%w: %w", ErrClosed, p.err) // both match errors.Is
	}
	return ErrClosed
}

// shutdown closes the wire and fails everything waiting (err: why).
func (p *peer) shutdown(err error) {
	p.closeOnce.Do(func() {
		p.err = err
		p.cancel()
		_ = p.w.close()
		close(p.done)
	})
}

func compactJSON(buf *bytes.Buffer, data []byte) error { return json.Compact(buf, data) }
