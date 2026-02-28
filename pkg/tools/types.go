package tools

import (
	"context"
	"encoding/json"
)

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Required    []string       `json:"required"`
}

type toolResponseType string

const (
	ToolResponseTypeText toolResponseType = "text"
)

type ToolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

type TextResponseWithDiagnostics struct {
	Text               string `json:"text"`
	FileDiagnostics    string `json:"file_diagnostics,omitempty"`
	ProjectDiagnostics string `json:"project_diagnostics,omitempty"`
}

func NewTextResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	}
}

func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	}
}

func WithResponseMetadata(response ToolResponse, metadata any) ToolResponse {
	if metadata == nil {
		return response
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return response
	}
	response.Metadata = string(metadataBytes)
	return response
}

type ToolCall struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Run(ctx context.Context, call ToolCall) (ToolResponse, error)
}
