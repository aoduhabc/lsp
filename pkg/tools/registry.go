package tools

import (
	"path/filepath"

	"github.com/example/demo-tools-bridge/pkg/lsp"
)

type Registry struct {
	RootAbs    string
	Tools      map[string]BaseTool
	LSPClients map[string]*lsp.Client
}

func NewRegistry(root string) (*Registry, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}

	r := &Registry{
		RootAbs:    rootAbs,
		Tools:      map[string]BaseTool{},
		LSPClients: map[string]*lsp.Client{},
	}

	r.Tools[GlobToolName] = NewGlobTool(rootAbs)
	r.Tools[GrepToolName] = NewGrepTool(rootAbs)
	r.Tools[LSToolName] = NewLsTool(rootAbs)
	r.Tools[ViewToolName] = NewViewTool(rootAbs)
	r.Tools[WriteToolName] = NewWriteTool(rootAbs)
	r.Tools[BashToolName] = NewBashTool()
	r.Tools[DiagnosticsToolName] = NewDiagnosticsTool(rootAbs)

	return r, nil
}

func (r *Registry) SetLSPClients(clients map[string]*lsp.Client) {
	r.LSPClients = clients
	// Attach to tools that can use LSP
	if vt, ok := r.Tools[ViewToolName].(*viewTool); ok {
		vt.lsps = clients
	}
	if wt, ok := r.Tools[WriteToolName].(*writeTool); ok {
		wt.lsps = clients
	}
	if dt, ok := r.Tools[DiagnosticsToolName].(*diagnosticsTool); ok {
		dt.lsps = clients
	}
}

func (r *Registry) List() []ToolInfo {
	out := make([]ToolInfo, 0, len(r.Tools))
	for _, t := range r.Tools {
		out = append(out, t.Info())
	}
	return out
}
