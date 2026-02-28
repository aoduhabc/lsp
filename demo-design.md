# Logical Specification Proof Guard（lsp）设计文档

## 1. 项目概览
Logical Specification Proof Guard（简称 lsp）是一个“本地工具 → LLM 工具调用”的最小闭环示例项目。它把一组文件系统类工具（glob/grep/ls/view/write 等）封装成一个独立的工具服务进程（Go），并通过标准输入输出的 JSON 协议提供 `list_tools` / `call_tool` 两类能力；上层由 Python 负责启动该进程、向 LLM 暴露工具 schema、驱动 LLM 自主选择工具并回灌工具结果，最终完成“LLM 选工具 → 工具执行 → 结果回传 → LLM 最终回答”的闭环。

核心价值：
- 以最少组件展示工具调用闭环
- 支持本地工具执行与 LLM 决策解耦
- 可扩展为更复杂的工具集或自治 agent

## 2. 项目现状
当前项目已实现以下关键能力：
- 工具服务进程（Go）：启动后接收 JSON 请求并执行工具
- 工具注册表：注册并暴露多种工具能力
- Python agent：完成模型请求、工具 schema 生成、工具调用与结果回灌
- 支持流式与非流式模型响应
- 日志与输入输出分步打印，便于排障与复盘

相关实现位置：
- 工具服务入口：[main.go](file:///c:/Users/Anonymous/Desktop/llm-security/%E7%94%B0%E6%B8%8A%E6%A0%8B/opencode-main/demo-tools-bridge/cmd/toolserver/main.go#L1-L160)
- 工具注册表：[registry.go](file:///c:/Users/Anonymous/Desktop/llm-security/%E7%94%B0%E6%B8%8A%E6%A0%8B/opencode-main/demo-tools-bridge/pkg/tools/registry.go#L1-L58)
- 工具描述与响应结构：[types.go](file:///c:/Users/Anonymous/Desktop/llm-security/%E7%94%B0%E6%B8%8A%E6%A0%8B/opencode-main/demo-tools-bridge/pkg/tools/types.go#L8-L69)
- Python agent 主流程：[agent_demo.py](file:///c:/Users/Anonymous/Desktop/llm-security/%E7%94%B0%E6%B8%8A%E6%A0%8B/opencode-main/demo-tools-bridge/python/agent_demo.py#L535-L706)
- 模型请求与工具调用解析：[agent_demo.py](file:///c:/Users/Anonymous/Desktop/llm-security/%E7%94%B0%E6%B8%8A%E6%A0%8B/opencode-main/demo-tools-bridge/python/agent_demo.py#L223-L408)

## 3. 目标与非目标
### 3.1 目标
- 提供稳定可复用的“工具服务 + LLM 驱动”闭环
- 支持工具集合的可扩展与最小可用能力验证
- 形成可阅读、可复现、可移植的工程样本

### 3.2 非目标
- 不提供完整 IDE 交互体验
- 不内置复杂任务规划与多代理协作
- 不覆盖全部文件类型与高并发场景

## 4. 项目功能
### 4.1 工具服务（Go）
1) 读取环境变量或当前目录作为工作根目录  
2) 构建工具注册表，并加载可选 LSP  
3) 监听 stdin，每行一个 JSON 请求  
4) 支持两类请求：  
   - `list_tools`：返回全部工具描述  
   - `call_tool`：执行指定工具并返回结果  

### 4.2 Python Agent
1) 启动工具服务子进程并建立通信  
2) 获取工具列表并生成模型工具 schema  
3) 构造 system/user 消息并请求 LLM  
4) 解析 LLM 工具调用（tool_calls）  
5) 执行工具并把结果追加为 tool 消息  
6) 重复 3~5，直到 LLM 输出最终回答  

## 5. 闭环流程（工具选择 → 执行 → 回灌）
**闭环核心：**  
LLM 获取工具列表后自主决定是否调用工具、调用哪个工具、用什么参数；Python 把工具调用转发给 Go 工具服务，拿到结果再回传给 LLM，形成闭环。

**关键步骤：**  
1) `list_tools`：获取工具描述  
2) `build_tool_schema`：转换为模型可理解的 schema  
3) 请求 LLM：`tool_choice=auto`  
4) 解析 `tool_calls`  
5) `call_tool`：执行工具  
6) 工具结果以 `role=tool` 回灌到消息列表  
7) LLM 输出最终回答  

## 6. 数据协议（stdio JSON）
### 6.1 请求格式
```json
{
  "id": "1",
  "method": "list_tools",
  "params": {}
}
```
```json
{
  "id": "2",
  "method": "call_tool",
  "params": {
    "name": "view",
    "input": { "file_path": "contract/MathUtils.sol" }
  }
}
```

### 6.2 响应格式
```json
{
  "id": "2",
  "result": {
    "type": "text",
    "content": "...",
    "metadata": "...",
    "is_error": false
  }
}
```

