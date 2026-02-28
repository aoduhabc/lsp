package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example/demo-tools-bridge/pkg/fileutil"
)

const (
	GrepToolName    = "grep"
	grepDescription = `Fast content search tool that finds files containing specific text or patterns, returning matching file paths sorted by modification time (newest first).

WHEN TO USE THIS TOOL:
- Use when you need to find files containing specific text or patterns
- Great for searching code bases for function names, variable declarations, or error messages
- Useful for finding all files that use a particular API or pattern

HOW TO USE:
- Provide a regex pattern to search for within file contents
- Set literal_text=true if you want to search for the exact text with special characters (recommended for non-regex users)
- Optionally specify a starting directory (defaults to current working directory)
- Optionally provide an include pattern to filter which files to search
- Results are sorted with most recently modified files first

REGEX PATTERN SYNTAX (when literal_text=false):
- Supports standard regular expression syntax
- 'function' searches for the literal text "function"
- 'log\..*Error' finds text starting with "log." and ending with "Error"
- 'import\s+.*\s+from' finds import statements in JavaScript/TypeScript

COMMON INCLUDE PATTERN EXAMPLES:
- '*.js' - Only search JavaScript files
- '*.{ts,tsx}' - Only search TypeScript files
- '*.go' - Only search Go files

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Performance depends on the number of files being searched
- Very large binary files may be skipped
- Hidden files (starting with '.') are skipped

TIPS:
- For faster, more targeted searches, first use Glob to find relevant files, then use Grep
- When doing iterative exploration that may require multiple rounds of searching, consider using the Agent tool instead
- Always check if results are truncated and refine your search pattern if needed
- Use literal_text=true when searching for exact text containing special characters like dots, parentheses, etc.`
)

const maxFallbackFileSize = 1 * 1024 * 1024

var rgOutputLineRE = regexp.MustCompile(`^(.*?):(\d+):(.*)$`)

type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	Include     string `json:"include"`
	LiteralText bool   `json:"literal_text"`
}

type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	lineText string
}

type grepTool struct {
	root string
}

func NewGrepTool(root string) BaseTool {
	return &grepTool{root: root}
}

func (g *grepTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GrepToolName,
		Description: grepDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")",
			},
			"literal_text": map[string]any{
				"type":        "boolean",
				"description": "If true, the pattern will be treated as literal text with special regex characters escaped. Default is false.",
			},
		},
		Required: []string{"pattern"},
	}
}

func (g *grepTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GrepParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = escapeRegexPattern(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = WorkingDir()
	}
	searchAbs, err := absClean(searchPath)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if g.root != "" && !isWithinRoot(g.root, searchAbs) {
		return NewTextErrorResponse("path is outside workspace root"), nil
	}

	matches, truncated, err := searchFiles(searchPattern, searchAbs, params.Include, 100)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error searching files: %w", err)
	}

	out := "No files found"
	if len(matches) > 0 {
		out = fmt.Sprintf("Found %d matches\n", len(matches))
		currentFile := ""
		for _, match := range matches {
			if currentFile != match.path {
				if currentFile != "" {
					out += "\n"
				}
				currentFile = match.path
				out += fmt.Sprintf("%s:\n", match.path)
			}
			if match.lineNum > 0 {
				out += fmt.Sprintf("  Line %d: %s\n", match.lineNum, match.lineText)
			} else {
				out += fmt.Sprintf("  %s\n", match.path)
			}
		}
		if truncated {
			out += "\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return WithResponseMetadata(
		NewTextResponse(out),
		GrepResponseMetadata{NumberOfMatches: len(matches), Truncated: truncated},
	), nil
}

func escapeRegexPattern(pattern string) string {
	specialChars := []string{"\\", ".", "+", "*", "?", "(", ")", "[", "]", "{", "}", "^", "$", "|"}
	escaped := pattern
	for _, char := range specialChars {
		escaped = strings.ReplaceAll(escaped, char, "\\"+char)
	}
	return escaped
}

func searchFiles(pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	matches, err := searchWithRipgrep(pattern, rootPath, include)
	if err != nil {
		matches, err = searchFilesWithRegex(pattern, rootPath, include)
		if err != nil {
			return nil, false, err
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime.After(matches[j].modTime)
	})

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}

func searchWithRipgrep(pattern, path, include string) ([]grepMatch, error) {
	_, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("ripgrep not found: %w", err)
	}

	args := []string{"-H", "-n", pattern}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, path)

	cmd := exec.Command("rg", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	matches := make([]grepMatch, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		m := rgOutputLineRE.FindStringSubmatch(line)
		if len(m) != 4 {
			continue
		}

		filePath := m[1]
		lineNum, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		lineText := m[3]

		fileInfo, err := os.Stat(filePath)
		if err != nil {
			continue
		}

		if fileutil.SkipHidden(filePath) {
			continue
		}

		matches = append(matches, grepMatch{
			path:     filePath,
			modTime:  fileInfo.ModTime(),
			lineNum:  lineNum,
			lineText: lineText,
		})
	}

	return matches, nil
}

func searchFilesWithRegex(pattern, rootPath, include string) ([]grepMatch, error) {
	matches := []grepMatch{}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var includePattern *regexp.Regexp
	if include != "" {
		regexPattern := globToRegex(include)
		includePattern, err = regexp.Compile(regexPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid include pattern: %w", err)
		}
	}

	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if fileutil.SkipHidden(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if fileutil.SkipHidden(path) {
			return nil
		}
		if info.Size() > maxFallbackFileSize {
			return nil
		}
		if includePattern != nil && !includePattern.MatchString(path) {
			return nil
		}
		isBinary, binErr := isLikelyBinaryFile(path)
		if binErr != nil || isBinary {
			return nil
		}

		match, lineNum, lineText, err := fileContainsPattern(path, regex)
		if err != nil {
			return nil
		}
		if match {
			matches = append(matches, grepMatch{
				path:     path,
				modTime:  info.ModTime(),
				lineNum:  lineNum,
				lineText: lineText,
			})
			if len(matches) >= 200 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func fileContainsPattern(filePath string, pattern *regexp.Regexp) (bool, int, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, 0, "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if pattern.MatchString(line) {
			return true, lineNum, line, nil
		}
	}

	return false, 0, "", scanner.Err()
}

func isLikelyBinaryFile(filePath string) (bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	buf = buf[:n]
	for _, b := range buf {
		if b == 0 {
			return true, nil
		}
	}
	return false, nil
}

func globToRegex(glob string) string {
	regexPattern := strings.ReplaceAll(glob, ".", "\\.")
	regexPattern = strings.ReplaceAll(regexPattern, "*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "?", ".")

	re := regexp.MustCompile(`\{([^}]+)\}`)
	regexPattern = re.ReplaceAllStringFunc(regexPattern, func(match string) string {
		inner := match[1 : len(match)-1]
		return "(" + strings.ReplaceAll(inner, ",", "|") + ")"
	})

	return regexPattern
}
