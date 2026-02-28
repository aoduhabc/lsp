package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/example/demo-tools-bridge/pkg/lsp"
	"github.com/example/demo-tools-bridge/pkg/lsp/protocol"
)

const DiagnosticsToolName = "diagnostics"

type DiagnosticsParams struct {
	FilePath string `json:"file_path"`
}

type diagnosticsTool struct {
	root string
	lsps map[string]*lsp.Client
}

func NewDiagnosticsTool(root string) BaseTool {
	return &diagnosticsTool{root: root, lsps: map[string]*lsp.Client{}}
}

func (d *diagnosticsTool) Info() ToolInfo {
	return ToolInfo{
		Name:        DiagnosticsToolName,
		Description: "Get diagnostics for a file or project.",
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to get diagnostics for (leave empty for project diagnostics)",
			},
		},
		Required: []string{},
	}
}

func (d *diagnosticsTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params DiagnosticsParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if len(d.lsps) == 0 {
		return NewTextErrorResponse("no LSP clients available"), nil
	}

	filePath := strings.TrimSpace(params.FilePath)
	if filePath != "" {
		fileAbs, err := absClean(filePath)
		if err != nil {
			return NewTextErrorResponse(err.Error()), nil
		}
		if d.root != "" && !isWithinRoot(d.root, fileAbs) {
			return NewTextErrorResponse("path is outside workspace root"), nil
		}
		return NewTextResponse(diagnosticsForFile(ctx, fileAbs, d.lsps)), nil
	}

	return NewTextResponse(projectDiagnostics(d.root, d.lsps)), nil
}

func diagnosticsForFile(ctx context.Context, filePath string, lsps map[string]*lsp.Client) string {
	lines := make([]string, 0)
	for name, client := range lsps {
		_ = client.OpenFile(ctx, filePath)
		_ = client.NotifyChange(ctx, filePath)
		ds, err := client.GetDiagnosticsForFile(ctx, filePath)
		if err != nil {
			continue
		}
		for _, d := range ds {
			lines = append(lines, formatDiagnostic(filePath, d, name))
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return fmt.Sprintf("No diagnostics found for %s", filePath)
	}
	return fmt.Sprintf("Diagnostics for %s:\n%s", filePath, strings.Join(lines, "\n"))
}

func projectDiagnostics(root string, lsps map[string]*lsp.Client) string {
	lines := make([]string, 0)
	for name, client := range lsps {
		for uri, diags := range client.GetDiagnostics() {
			pth := uri.Path()
			if root != "" && !isWithinRoot(root, pth) {
				continue
			}
			for _, d := range diags {
				lines = append(lines, formatDiagnostic(pth, d, name))
			}
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return "No project diagnostics found"
	}
	return "Project diagnostics:\n" + strings.Join(lines, "\n")
}

func formatDiagnostic(path string, d protocol.Diagnostic, source string) string {
	severity := "Info"
	switch d.Severity {
	case protocol.SeverityError:
		severity = "Error"
	case protocol.SeverityWarning:
		severity = "Warn"
	case protocol.SeverityHint:
		severity = "Hint"
	case protocol.SeverityInformation:
		severity = "Info"
	}
	src := d.Source
	if src == "" {
		src = source
	}
	code := ""
	if d.Code != nil {
		code = fmt.Sprintf("[%v]", d.Code)
	}
	loc := fmt.Sprintf("%s:%d:%d", path, d.Range.Start.Line+1, d.Range.Start.Character+1)
	return fmt.Sprintf("%s: %s [%s]%s %s", severity, loc, src, code, d.Message)
}
