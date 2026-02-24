package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// ── JSON-RPC types ────────────────────────────────────────────────────────────

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Server ────────────────────────────────────────────────────────────────────

type Server struct {
	backend Backend
	version string
}

func NewServer(backend Backend) *Server {
	return &Server{backend: backend, version: "1.0.0"}
}

func (s *Server) Routes(mux *http.ServeMux) {
	// Streamable HTTP: single endpoint accepts POST for all JSON-RPC messages
	// and GET for server-sent events (optional, for streaming responses)
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/health", s.handleHealth)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": s.version,
	})
}

// handleMCP is the single streamable HTTP endpoint.
// POST  → receives a JSON-RPC request, returns a JSON-RPC response.
// GET   → returns an SSE stream (for clients that want server-initiated messages).
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// CORS — open-webui may be on a different origin
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Mcp-Session-Id")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)

	case http.MethodPost:
		s.handlePost(w, r)

	case http.MethodGet:
		// GET /mcp opens an SSE stream for server-initiated notifications.
		// open-webui uses this for streaming tool responses.
		s.handleSSEStream(w, r)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Support both single request and batch (array)
	trimmed := trimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		s.handleBatch(w, body)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}

	result, rpcErr := s.dispatch(&req)
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}

	// Notifications have no ID and expect no response body
	if req.ID == nil && rpcErr == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleBatch(w http.ResponseWriter, body []byte) {
	var reqs []Request
	if err := json.Unmarshal(body, &reqs); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}

	var responses []Response
	for _, req := range reqs {
		result, rpcErr := s.dispatch(&req)
		resp := Response{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		if req.ID != nil {
			responses = append(responses, resp)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responses)
}

// handleSSEStream opens a persistent SSE connection.
// For this server we don't push server-initiated messages,
// but we keep the connection alive so clients that require it don't error out.
func (s *Server) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send an initial ping so the client knows we're alive
	fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Hold open until client disconnects
	<-r.Context().Done()
}

// ── JSON-RPC dispatch ─────────────────────────────────────────────────────────

func (s *Server) dispatch(req *Request) (any, *RPCError) {
	log.Printf("→ %s (id=%v)", req.Method, req.ID)

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]string{}, nil
	case "tools/list":
		return map[string]any{"tools": GetTools()}, nil
	case "tools/call":
		return s.handleToolCall(req.Params)
	default:
		return nil, &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}
}

func (s *Server) handleInitialize(params json.RawMessage) (any, *RPCError) {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "self-improvement-mcp",
			"version": s.version,
		},
		"instructions": `This is a self-improvement MCP server.
IMPORTANT: Call 'lookup_context' with relevant keywords at the START of every conversation before formulating your response.
This allows you to retrieve stored preferences, past learnings, and context about the user.
After conversations where you learn something useful, call 'store_learning' to persist it for future sessions.`,
	}, nil
}

func (s *Server) handleToolCall(params json.RawMessage) (any, *RPCError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: -32602, Message: "invalid params"}
	}

	log.Printf("  tool: %s", p.Name)
	result := HandleTool(s.backend, p.Name, p.Arguments)
	return result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
}

func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	return b[start:]
}
