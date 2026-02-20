package main

import (
	"encoding/json"
	"fmt"
)

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

func toolResultText(text string) ToolResult {
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func toolResultJSON(value any) ToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolResultText(fmt.Sprintf("failed to encode result: %v", err))
	}
	return toolResultText(string(data))
}
