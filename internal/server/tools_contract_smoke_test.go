package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/steward"
	"go.uber.org/zap"
)

// T146. The schema tests pin how many tools there are and how they group, not
// that a handler accepts the input its own inputSchema declares. This test
// plays the client: initialize → tools/list → tools/call for every advertised
// tool (and every action of a grouped tool) with the minimal arguments the
// schema promises are enough — required fields only, a value per declared
// type — on a throwaway bank.
//
// A contract failure is an error a schema-valid call must never get: invalid
// params (the handler wants a field the schema does not require, or reads it
// under another name or type), an unknown tool/action, or a panic. Business
// errors on placeholder data ("not found", empty bank) are fine.
//
// RAG tools (semantic_search, index_documents) are not covered: the engine
// needs an embeddings provider, so without one the server does not advertise
// them and there is nothing to call.

// contractError classifies a tools/call error. Invalid params and an unknown
// tool/action mean the call did not match what the handler expects. -32603 is
// a contract failure only when it comes from the panic barrier: the repo_*
// tools also report plain I/O errors (a missing file) with that code, and a
// missing file is a business outcome of a placeholder path.
func contractError(rErr *rpcError) (string, bool) {
	switch {
	case rErr.Code == rpcErrInvalidParams:
		return "invalid params", true
	case rErr.Code == rpcErrMethodNotFound:
		return "tool/action not found", true
	case rErr.Code == rpcErrInternalError && strings.HasPrefix(rErr.Message, "internal error in tool"):
		return "handler panic", true
	}
	return "", false
}

func TestToolsContractSmokeFlat(t *testing.T) {
	runContractSmoke(t, false)
}

func TestToolsContractSmokeGrouped(t *testing.T) {
	runContractSmoke(t, true)
}

func runContractSmoke(t *testing.T, grouping bool) {
	s := newMemoryTestServer(t)
	svc, err := steward.NewService(s.memoryStore, zap.NewNop())
	if err != nil {
		t.Fatalf("steward.NewService: %v", err)
	}
	s.stewardService = svc
	s.config.ToolGrouping = grouping

	c := &contractClient{t: t, s: s}
	c.call("initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "contract-smoke", "version": "0"},
	})

	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	c.decode(c.call("tools/list", map[string]any{}), &list)
	if len(list.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}

	calls := 0
	for _, tl := range list.Tools {
		// Only a group meta-tool dispatches on `action`; steward_policy and
		// steward_inbox_resolve have an `action` enum of their own and are
		// plain tools whose schema states its requirements directly.
		if _, isGroup := groupByName[tl.Name]; isGroup {
			actions := groupedActions(tl.InputSchema)
			if len(actions) == 0 {
				t.Errorf("%s: group tool advertises no actions", tl.Name)
			}
			for _, a := range actions {
				args := minimalArgs(tl.InputSchema, a.required)
				args["action"] = a.name
				c.checkToolCall(tl.Name+"/"+a.name, tl.Name, args)
				calls++
			}
			continue
		}
		c.checkToolCall(tl.Name, tl.Name, minimalArgs(tl.InputSchema, requiredOf(tl.InputSchema)))
		calls++
	}
	t.Logf("grouping=%v: %d tools listed, %d calls checked", grouping, len(list.Tools), calls)
}

type contractClient struct {
	t  *testing.T
	s  *MCPServer
	id int
}

func (c *contractClient) call(method string, params any) *rpcResponse {
	c.t.Helper()
	c.id++
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatalf("marshal %s params: %v", method, err)
	}
	resp := c.s.handle(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprint(c.id)),
		Method:  method,
		Params:  raw,
		client:  stdioClientKey,
	})
	if resp == nil {
		c.t.Fatalf("%s: no response", method)
	}
	return resp
}

func (c *contractClient) decode(resp *rpcResponse, into any) {
	c.t.Helper()
	if resp.Error != nil {
		c.t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		c.t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		c.t.Fatalf("decode result: %v", err)
	}
}

func (c *contractClient) checkToolCall(label, name string, args map[string]any) {
	c.t.Helper()
	resp := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	if resp.Error == nil {
		return
	}
	if kind, bad := contractError(resp.Error); bad {
		argsJSON, _ := json.Marshal(args)
		c.t.Errorf("%s: schema-valid call rejected as %s: %s (data=%v)\n  args: %s",
			label, kind, resp.Error.Message, resp.Error.Data, argsJSON)
	}
}

type contractAction struct {
	name     string
	required []string
}

var reqMarker = regexp.MustCompile(`\[req: ([^\]]+)\]\s*$`)

// groupedActions returns the actions of a grouped meta-tool, or nil for a
// plain tool. JSON Schema cannot say "content is required when action=store",
// so the grouped schema requires only `action` and states each action's own
// required fields in the enum help as "[req: a, b]" (T91 M8). A client has
// nothing else to go on, so neither does this test.
func groupedActions(schema map[string]any) []contractAction {
	props, _ := schema["properties"].(map[string]any)
	action, _ := props["action"].(map[string]any)
	enum, _ := action["enum"].([]any)
	if len(enum) == 0 {
		return nil
	}
	desc, _ := action["description"].(string)
	blocks := map[string]string{}
	for _, block := range strings.Split(desc, "\n- ")[1:] {
		name, rest, _ := strings.Cut(block, ":")
		blocks[name] = rest
	}
	out := make([]contractAction, 0, len(enum))
	for _, e := range enum {
		name, _ := e.(string)
		a := contractAction{name: name}
		if m := reqMarker.FindStringSubmatch(blocks[name]); m != nil {
			for _, f := range strings.Split(m[1], ",") {
				a.required = append(a.required, strings.TrimSpace(f))
			}
		}
		out = append(out, a)
	}
	return out
}

func requiredOf(schema map[string]any) []string {
	raw, _ := schema["required"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// minimalArgs builds the smallest argument object the schema accepts: one
// value per required field, chosen from the field's declared type.
func minimalArgs(schema map[string]any, required []string) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	args := map[string]any{}
	for _, name := range required {
		prop, _ := props[name].(map[string]any)
		args[name] = sampleValue(prop)
	}
	return args
}

func sampleValue(prop map[string]any) any {
	if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	typ, _ := prop["type"].(string)
	if types, ok := prop["type"].([]any); ok {
		for _, tv := range types {
			if s, _ := tv.(string); s != "null" {
				typ = s
				break
			}
		}
	}
	switch typ {
	case "integer", "number":
		if minimum, ok := prop["minimum"].(float64); ok {
			return minimum
		}
		if maximum, ok := prop["maximum"].(float64); ok && maximum < 1 {
			return maximum
		}
		return 1
	case "boolean":
		return false
	case "array":
		items, _ := prop["items"].(map[string]any)
		return []any{sampleValue(items)}
	case "object":
		return minimalArgs(prop, requiredOf(prop))
	case "string":
		if prop["format"] == "date-time" {
			return time.Now().UTC().Format(time.RFC3339)
		}
		return "contract-probe"
	default:
		// A schema with no type at all is treated as a string: that is what a
		// client falls back to, and a mismatch here is itself a contract bug.
		return "contract-probe"
	}
}
