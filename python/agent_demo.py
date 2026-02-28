import json
import os
import subprocess
import sys
import random
import time
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional


def load_env_file(path: str) -> None:
    if not os.path.exists(path):
        return
    try:
        with open(path, "r", encoding="utf-8") as f:
            lines = f.readlines()
    except Exception:
        return
    for line in lines:
        raw = line.strip()
        if not raw or raw.startswith("#"):
            continue
        if raw.startswith("export "):
            raw = raw[7:].strip()
        if "=" not in raw:
            continue
        key, value = raw.split("=", 1)
        key = key.strip()
        value = value.strip()
        if not key or key in os.environ:
            continue
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        os.environ[key] = value


class ToolServerClient:
    def __init__(self, proc: subprocess.Popen):
        self.proc = proc
        self._next_id = 1

    def request(self, method: str, params: Optional[Dict[str, Any]] = None) -> Any:
        req_id = str(self._next_id)
        self._next_id += 1
        payload = {"id": req_id, "method": method, "params": params or {}}
        self.proc.stdin.write((json.dumps(payload) + "\n").encode("utf-8"))
        self.proc.stdin.flush()

        line = self.proc.stdout.readline()
        if not line:
            raise RuntimeError("toolserver exited")
        resp = json.loads(line.decode("utf-8"))
        if resp.get("id") != req_id:
            raise RuntimeError(f"mismatched response id: expected {req_id}, got {resp.get('id')}")
        if resp.get("error"):
            raise RuntimeError(resp["error"]["message"])
        return resp.get("result")

    def list_tools(self) -> Any:
        return self.request("list_tools")

    def call_tool(self, name: str, input_obj: Dict[str, Any]) -> Any:
        return self.request("call_tool", {"name": name, "input": input_obj})


Message = Dict[str, Any]
ToolCall = Dict[str, Any]


class TokenUsage:
    def __init__(self, input_tokens: int = 0, output_tokens: int = 0, total_tokens: int = 0):
        self.input_tokens = input_tokens
        self.output_tokens = output_tokens
        self.total_tokens = total_tokens

    def add(self, other: "TokenUsage") -> None:
        self.input_tokens += other.input_tokens
        self.output_tokens += other.output_tokens
        self.total_tokens += other.total_tokens


class Model:
    def __init__(
        self,
        model_id: str,
        provider: str,
        api_model: str,
        cost_per_1m_in: float,
        cost_per_1m_out: float,
        context_window: int,
        can_reason: bool,
        supports_attachments: bool,
    ):
        self.model_id = model_id
        self.provider = provider
        self.api_model = api_model
        self.cost_per_1m_in = cost_per_1m_in
        self.cost_per_1m_out = cost_per_1m_out
        self.context_window = context_window
        self.can_reason = can_reason
        self.supports_attachments = supports_attachments


SUPPORTED_MODELS: Dict[str, Model] = {
    "openai.gpt-4o-mini": Model(
        "openai.gpt-4o-mini",
        "openai",
        "gpt-4o-mini",
        0.15,
        0.60,
        128000,
        False,
        True,
    ),
    "openai.gpt-4o": Model(
        "openai.gpt-4o",
        "openai",
        "gpt-4o",
        2.50,
        10.00,
        128000,
        True,
        True,
    ),
    "groq.llama-3.3-70b": Model(
        "groq.llama-3.3-70b",
        "groq",
        "llama-3.3-70b-versatile",
        0.59,
        0.79,
        128000,
        False,
        False,
    ),
    "openrouter.deepseek-r1": Model(
        "openrouter.deepseek-r1",
        "openrouter",
        "deepseek/deepseek-r1",
        0.0,
        0.0,
        128000,
        True,
        False,
    ),
    "deepseek.deepseek-chat": Model(
        "deepseek.deepseek-chat",
        "deepseek",
        "deepseek-chat",
        0.0,
        0.0,
        128000,
        False,
        False,
    ),
    "xai.grok-2": Model(
        "xai.grok-2",
        "xai",
        "grok-2",
        0.0,
        0.0,
        128000,
        True,
        True,
    ),
    "local.model": Model(
        "local.model",
        "local",
        "local",
        0.0,
        0.0,
        4096,
        False,
        False,
    ),
}


