package agentmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	agentapp "github.com/shotforward/shotforward/daemon/internal/application/agent"
)

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name string `json:"name"`
	// Codex app-server's mcpServer/tool/call bridge currently forwards its
	// public `tool` field instead of normalizing it to MCP's `name` field.
	// Both spellings remain subject to the same fixed allowlist.
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content           []toolContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type codedToolError struct {
	code          string
	message       string
	sourceAssetID string
	nextTool      string
}

func (e *codedToolError) Error() string { return e.message }

func toolError(code, message string) error {
	return &codedToolError{code: code, message: message}
}

func (s *Server) handlePost(response http.ResponseWriter, request *http.Request, route string) {
	contentType := request.Header.Get("Content-Type")
	if contentType != "application/json" && !strings.HasPrefix(contentType, "application/json;") {
		response.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, MaxRequestBytes)
	defer request.Body.Close()
	var message rpcRequest
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&message); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		response.WriteHeader(status)
		return
	}
	if err := requireJSONEOF(decoder); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if message.JSONRPC != "2.0" || strings.TrimSpace(message.Method) == "" {
		s.writeRPCError(response, message.ID, -32600, "invalid JSON-RPC request")
		return
	}
	if len(message.ID) == 0 || bytes.Equal(message.ID, []byte("null")) {
		if message.Method == "notifications/initialized" || strings.HasPrefix(message.Method, "notifications/") {
			response.WriteHeader(http.StatusAccepted)
			return
		}
		response.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	switch message.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "shotforward", "version": "0.1.0"},
			"instructions":    serverInstructions + agentapp.AssetReuseInstructions + agentapp.VideoPromptInstructions + videoPromptAdaptationInstructions,
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		var parameters toolCallParams
		if err := decodeStrict(message.Params, &parameters); err != nil ||
			(parameters.Name == "" && parameters.Tool == "") ||
			(parameters.Name != "" && parameters.Tool != "" && parameters.Name != parameters.Tool) {
			s.writeRPCError(response, message.ID, -32602, "invalid tools/call parameters")
			return
		}
		if parameters.Name == "" {
			parameters.Name = parameters.Tool
		}
		payload, err := s.callTool(request.Context(), route, parameters.Name, parameters.Arguments)
		if err != nil {
			failure := toolFailure{Code: "tool_failed", Message: "tool call failed"}
			var coded *codedToolError
			if errors.As(err, &coded) {
				failure.Code, failure.Message = coded.code, coded.message
				failure.SourceAssetID, failure.NextTool = coded.sourceAssetID, coded.nextTool
			}
			encoded, _ := json.Marshal(failure)
			result = toolCallResult{Content: []toolContent{{Type: "text", Text: string(encoded)}}, IsError: true}
		} else {
			toolResult := toolCallResult{Content: []toolContent{{Type: "text", Text: string(payload)}}}
			if parameters.Name == "get_session_context" {
				var structured map[string]any
				if json.Unmarshal(payload, &structured) == nil {
					toolResult.StructuredContent = structured
				}
			}
			result = toolResult
		}
	default:
		s.writeRPCError(response, message.ID, -32601, "method not found")
		return
	}
	s.writeRPCResult(response, message.ID, result)
}

func (s *Server) writeRPCResult(response http.ResponseWriter, id json.RawMessage, result any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("MCP-Protocol-Version", mcpProtocolVersion)
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeRPCError(response http.ResponseWriter, id json.RawMessage, code int, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("MCP-Protocol-Version", mcpProtocolVersion)
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func decodeStrict(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
