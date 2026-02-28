package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	BashToolName     = "bash"
	DefaultTimeoutMs = 60 * 1000
	MaxTimeoutMs     = 10 * 60 * 1000
	MaxOutputLength  = 30000
)

type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type BashResponseMetadata struct {
	StartTime int64 `json:"start_time"`
	EndTime   int64 `json:"end_time"`
	ExitCode  int   `json:"exit_code"`
}

type bashTool struct{}

func NewBashTool() BaseTool {
	return &bashTool{}
}

func (b *bashTool) Info() ToolInfo {
	return ToolInfo{
		Name:        BashToolName,
		Description: "Execute shell commands with optional timeout and return stdout/stderr.",
		Parameters: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The command to execute",
			},
			"timeout": map[string]any{
				"type":        "number",
				"description": "Optional timeout in milliseconds (max 600000)",
			},
		},
		Required: []string{"command"},
	}
}

func (b *bashTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}
	if params.Command == "" {
		return NewTextErrorResponse("missing command"), nil
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeoutMs
	} else if timeout > MaxTimeoutMs {
		timeout = MaxTimeoutMs
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
	}

	startTime := time.Now()
	cmdName, cmdArgs := shellForCommand(params.Command)
	cmd := exec.CommandContext(runCtx, cmdName, cmdArgs...)
	cmd.Dir = WorkingDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	outStr := truncateOutput(stdout.String())
	errStr := truncateOutput(stderr.String())
	if runCtx.Err() == context.DeadlineExceeded {
		if errStr != "" {
			errStr += "\n"
		}
		errStr += "Command timed out"
	}
	if exitCode != 0 && errStr == "" {
		errStr = fmt.Sprintf("Exit code %d", exitCode)
	}

	result := outStr
	if errStr != "" {
		if result != "" {
			result += "\n"
		}
		result += errStr
	}
	if result == "" {
		result = "no output"
	}

	return WithResponseMetadata(
		NewTextResponse(result),
		BashResponseMetadata{
			StartTime: startTime.UnixMilli(),
			EndTime:   time.Now().UnixMilli(),
			ExitCode:  exitCode,
		},
	), nil
}

func shellForCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-Command", command}
	}
	return "/bin/sh", []string{"-c", command}
}

func truncateOutput(text string) string {
	if len(text) <= MaxOutputLength {
		return strings.TrimRight(text, "\n")
	}
	truncated := text[:MaxOutputLength]
	truncated = strings.TrimRight(truncated, "\n")
	return truncated + "\n(output truncated)"
}