PROVIDER_DEFAULTS: Dict[str, Dict[str, Any]] = {
    "openai": {"base_url": "https://api.openai.com/v1", "headers": {}},
    "openrouter": {
        "base_url": "https://openrouter.ai/api/v1",
        "headers": {"HTTP-Referer": "opencode.ai", "X-Title": "OpenCode"},
    },
    "groq": {"base_url": "https://api.groq.com/openai/v1", "headers": {}},
    "deepseek": {"base_url": "https://api.deepseek.com/v1", "headers": {}},
    "xai": {"base_url": "https://api.x.ai/v1", "headers": {}},
    "local": {"base_url": os.environ.get("LOCAL_ENDPOINT", ""), "headers": {}},
}


class ProviderResponse:
    def __init__(self, message: Message, tool_calls: List[ToolCall], raw: Any, usage: TokenUsage):
        self.message = message
        self.tool_calls = tool_calls
        self.raw = raw
        self.usage = usage


class ProviderEvent:
    def __init__(
        self,
        event_type: str,
        content: str = "",
        tool_call: Optional[ToolCall] = None,
        response: Optional[ProviderResponse] = None,
        error: Optional[Exception] = None,
    ):
        self.event_type = event_type
        self.content = content
        self.tool_call = tool_call
        self.response = response
        self.error = error


class OpenAICompatibleProvider:
    def __init__(self, api_key: str, base_url: str, model: str, extra_headers: Dict[str, str]):
        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.model = model
        self.extra_headers = extra_headers

    def _build_payload(self, messages: List[Message], tools: List[Dict[str, Any]]) -> Dict[str, Any]:
        return {
            "model": self.model,
            "messages": messages,
            "tools": tools,
            "tool_choice": "auto",
        }

    def _build_stream_payload(self, messages: List[Message], tools: List[Dict[str, Any]]) -> Dict[str, Any]:
        payload = self._build_payload(messages, tools)
        payload["stream"] = True
        payload["stream_options"] = {"include_usage": True}
        return payload

    def _request(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        url = self.base_url + "/chat/completions"
        data = json.dumps(payload).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
        }
        headers.update(self.extra_headers)
        req = urllib.request.Request(
            url,
            data=data,
            headers=headers,
            method="POST",
        )
        return self._request_with_retries(req)

    def _request_with_retries(self, req: urllib.request.Request) -> Dict[str, Any]:
        attempts = 0
        while True:
            attempts += 1
            try:
                with urllib.request.urlopen(req, timeout=60) as resp:
                    raw = resp.read()
                return json.loads(raw.decode("utf-8"))
            except urllib.error.HTTPError as e:
                if e.code not in {429, 500}:
                    raise
                if attempts >= 8:
                    raise
                retry_after = e.headers.get("Retry-After")
                if retry_after and retry_after.isdigit():
                    sleep_ms = int(retry_after) * 1000
                else:
                    backoff = 2000 * (2 ** (attempts - 1))
                    jitter = int(backoff * random.uniform(0.0, 0.2))
                    sleep_ms = backoff + jitter
                time.sleep(sleep_ms / 1000.0)
            except urllib.error.URLError:
                if attempts >= 8:
                    raise
                backoff = 2000 * (2 ** (attempts - 1))
                jitter = int(backoff * random.uniform(0.0, 0.2))
                time.sleep((backoff + jitter) / 1000.0)

    def _parse_tool_calls(self, assistant_msg: Message) -> List[ToolCall]:
        tool_calls = []
        raw_calls = assistant_msg.get("tool_calls") or []
        for call in raw_calls:
            function = call.get("function") or {}
            tool_calls.append(
                {
                    "id": call.get("id", ""),
                    "name": function.get("name", ""),
                    "arguments": function.get("arguments", "{}"),
                }
            )
        return tool_calls

    def request(self, messages: List[Message], tools: List[Dict[str, Any]]) -> ProviderResponse:
        payload = self._build_payload(messages, tools)
        result = self._request(payload)
        choice = result["choices"][0]
        assistant_msg = choice["message"]
        tool_calls = self._parse_tool_calls(assistant_msg)
        usage = extract_token_usage(result)
        return ProviderResponse(assistant_msg, tool_calls, result, usage)

    def stream(self, messages: List[Message], tools: List[Dict[str, Any]]) -> List[ProviderEvent]:
        payload = self._build_stream_payload(messages, tools)
        url = self.base_url + "/chat/completions"
        data = json.dumps(payload).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
        }
        headers.update(self.extra_headers)
        req = urllib.request.Request(
            url,
            data=data,
            headers=headers,
            method="POST",
        )
        attempts = 0
        while True:
            attempts += 1
            try:
                with urllib.request.urlopen(req, timeout=60) as resp:
                    return self._parse_stream(resp)
            except urllib.error.HTTPError as e:
                if e.code not in {429, 500}:
                    raise
                if attempts >= 8:
                    raise
                retry_after = e.headers.get("Retry-After")
                if retry_after and retry_after.isdigit():
                    sleep_ms = int(retry_after) * 1000
                else:
                    backoff = 2000 * (2 ** (attempts - 1))
                    jitter = int(backoff * random.uniform(0.0, 0.2))
                    sleep_ms = backoff + jitter
                time.sleep(sleep_ms / 1000.0)
            except urllib.error.URLError:
                if attempts >= 8:
                    raise
                backoff = 2000 * (2 ** (attempts - 1))
                jitter = int(backoff * random.uniform(0.0, 0.2))
                time.sleep((backoff + jitter) / 1000.0)

    def _parse_stream(self, resp: Any) -> List[ProviderEvent]:
        events: List[ProviderEvent] = []
        content = ""
        finish_reason = ""
        usage = TokenUsage()
        tool_calls_by_index: Dict[int, ToolCall] = {}
        while True:
            line = resp.readline()
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            if not line.startswith(b"data:"):
                continue
            data = line[len(b"data:") :].strip()
            if data == b"[DONE]":
                break
            try:
                chunk = json.loads(data.decode("utf-8"))
            except Exception:
                continue
            chunk_usage = chunk.get("usage")
            if isinstance(chunk_usage, dict):
                usage.add(
                    TokenUsage(
                        int(chunk_usage.get("prompt_tokens") or 0),
                        int(chunk_usage.get("completion_tokens") or 0),
                        int(chunk_usage.get("total_tokens") or 0),
                    )
                )
            for choice in chunk.get("choices") or []:
                delta = choice.get("delta") or {}
                delta_content = delta.get("content")
                if delta_content:
                    events.append(ProviderEvent("content_delta", content=delta_content))
                    content += delta_content
                if "tool_calls" in delta:
                    for tc in delta.get("tool_calls") or []:
                        idx = tc.get("index", 0)
                        current = tool_calls_by_index.get(idx, {"id": "", "name": "", "arguments": ""})
                        if tc.get("id") and not current.get("id"):
                            current["id"] = tc.get("id")
                            events.append(ProviderEvent("tool_use_start", tool_call=current.copy()))
                        func = tc.get("function") or {}
                        if func.get("name") and current.get("name") != func.get("name"):
                            current["name"] = func.get("name")
                            events.append(ProviderEvent("tool_use_start", tool_call=current.copy()))
                        if "arguments" in func and func.get("arguments"):
                            current["arguments"] = current.get("arguments", "") + func.get("arguments")
                            events.append(ProviderEvent("tool_use_delta", tool_call=current.copy()))
                        tool_calls_by_index[idx] = current
                if choice.get("finish_reason"):
                    finish_reason = choice.get("finish_reason") or finish_reason
        tool_calls: List[ToolCall] = []
        for idx in sorted(tool_calls_by_index.keys()):
            tc = tool_calls_by_index[idx]
            if not tc.get("id"):
                tc["id"] = f"call_{idx}"
            tool_calls.append(tc)
            events.append(ProviderEvent("tool_use_stop", tool_call=tc.copy()))
        response = ProviderResponse({"content": content}, tool_calls, {"finish_reason": finish_reason}, usage)
        events.append(ProviderEvent("complete", response=response))
        return events


