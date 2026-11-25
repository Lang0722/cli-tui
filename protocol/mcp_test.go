package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMcpWireRoundTrip(t *testing.T) {
	events := []EventMsg{
		McpServerList{Servers: []McpServerStatus{{
			Name: "docs", Status: McpServerConnected, Auth: McpAuthOAuth,
			Tools:             []McpTool{{Name: "search", Title: "Search", Description: "d"}},
			Resources:         []McpResource{{Name: "r", Title: "R", URI: "docs://r"}},
			ResourceTemplates: []McpResourceTemplate{{Name: "t", URITemplate: "docs://{x}"}},
		}}},
		McpStartupUpdate{Server: "docs", Status: McpStartupFailed, Error: "boom"},
		ElicitationRequest{ServerName: "docs", RequestID: "7", Message: "m",
			RequestedSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
			Kind:            ElicitationToolCall, ToolParams: []ElicitationParam{{Name: "q", DisplayName: "Query", Value: json.RawMessage(`[300,"x"]`)}},
			Persist: []string{PersistSession, PersistAlways}},
	}
	for _, e := range events {
		b, err := MarshalEvent(Event{Session: "s1", Msg: e})
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalEvent(b)
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if !reflect.DeepEqual(got.Msg, e) || got.Session != "s1" {
			t.Errorf("round trip of %s:\n got %#v\nwant %#v", b, got.Msg, e)
		}
	}
	ops := []Op{
		ListMcpServers{},
		ResolveElicitation{ServerName: "docs", RequestID: "7", Action: ElicitationAccept, Content: json.RawMessage(`{"a":"b","n":true}`),
			Persist: PersistAlways, ThreadID: "t", SessionID: "s"},
	}
	for _, o := range ops {
		b, err := MarshalOp(o)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalOp(b)
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if !reflect.DeepEqual(got, o) {
			t.Errorf("round trip of %s: got %#v", b, got)
		}
	}
}

// Other languages may send a numeric request id (MCP's are string or
// number) and a schema with numbers in it; neither may be mangled.
func TestElicitationWireLeniency(t *testing.T) {
	ev, err := UnmarshalEvent([]byte(`{"session_id":"s","msg":{"type":"elicitation_request","server_name":"db","request_id":42,` +
		`"message":"x","requested_schema":{"type":"object","properties":{"n":{"type":"integer","maximum":100000}}},` +
		`"tool_params":[{"name":"big","value":123456789012}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := ev.Msg.(ElicitationRequest)
	if e.RequestID != "42" {
		t.Errorf("request id %q", e.RequestID)
	}
	if string(e.RequestedSchema) != `{"type":"object","properties":{"n":{"type":"integer","maximum":100000}}}` {
		t.Errorf("schema %s", e.RequestedSchema)
	}
	if string(e.ToolParams[0].Value) != "123456789012" {
		t.Errorf("value %s", e.ToolParams[0].Value)
	}
	op, err := UnmarshalOp([]byte(`{"type":"resolve_elicitation","server_name":"db","request_id":42,"action":"accept","content":{"n":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := op.(ResolveElicitation)
	if r.RequestID != "42" || string(r.Content) != `{"n":3}` {
		t.Errorf("op %#v", r)
	}
}

func TestMcpScoping(t *testing.T) {
	if !Unscoped(McpServerList{}) || !Unscoped(McpStartupUpdate{}) || Unscoped(ElicitationRequest{}) {
		t.Error("MCP inventory and startup are about the backend; requests belong to a session")
	}
	op := ForSession(ResolveElicitation{ServerName: "a", RequestID: "1"}, "s9")
	if OpSession(op) != "s9" || op.(ResolveElicitation).SessionID != "s9" {
		t.Errorf("ForSession: %#v", op)
	}
	if OpSession(ForSession(ListMcpServers{}, "s9")) != "" {
		t.Error("listing is not session-scoped")
	}
}
