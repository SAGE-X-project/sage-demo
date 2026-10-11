// SPDX-License-Identifier: LGPL-3.0-or-later

// Package baseline is the unprotected run of the demo: an agent and a tool
// host that speak MCP JSON-RPC 2.0 (initialize, tools/call) over length-framed
// TCP with no authentication or integrity protection, as an unprotected MCP
// deployment does. It models the message shape, not a specific MCP SDK.
package baseline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/sage-x-project/sage-demo/internal/relay"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// ProtocolVersion is the MCP revision named in initialize.
const ProtocolVersion = "2025-06-18"

// Frame indexes of one agent call, in each direction.
const (
	RequestInitialize = 0
	RequestCall       = 2
	ResponseCall      = 1
)

var (
	// ErrProtocol reports an unexpected or malformed message.
	ErrProtocol = errors.New("baseline: protocol error")
	// ErrTool reports a tool call the host answered with isError.
	ErrTool = errors.New("baseline: tool error")
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content           []content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

// Host serves the transfer tool over unprotected MCP.
type Host struct {
	ledger  *transfer.Ledger
	timeout time.Duration
	wg      sync.WaitGroup
}

// NewHost serves calls against ledger. The timeout bounds each connection.
func NewHost(ledger *transfer.Ledger, timeout time.Duration) (*Host, error) {
	if ledger == nil || timeout <= 0 {
		return nil, errors.New("baseline: invalid host configuration")
	}
	return &Host{ledger: ledger, timeout: timeout}, nil
}

// Serve answers connections on l until ctx ends or l closes.
func (h *Host) Serve(ctx context.Context, l net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	defer h.wg.Wait()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.handle(conn)
		}()
	}
}

func (h *Host) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if conn.SetDeadline(time.Now().Add(h.timeout)) != nil {
		return
	}
	for {
		frame, err := relay.ReadFrame(conn)
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(frame, &m) != nil || m.JSONRPC != "2.0" {
			return
		}
		if m.ID == nil {
			continue
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "demo-transfer-host", "version": "1"},
			}
		case "tools/call":
			result = h.call(m.Params)
		default:
			return
		}
		out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": result})
		if err != nil || relay.WriteFrame(conn, out) != nil {
			return
		}
	}
}

func (h *Host) call(raw json.RawMessage) callResult {
	var p callParams
	if json.Unmarshal(raw, &p) != nil || p.Name != transfer.Tool {
		return callResult{Content: []content{{Type: "text", Text: "unknown tool"}}, IsError: true}
	}
	out, err := h.ledger.Apply(p.Arguments)
	if err != nil {
		return callResult{Content: []content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	return callResult{Content: []content{{Type: "text", Text: string(out)}}, StructuredContent: out}
}

// Call runs one agent call on conn: initialize, the initialized notification
// and tools/call for the transfer with arguments. It returns the structured
// tool result exactly as received. The agent has no way to check who answered
// or whether the request or result changed on the way.
func Call(ctx context.Context, conn net.Conn, arguments []byte) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	initialize, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "demo-agent", "version": "1"},
		},
	})
	if _, err := roundTrip(conn, initialize, 1); err != nil {
		return nil, err
	}
	if err := relay.WriteFrame(conn, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		return nil, err
	}
	call, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": callParams{Name: transfer.Tool, Arguments: arguments},
	})
	if err != nil {
		return nil, ErrProtocol
	}
	raw, err := roundTrip(conn, call, 2)
	if err != nil {
		return nil, err
	}
	var r callResult
	if json.Unmarshal(raw, &r) != nil {
		return nil, ErrProtocol
	}
	if r.IsError || len(r.StructuredContent) == 0 {
		return nil, ErrTool
	}
	return r.StructuredContent, nil
}

func roundTrip(conn net.Conn, request []byte, id int) (json.RawMessage, error) {
	if err := relay.WriteFrame(conn, request); err != nil {
		return nil, err
	}
	frame, err := relay.ReadFrame(conn)
	if err != nil {
		return nil, err
	}
	var m message
	if json.Unmarshal(frame, &m) != nil || m.ID == nil || *m.ID != id || len(m.Error) != 0 || len(m.Result) == 0 {
		return nil, ErrProtocol
	}
	return m.Result, nil
}

// TamperArguments returns frame with the tools/call arguments replaced. Other
// frames are returned unchanged.
func TamperArguments(frame, arguments []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil || !bytes.Equal(m["method"], []byte(`"tools/call"`)) {
		return frame
	}
	var p callParams
	if json.Unmarshal(m["params"], &p) != nil {
		return frame
	}
	p.Arguments = arguments
	params, err := json.Marshal(p)
	if err != nil {
		return frame
	}
	m["params"] = params
	out, err := json.Marshal(m)
	if err != nil {
		return frame
	}
	return out
}

// ForgeResult returns frame with the structured tool result replaced. Frames
// without a tool result are returned unchanged.
func ForgeResult(frame, result []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil {
		return frame
	}
	var r callResult
	if json.Unmarshal(m["result"], &r) != nil || len(r.StructuredContent) == 0 {
		return frame
	}
	r.StructuredContent = result
	r.Content = []content{{Type: "text", Text: string(result)}}
	raw, err := json.Marshal(r)
	if err != nil {
		return frame
	}
	m["result"] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return frame
	}
	return out
}
