package server

import (
	"encoding/json"
	"strconv"
	"strings"
)

func parseParams[T any](args map[string]any) (T, error) {
	var result T
	data, err := json.Marshal(args)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

func getString(args map[string]any, key string) (string, bool) {
	val, ok := args[key]
	if !ok {
		return "", false
	}
	// Strict (Round 3 M33): accept only an actual JSON string. Previously a
	// non-string (number/bool/object) was coerced via fmt.Sprintf and returned
	// with ok=true, silently masking a client that sent the wrong type for a
	// string field. Now a type mismatch reads as "absent" so required-field
	// validation (requiredString) rejects it instead of storing e.g. "5".
	typed, isString := val.(string)
	if !isString {
		return "", false
	}
	return typed, true
}

func getInt(args map[string]any, key string) (int, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func getInt64(args map[string]any, key string) (int64, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func getBool(args map[string]any, key string) (bool, bool) {
	val, ok := args[key]
	if !ok {
		return false, false
	}
	switch typed := val.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true, true
		case "false", "0", "no", "n":
			return false, true
		}
	}
	return false, false
}

func getStringSlice(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}

	switch typed := val.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				result = append(result, strings.TrimSpace(str))
			}
		}
		return result
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		parts := strings.Split(typed, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
		return result
	default:
		return nil
	}
}

func (s *MCPServer) requireMemoryStore() *rpcError {
	if s.memoryStore == nil {
		return &rpcError{Code: rpcErrServerError, Message: "Memory store not available"}
	}
	return nil
}

func (s *MCPServer) requireRAGEngine() *rpcError {
	if s.getRagEngine() == nil {
		return &rpcError{Code: rpcErrServerError, Message: "RAG engine not available"}
	}
	return nil
}

// RAG tool implementations