## 7. 关键模块说明（面向非 Go 读者）
### 7.1 工具注册表（Registry）
注册表是“工具清单”，包括工具名字、描述、参数和执行函数。Go 负责声明并注册工具，Python 只需读取描述即可生成 LLM 可用的工具 schema。

### 7.2 工具描述（ToolInfo）
ToolInfo 就是给 LLM 的“工具说明书”，包含：
- `name`：工具名
- `description`：用途说明
- `parameters`：可用参数结构
- `required`：必填参数

### 7.3 Python 侧工具调用桥接
Python 只做两件事：
- 把 LLM 的 tool_calls 转成 `call_tool` 请求发给 toolserver
- 把结果以 `role=tool` 的消息形式回灌给 LLM

## 8. 关键功能伪代码
### 8.1 Python 闭环驱动
```text
tools = list_tools()
tool_schema = build_tool_schema(tools)
messages = [system, user]
for step in 1..max_steps:
  response = call_llm(messages, tool_schema)
  append assistant message
  if no tool_calls:
    print final answer
    break
  for each tool_call:
    result = call_tool(tool_call.name, tool_call.args)
    append tool message(result)
```

### 8.2 toolserver 主循环
```text
while read_line(stdin):
  req = parse_json(line)
  if req.method == "list_tools":
     write_json({id: req.id, result: registry.list()})
  else if req.method == "call_tool":
     tool = registry.get(req.params.name)
     result = tool.run(req.params.input)
     write_json({id: req.id, result: result})
  else:
     write_json({id: req.id, error: method_not_found})
```

### 8.3 工具执行逻辑
```text
parse input json -> params
validate params
execute tool logic
return ToolResponse(text, metadata, is_error)
```

## 9. OpenCode 移植模块清单
从 OpenCode 抽离并移植到本项目的核心模块如下：
- `pkg/tools/`：工具集合与执行框架（glob/grep/ls/view/write/diagnostics/bash 等）
- `pkg/fileutil/`：文件遍历与忽略规则、rg 辅助封装
- `pkg/config/`：工具与 LSP 配置读取与工作目录管理
- `pkg/logging/`：统一日志与错误输出
- `pkg/lsp/`：LSP 客户端、协议与传输层
- `pkg/lsp/watcher/`：工作区文件变化监听与同步
- `pkg/lsp/util/` 与 `pkg/lsp/protocol/`：协议结构与工具函数

## 10. 目录结构
```
demo-tools-bridge/
├─ cmd/toolserver/          # stdio JSON tool server
├─ pkg/
│  ├─ tools/                # 可移植工具集合
│  ├─ fileutil/             # 文件系统辅助
│  ├─ config/               # 配置与工作目录
│  ├─ logging/              # 日志
│  └─ lsp/                  # LSP 客户端与协议
├─ python/agent_demo.py     # Python 上层 agent 示例
├─ contract/                # 示例合约
└─ output/                  # 示例输出
```

## 11. 安装与构建
### 11.1 构建 toolserver
```bash
cd demo-tools-bridge
go build -o toolserver ./cmd/toolserver
```

### 11.2 运行 Python demo
```bash
python ./python/agent_demo.py "你的任务描述"
```

## 12. 配置与环境变量
常用环境变量：
- `LLM_PROVIDER`：模型提供方（openai/openrouter/groq/deepseek/xai/local）
- `OPENAI_API_KEY` / `DEEPSEEK_API_KEY`：API Key
- `OPENAI_MODEL` / `DEEPSEEK_MODEL`：模型名
- `OPENAI_BASE_URL` / `DEEPSEEK_BASE_URL`：自定义接口地址
- `AGENT_STREAM`：是否开启流式（true/false）
- `AGENT_MAX_STEPS`：单次闭环最大轮数

## 13. 项目上限与限制
### 13.1 上限与边界
- 工具数量上限：由注册表可扩展，无硬性限制
- 工具执行能力上限：受 Go 侧实现能力与系统权限限制
- 模型上下文上限：受模型 context window 限制
- 工具输出上限：单次 `view`/`ls` 等工具会截断输出或限制行数

### 13.2 现有限制
- LLM 仅通过工具 schema 感知工具能力，无法自动发现新工具逻辑
- 工具执行结果目前是文本，结构化语义有限
- 复杂多步任务需要更多回合，受 `max_steps` 限制
- 工具服务是单进程串行执行，吞吐量受限

## 14. 路线图
- 支持并行工具调用与并发执行
- 结构化工具返回结果（JSON schema 细化）
- 权限与沙箱增强（限制写入/执行）
- 任务级规划与自我反思机制
- 运行时工具注册与动态发现

## 15. 贡献指南
- 提交 PR 前确保通过本地构建与基础验证
- 新增工具需补充 ToolInfo 描述与参数定义
- 变更公共协议需同步更新文档与示例

## 16. 许可证
项目许可证与依赖许可信息以仓库内 LICENSE 文件为准。

## 17. 结语
lsp 是一个可最小化复现“LLM 工具闭环”的工程样本，核心机制清晰、结构简单、易于扩展，适合作为更大规模 Agent 系统的原型基座。
