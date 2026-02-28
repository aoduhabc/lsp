# demo-tools-bridge

把本地工具层（grep / glob / ls / view）用 Go 实现成一个可复用模块，并通过 stdio JSON 协议暴露为一个“tool server”。上层 agent 用 Python 作为子进程客户端调用这些工具。

## 目录结构

- `pkg/tools/`：可移植的底层工具实现（不依赖 opencode 的 internal 包）
- `cmd/toolserver/`：stdio JSON tool server（`list_tools` / `call_tool`）
- `python/agent_demo.py`：Python 上层示例（启动子进程 + 调用工具）

## 快速开始

### 1) 构建 toolserver

```bash
cd demo-tools-bridge
go build -o toolserver ./cmd/toolserver
```

### 2) 运行 Python demo

```bash
python ./python/agent_demo.py
```

默认会把工作目录设为本仓库根目录，并演示：
- 列出工具列表
- glob 查找 `**/*.go`
- grep 查找 `ToolInfo`
- ls 列出 `.` 的树
- view 读取 `go.mod`

