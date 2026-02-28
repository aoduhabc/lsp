package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/example/demo-tools-bridge/pkg/config"
	"github.com/example/demo-tools-bridge/pkg/logging"
	"github.com/example/demo-tools-bridge/pkg/lsp"
	"github.com/example/demo-tools-bridge/pkg/lsp/protocol"
	"github.com/fsnotify/fsnotify"
)

type WorkspaceWatcher struct {
	client         *lsp.Client
	workspacePath  string
	debounceTime   time.Duration
	debounceMap    map[string]*time.Timer
	debounceMu     sync.Mutex
	registrations  []protocol.FileSystemWatcher
	registrationMu sync.RWMutex
}

func NewWorkspaceWatcher(client *lsp.Client) *WorkspaceWatcher {
	return &WorkspaceWatcher{
		client:        client,
		debounceTime:  300 * time.Millisecond,
		debounceMap:   make(map[string]*time.Timer),
		registrations: []protocol.FileSystemWatcher{},
	}
}

func (w *WorkspaceWatcher) AddRegistrations(ctx context.Context, id string, watchers []protocol.FileSystemWatcher) {
	cnf := config.Get()
	logging.Debug("Adding file watcher registrations")
	w.registrationMu.Lock()
	defer w.registrationMu.Unlock()
	w.registrations = append(w.registrations, watchers...)
	if cnf.DebugLSP {
		logging.Debug("Adding file watcher registrations", "id", id, "watchers", len(watchers), "total", len(w.registrations))
		for i, watcher := range watchers {
			logging.Debug("Registration", "index", i+1)
			switch v := watcher.GlobPattern.Value.(type) {
			case string:
				logging.Debug("GlobPattern", "pattern", v)
			case protocol.RelativePattern:
				logging.Debug("GlobPattern", "pattern", v.Pattern)
				switch u := v.BaseURI.Value.(type) {
				case string:
					logging.Debug("BaseURI", "baseURI", u)
				case protocol.DocumentUri:
					logging.Debug("BaseURI", "baseURI", u)
				default:
					logging.Debug("BaseURI", "baseURI", u)
				}
			}
		}
	}
}

func (w *WorkspaceWatcher) WatchWorkspace(ctx context.Context, workspacePath string) {
	w.workspacePath = workspacePath
	lsp.RegisterFileWatchHandler(func(id string, watchers []protocol.FileSystemWatcher) {
		w.AddRegistrations(ctx, id, watchers)
	})
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logging.Error("Failed to create file watcher", "error", err)
		return
	}
	defer watcher.Close()
	err = filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if shouldSkipDirectory(path) {
				return filepath.SkipDir
			}
			if err := watcher.Add(path); err != nil {
				logging.Error("Failed to watch directory", "path", path, "error", err)
			}
		}
		return nil
	})
	if err != nil {
		logging.Error("Failed to walk workspace", "error", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(ctx, event)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logging.Error("Watcher error", "error", err)
		}
	}
}

func (w *WorkspaceWatcher) handleEvent(ctx context.Context, event fsnotify.Event) {
	path := event.Name
	info, err := os.Stat(path)
	if err == nil && info.IsDir() && event.Op&fsnotify.Create == fsnotify.Create {
		if shouldSkipDirectory(path) {
			return
		}
	}
	if event.Op&fsnotify.Create == fsnotify.Create {
		w.handleFileEvent(ctx, "file://"+path, protocol.FileChangeType(protocol.Created))
	}
	if event.Op&fsnotify.Write == fsnotify.Write {
		w.debounceEvent(ctx, "file://"+path, protocol.FileChangeType(protocol.Changed))
	}
	if event.Op&fsnotify.Remove == fsnotify.Remove {
		w.handleFileEvent(ctx, "file://"+path, protocol.FileChangeType(protocol.Deleted))
	}
	if event.Op&fsnotify.Rename == fsnotify.Rename {
		w.handleFileEvent(ctx, "file://"+path, protocol.FileChangeType(protocol.Deleted))
	}
}

func (w *WorkspaceWatcher) debounceEvent(ctx context.Context, uri string, changeType protocol.FileChangeType) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()
	if timer, ok := w.debounceMap[uri]; ok {
		timer.Stop()
	}
	w.debounceMap[uri] = time.AfterFunc(w.debounceTime, func() {
		w.handleFileEvent(ctx, uri, changeType)
		w.debounceMu.Lock()
		delete(w.debounceMap, uri)
		w.debounceMu.Unlock()
	})
}

func (w *WorkspaceWatcher) handleFileEvent(ctx context.Context, uri string, changeType protocol.FileChangeType) {
	filePath := uri[7:]
	if changeType == protocol.FileChangeType(protocol.Deleted) {
		w.client.ClearDiagnosticsForURI(protocol.DocumentUri(uri))
	} else if changeType == protocol.FileChangeType(protocol.Changed) && w.client.IsFileOpen(filePath) {
		err := w.client.NotifyChange(ctx, filePath)
		if err != nil {
			logging.Error("Error notifying change", "error", err)
		}
		return
	}
	if err := w.notifyFileEvent(ctx, uri, changeType); err != nil {
		logging.Error("Error notifying LSP server about file event", "error", err)
	}
}

func (w *WorkspaceWatcher) notifyFileEvent(ctx context.Context, uri string, changeType protocol.FileChangeType) error {
	if !w.shouldNotify(uri) {
		return nil
	}
	params := protocol.DidChangeWatchedFilesParams{
		Changes: []protocol.FileEvent{
			{
				URI:  protocol.DocumentUri(uri),
				Type: changeType,
			},
		},
	}
	return w.client.DidChangeWatchedFiles(ctx, params)
}

func (w *WorkspaceWatcher) shouldNotify(uri string) bool {
	w.registrationMu.RLock()
	defer w.registrationMu.RUnlock()
	for _, watcher := range w.registrations {
		if w.matchesWatcher(uri, watcher) {
			return true
		}
	}
	return len(w.registrations) == 0
}

func (w *WorkspaceWatcher) matchesWatcher(uri string, watcher protocol.FileSystemWatcher) bool {
	switch v := watcher.GlobPattern.Value.(type) {
	case string:
		return matchGlobPattern(v, uri, w.workspacePath)
	case protocol.RelativePattern:
		base := ""
		switch u := v.BaseURI.Value.(type) {
		case string:
			base = u
		case protocol.DocumentUri:
			base = string(u)
		}
		return matchGlobPattern(v.Pattern, uri, strings.TrimPrefix(base, "file://"))
	}
	return false
}

func matchGlobPattern(pattern string, uri string, basePath string) bool {
	path := strings.TrimPrefix(uri, "file://")
	if basePath != "" && !strings.HasPrefix(path, basePath) {
		return false
	}
	relPath := path
	if basePath != "" {
		rel, err := filepath.Rel(basePath, path)
		if err == nil {
			relPath = rel
		}
	}
	ok, err := doublestar.Match(pattern, relPath)
	if err != nil {
		logging.Error("Error matching glob pattern", "pattern", pattern, "path", relPath, "error", err)
		return false
	}
	return ok
}

func shouldSkipDirectory(path string) bool {
	base := filepath.Base(path)
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}
	skipNames := []string{"node_modules", ".git", ".idea", ".vscode", "dist", "build", "target"}
	for _, name := range skipNames {
		if base == name {
			return true
		}
	}
	return false
}
