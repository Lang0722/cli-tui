package mcpconn

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK decodes an elicitation's requestedSchema into a map, which
// loses the order of its properties, and a form should ask in the order
// the server wrote them. orderConn keeps the raw schemas of the
// elicitations it reads (sent as elicitation/create, or inside a
// result's inputRequests), keyed by their canonical form, so that
// OrderedSchema can give the server's bytes back for the decoded value.

// OrderedSchema is schema (an ElicitRequest's RequestedSchema) as the
// server wrote it, properties in their order; a schema it has not seen
// is marshaled as it is. nil stays nil.
func (t *Transport) OrderedSchema(schema any) json.RawMessage {
	if schema == nil {
		return nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	if raw := t.schemas.lookup(data); raw != nil {
		return raw
	}
	return data
}

// schemaOrder maps canonical schemas to raw ones (the last few only).
type schemaOrder struct {
	mu   sync.Mutex
	raw  map[string]json.RawMessage
	keys []string
}

const keepSchemas = 64

func canonical(raw []byte) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	data, err := json.Marshal(v) // map keys sorted
	return string(data), err == nil
}

func (o *schemaOrder) add(raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	key, ok := canonical(raw)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.raw == nil {
		o.raw = map[string]json.RawMessage{}
	}
	if _, seen := o.raw[key]; !seen {
		o.keys = append(o.keys, key)
		if len(o.keys) > keepSchemas {
			delete(o.raw, o.keys[0])
			o.keys = o.keys[1:]
		}
	}
	o.raw[key] = append(json.RawMessage(nil), raw...)
}

func (o *schemaOrder) lookup(marshaled []byte) json.RawMessage {
	key, ok := canonical(marshaled)
	if !ok {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.raw[key]
}

type orderConn struct {
	mcp.Connection
	order *schemaOrder
}

type elicitWire struct {
	RequestedSchema json.RawMessage `json:"requestedSchema"`
}

func (c *orderConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err != nil {
		return msg, err
	}
	switch m := msg.(type) {
	case *jsonrpc.Request:
		if m.Method == "elicitation/create" {
			var p elicitWire
			if json.Unmarshal(m.Params, &p) == nil {
				c.order.add(p.RequestedSchema)
			}
		}
	case *jsonrpc.Response:
		// Multi round-trip requests (2026-07-28): a result asking for input.
		if bytes.Contains(m.Result, []byte(`"inputRequests"`)) {
			var r struct {
				InputRequests map[string]struct {
					Method string     `json:"method"`
					Params elicitWire `json:"params"`
				} `json:"inputRequests"`
			}
			if json.Unmarshal(m.Result, &r) == nil {
				for _, ir := range r.InputRequests {
					if ir.Method == "elicitation/create" {
						c.order.add(ir.Params.RequestedSchema)
					}
				}
			}
		}
	}
	return msg, nil
}