def build_tool_schema(tools: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    schema = []
    for tool in tools:
        schema.append(
            {
                "type": "function",
                "function": {
                    "name": tool.get("name", ""),
                    "description": tool.get("description", ""),
                    "parameters": {
                        "type": "object",
                        "properties": tool.get("parameters", {}) or {},
                        "required": tool.get("required", []) or [],
                    },
                },
            }
        )
    return schema


def build_provider(provider_name: str, api_key: str, base_url: str, model: str) -> OpenAICompatibleProvider:
    provider = provider_name.strip().lower()
    defaults = PROVIDER_DEFAULTS.get(provider)
    if defaults is None:
        raise ValueError(f"unsupported provider: {provider_name}")
    if not base_url:
        base_url = defaults["base_url"]
    extra_headers = defaults["headers"]
    return OpenAICompatibleProvider(api_key, base_url, model, extra_headers)


def resolve_model(model_id: str, fallback_model: str, provider_name: str) -> Model:
    if model_id and model_id in SUPPORTED_MODELS:
        return SUPPORTED_MODELS[model_id]
    api_model = fallback_model or "gpt-4o-mini"
    provider = provider_name or "openai"
    return Model(
        api_model,
        provider,
        api_model,
        0.0,
        0.0,
        128000,
        False,
        True,
    )


def extract_token_usage(result: Any) -> TokenUsage:
    usage = result.get("usage") if isinstance(result, dict) else None
    if not isinstance(usage, dict):
        return TokenUsage()
    input_tokens = usage.get("prompt_tokens")
    output_tokens = usage.get("completion_tokens")
    total_tokens = usage.get("total_tokens")
    return TokenUsage(
        int(input_tokens or 0),
        int(output_tokens or 0),
        int(total_tokens or 0),
    )


def normalize_arguments(arguments: Any) -> Dict[str, Any]:
    if isinstance(arguments, dict):
        return arguments
    if not isinstance(arguments, str):
        return {}
    try:
        args_obj = json.loads(arguments)
        if isinstance(args_obj, dict):
            return args_obj
    except Exception:
        return {}
    return {}


def strip_tool_order_instructions(prompt: str) -> str:
    start = prompt.find("1) 用 ls 查看 contract 目录")
    if start == -1:
        return prompt
    end = prompt.find("最终回复", start)
    if end == -1:
        return prompt[:start].strip()
    return (prompt[:start] + prompt[end:]).strip()


def build_assistant_message(assistant_msg: Message, tool_calls: List[ToolCall]) -> Message:
    record: Message = {"role": "assistant"}
    content = assistant_msg.get("content")
    if content is not None:
        record["content"] = content
    if tool_calls:
        record["tool_calls"] = [
            {
                "id": call.get("id", ""),
                "type": "function",
                "function": {
                    "name": call.get("name", ""),
                    "arguments": call.get("arguments", "{}"),
                },
            }
            for call in tool_calls
        ]
    return record


def response_from_events(events: List[ProviderEvent]) -> ProviderResponse:
    response = None
    content = ""
    tool_calls_by_id: Dict[str, ToolCall] = {}
    usage = TokenUsage()
    for event in events:
        if event.event_type == "content_delta":
            content += event.content
        if event.event_type in {"tool_use_start", "tool_use_delta", "tool_use_stop"} and event.tool_call:
            tc = event.tool_call
            tc_id = tc.get("id", "")
            if not tc_id:
                continue
            current = tool_calls_by_id.get(tc_id, {"id": tc_id, "name": "", "arguments": ""})
            if tc.get("name"):
                current["name"] = tc.get("name")
            if tc.get("arguments"):
                current["arguments"] = tc.get("arguments")
            tool_calls_by_id[tc_id] = current
        if event.event_type == "complete" and event.response:
            response = event.response
            usage = event.response.usage
    if response is not None:
        return response
    tool_calls = list(tool_calls_by_id.values())
    return ProviderResponse({"content": content}, tool_calls, {}, usage)


def run_agent(
    client: ToolServerClient,
    prompt: str,
    api_key: str,
    base_url: str,
    model: str,
    provider_name: str,
    stream_enabled: bool,
    max_steps: int,
) -> int:
    tools = client.list_tools()
    tool_schema = build_tool_schema(tools)
    model_info = resolve_model(os.environ.get("LLM_MODEL_ID", "").strip(), model, provider_name)
    provider = build_provider(model_info.provider, api_key, base_url, model_info.api_model)
    tool_names = [tool.get("name", "") for tool in tools if tool.get("name")]
    tool_list_text = "，".join(tool_names)
    system_prompt = (
        "你是一个会使用工具的助手。需要时调用工具。完成后直接给出答案。回复内容尽量使用中文。"
        f"可用工具：{tool_list_text}。如果任务需要写文件且存在 write 工具，必须使用 write。"
    )
    messages: List[Message] = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": prompt},
    ]

    total_usage = TokenUsage()

    for step_index in range(1, max_steps + 1):
        print(f"\n========== LLM 输入 (step {step_index}) ==========", file=sys.stderr)
        print(json.dumps(messages, ensure_ascii=False, indent=2), file=sys.stderr)
        try:
            if stream_enabled:
                events = provider.stream(messages, tool_schema)
                response = response_from_events(events)
            else:
                response = provider.request(messages, tool_schema)
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", errors="replace")
            print(f"LLM request failed: {e.code} {e.reason}\n{body}", file=sys.stderr)
            return 1
        except Exception as e:
            print(f"LLM request failed: {e}", file=sys.stderr)
            return 1

        print(f"\n========== LLM 回复 (step {step_index}) ==========", file=sys.stderr)
        print(
            json.dumps(
                {"message": response.message, "tool_calls": response.tool_calls},
                ensure_ascii=False,
                indent=2,
            ),
            file=sys.stderr,
        )
        messages.append(build_assistant_message(response.message, response.tool_calls))

        total_usage.add(response.usage)

        if not response.tool_calls:
            content = response.message.get("content") or ""
            if content:
                print(content)
            if total_usage.total_tokens > 0:
                cost = (
                    model_info.cost_per_1m_in / 1_000_000 * total_usage.input_tokens
                    + model_info.cost_per_1m_out / 1_000_000 * total_usage.output_tokens
                )
                print(
                    f"Token usage: input={total_usage.input_tokens} "
                    f"output={total_usage.output_tokens} "
                    f"total={total_usage.total_tokens} "
                    f"cost_usd={cost:.6f}",
                    file=sys.stderr,
                )
            return 0

        for call in response.tool_calls:
            call_id = call.get("id", "")
            name = call.get("name", "")
            arguments = call.get("arguments", "{}")
            args_obj = normalize_arguments(arguments)

            try:
                result = client.call_tool(name, args_obj)
                tool_content = result.get("content", "")
            except Exception as e:
                tool_content = f"tool call failed: {e}"

            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": call_id,
                    "name": name,
                    "content": tool_content,
                }
            )

    print("Max steps reached without a final answer.", file=sys.stderr)
    if total_usage.total_tokens > 0:
        cost = (
            model_info.cost_per_1m_in / 1_000_000 * total_usage.input_tokens
            + model_info.cost_per_1m_out / 1_000_000 * total_usage.output_tokens
        )
        print(
            f"Token usage: input={total_usage.input_tokens} "
            f"output={total_usage.output_tokens} "
            f"total={total_usage.total_tokens} "
            f"cost_usd={cost:.6f}",
            file=sys.stderr,
        )
    return 1


def main() -> int:
    repo_root = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
    load_env_file(os.path.join(repo_root, ".env"))
    if os.name == "nt":
        toolserver_candidates = [os.path.join(repo_root, "toolserver.exe"), os.path.join(repo_root, "toolserver")]
    else:
        toolserver_candidates = [os.path.join(repo_root, "toolserver")]

    toolserver = next((p for p in toolserver_candidates if os.path.exists(p)), None)
    if not toolserver:
        print("toolserver binary not found. Build it first:", file=sys.stderr)
        if os.name == "nt":
            print("  go build -o toolserver.exe ./cmd/toolserver", file=sys.stderr)
        else:
            print("  go build -o toolserver ./cmd/toolserver", file=sys.stderr)
        return 2

    env = dict(os.environ)
    env["TOOLSERVER_ROOT"] = repo_root

    proc = subprocess.Popen(
        [toolserver],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=None,
        env=env,
        cwd=repo_root,
    )
    assert proc.stdin is not None
    assert proc.stdout is not None

    client = ToolServerClient(proc)
    try:
        provider_name = os.environ.get("LLM_PROVIDER", "openai").strip()
        provider_key = provider_name.lower().strip()
        api_key_env = "OPENAI_API_KEY"
        model_env = "OPENAI_MODEL"
        base_url_env = "OPENAI_BASE_URL"
        default_model = "gpt-4o-mini"
        if provider_key == "deepseek":
            api_key_env = "DEEPSEEK_API_KEY"
            model_env = "DEEPSEEK_MODEL"
            base_url_env = "DEEPSEEK_BASE_URL"
            default_model = "deepseek-chat"

        api_key = os.environ.get(api_key_env, "").strip()
        if not api_key:
            print(f"{api_key_env} is required for LLM mode.", file=sys.stderr)
            return 2

        model = os.environ.get(model_env, default_model).strip() or default_model
        base_url = os.environ.get(base_url_env, "").strip()
        stream_enabled = os.environ.get("AGENT_STREAM", "").strip().lower() in {"1", "true", "yes"}
        max_steps_str = os.environ.get("AGENT_MAX_STEPS", "8").strip()
        try:
            max_steps = int(max_steps_str)
        except Exception:
            max_steps = 8

        if len(sys.argv) > 1:
            prompt = " ".join(sys.argv[1:])
        else:
            prompt = "Summarize the Go tools available and show how to list *.go files."
        prompt = strip_tool_order_instructions(prompt)

        try:
            return run_agent(
                client,
                prompt,
                api_key,
                base_url,
                model,
                provider_name,
                stream_enabled,
                max_steps,
            )
        except ValueError as e:
            print(str(e), file=sys.stderr)
            return 2
    finally:
        try:
            proc.kill()
        except Exception:
            pass

    return 0


if __name__ == "__main__":
    raise SystemExit(main())

