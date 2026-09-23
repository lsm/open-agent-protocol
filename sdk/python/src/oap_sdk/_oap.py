"""OAP agent-control and model-provider client projections.

The public convenience APIs remain small; all bytes written to the child are
profiled OAP 0.1 envelopes. A response is correlated by ``in_reply_to`` and
events by ``session_id`` or ``inference_id``.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import time
from dataclasses import replace
from typing import Any, AsyncGenerator, Dict, List, Mapping, Optional, Sequence

from ._ids import new_ulid
from .errors import MakaiAuthRequiredError, MakaiProtocolError, MakaiStreamError
from .transport import Frame, FrameRoute, StdioTransport
from .types import (
    AgentEnd, AgentStart, AgentStreamEvent, AssistantMessage, ChatMessage,
    CompletionResponse, ContentPart, ListModelsResponse, MessageEnd, MessageStart,
    ModelDescriptor, ProviderStreamEvent, RunOptions, StreamError, TextDelta,
    ThinkingDelta, ToolCall, ToolDefinition, Usage,
)

PROTOCOL = "open-agent-protocol"
VERSION = "0.1"
AGENT = "open-agent-protocol.agent-control-core"
PROVIDER = "open-agent-protocol.model-provider-core"


def envelope(profile: str, kind: str, payload: Mapping[str, Any], *,
             id: Optional[str] = None, **scope: str) -> Frame:
    result: Frame = {"protocol": PROTOCOL, "version": VERSION,
                     "profile": profile, "type": kind, "id": id or new_ulid(),
                     "payload": dict(payload)}
    result.update(scope)
    return result


def _error(frame: Mapping[str, Any], *, provider_id: str = "") -> MakaiStreamError:
    payload = frame.get("payload")
    if not isinstance(payload, dict):
        payload = {}
    detail = payload.get("error")
    if not isinstance(detail, dict):
        detail = payload
    code = detail.get("code")
    message = detail.get("message")
    if code in ("credential_missing", "credential_expired", "credential_rejected", "auth_required"):
        return MakaiAuthRequiredError(provider_id, str(message or code))
    return MakaiStreamError(str(message or "OAP request failed"),
                            kind="provider_error", code=code if isinstance(code, str) else None,
                            provider_id=provider_id or None)


def _payload(frame: Mapping[str, Any]) -> Dict[str, Any]:
    value = frame.get("payload")
    if not isinstance(value, dict):
        raise MakaiProtocolError("OAP response has no object payload", "malformed_response")
    return value


def _model_parts(model_ref: str) -> tuple[str, str, str]:
    left, _, model = model_ref.partition("@")
    provider, _, wire = left.partition("/")
    return provider, wire, model


def _usage(value: Any) -> Optional[Usage]:
    if not isinstance(value, dict):
        return None
    return Usage(input=int(value.get("input_tokens") or 0),
                 output=int(value.get("output_tokens") or 0))


def _assistant(value: Any) -> AssistantMessage:
    if not isinstance(value, dict) or value.get("role") != "assistant":
        raise MakaiProtocolError("OAP completion omitted an assistant message", "malformed_response")
    content = value.get("content")
    if isinstance(content, str):
        return AssistantMessage(role="assistant", content=content)
    if not isinstance(content, list) or not content:
        raise MakaiProtocolError("OAP assistant content is not text or a nonempty part list", "malformed_response")
    parts: List[ContentPart] = []
    for raw in content:
        if not isinstance(raw, dict):
            raise MakaiProtocolError("OAP assistant part is not an object", "malformed_response")
        kind = raw.get("type")
        if kind == "text":
            if not isinstance(raw.get("text"), str):
                raise MakaiProtocolError("OAP text part omitted text", "malformed_response")
            parts.append({"type": "text", "text": raw["text"]})
        elif kind == "reasoning":
            if not isinstance(raw.get("reasoning"), str):
                raise MakaiProtocolError("OAP reasoning part omitted reasoning", "malformed_response")
            thinking: Dict[str, Any] = {"type": "thinking", "thinking": raw["reasoning"]}
            if isinstance(raw.get("carry"), str):
                thinking["thinking_signature"] = raw["carry"]
            parts.append(thinking)  # type: ignore[arg-type]
        elif kind == "image":
            image = raw.get("image")
            if not isinstance(image, dict):
                raise MakaiProtocolError("OAP image part omitted image", "malformed_response")
            if isinstance(image.get("url"), str) and image["url"]:
                parts.append({"type": "image", "url": image["url"]})
            elif isinstance(image.get("data"), str) and isinstance(image.get("media_type"), str):
                parts.append({"type": "image", "data": image["data"], "mime_type": image["media_type"]})
            else:
                raise MakaiProtocolError("OAP image has no supported source", "malformed_response")
        elif kind == "tool_call":
            if not isinstance(raw.get("tool_call_id"), str) or not isinstance(raw.get("name"), str) or "arguments_json" not in raw:
                raise MakaiProtocolError("OAP tool call omitted identity or arguments", "malformed_response")
            call: Dict[str, Any] = {"type": "tool_call", "tool_call_id": raw["tool_call_id"],
                                    "name": raw["name"], "arguments_json": json.dumps(raw["arguments_json"])}
            if isinstance(raw.get("carry"), str):
                call["carry"] = raw["carry"]
            parts.append(call)  # type: ignore[arg-type]
        elif kind == "tool_result":
            if not isinstance(raw.get("tool_call_id"), str):
                raise MakaiProtocolError("OAP tool result omitted identity", "malformed_response")
            result = raw.get("result")
            if isinstance(result, list):
                if not all(isinstance(item, dict) and item.get("type") == "text" and isinstance(item.get("text"), str) for item in result):
                    raise MakaiProtocolError("OAP tool result has unrepresentable content", "unsupported_feature")
                mapped_result: Any = [{"type": "text", "text": item["text"]} for item in result]
            elif isinstance(result, str):
                mapped_result = result
            else:
                raise MakaiProtocolError("OAP tool result has an unrepresentable JSON value", "unsupported_feature")
            parts.append({"type": "tool_result", "tool_call_id": raw["tool_call_id"],
                          "content": mapped_result, "is_error": raw.get("is_error") is True})
        else:
            raise MakaiProtocolError("OAP assistant part has no Python SDK projection", "unsupported_feature")
    return AssistantMessage(role="assistant", content=parts)


def _messages(messages: Sequence[ChatMessage]) -> List[Dict[str, Any]]:
    result: List[Dict[str, Any]] = []
    for message in messages:
        role = message.get("role")
        content = message.get("content")
        if role not in ("system", "developer", "user", "assistant", "tool"):
            raise MakaiProtocolError("unsupported message role", "invalid_request")
        if not isinstance(content, (str, list)):
            raise MakaiProtocolError("message content must be text or parts", "invalid_request")
        if message.get("name") and role != "tool":
            raise MakaiProtocolError("named non-tool messages have no OAP 0.1 projection", "unsupported_feature")
        if role == "tool" and isinstance(content, str):
            call_id = message.get("tool_call_id")
            if not call_id:
                raise MakaiProtocolError("tool result requires tool_call_id", "invalid_request")
            result.append({"role": role, "content": [{"type": "tool_result",
                           "tool_call_id": call_id, "result": content}]})
            continue
        if isinstance(content, list):
            parts: List[Dict[str, Any]] = []
            for part in content:
                p: Mapping[str, Any] = part
                kind = p.get("type")
                if kind == "text":
                    if p.get("text_signature"):
                        raise MakaiProtocolError("text signature has no OAP 0.1 projection", "unsupported_feature")
                    parts.append({"type": "text", "text": p["text"]})
                elif kind == "thinking":
                    item = {"type": "reasoning", "reasoning": p["thinking"]}
                    if p.get("thinking_signature"):
                        item["carry"] = p["thinking_signature"]
                    parts.append(item)
                elif kind == "image":
                    image = {"url": p["url"]} if p.get("url") else {"data": p["data"], "media_type": p["mime_type"]}
                    parts.append({"type": "image", "image": image})
                elif kind == "tool_call":
                    try:
                        arguments = json.loads(p["arguments_json"])
                    except (json.JSONDecodeError, KeyError) as exc:
                        raise MakaiProtocolError("tool call arguments_json is not valid JSON", "invalid_request") from exc
                    call = {"type": "tool_call", "tool_call_id": p["tool_call_id"],
                            "name": p["name"], "arguments_json": arguments}
                    if p.get("carry"):
                        call["carry"] = p["carry"]
                    parts.append(call)
                elif kind == "tool_result":
                    if p.get("details_json"):
                        raise MakaiProtocolError("tool result details_json has no OAP 0.1 projection", "unsupported_feature")
                    parts.append({"type": "tool_result", "tool_call_id": p["tool_call_id"],
                                  "result": p["content"], "is_error": p.get("is_error", False)})
                else:
                    raise MakaiProtocolError("content part has no OAP 0.1 projection", "unsupported_feature")
            if role == "tool" and message.get("tool_call_id") and all(p["type"] == "text" for p in parts):
                parts = [{"type": "tool_result", "tool_call_id": message["tool_call_id"], "result": parts}]
            result.append({"role": role, "content": parts})
        else:
            result.append({"role": role, "content": content})
    return result


async def _request(transport: StdioTransport, profile: str, kind: str,
                   payload: Mapping[str, Any], timeout: float, **scope: str) -> Frame:
    frame = envelope(profile, kind, payload, **scope)
    async with transport.route(request_id=frame["id"]) as route:
        await transport.send(frame)
        response = await route.next_frame(timeout)
    if response.get("type") in ("error", "error.response"):
        raise _error(response)
    return response


class OAPModelsApi:
    def __init__(self, transport: StdioTransport, *, response_timeout: float = 5.0) -> None:
        self._transport = transport
        self._timeout = response_timeout

    async def list(self, *, provider_id: Optional[str] = None, api: Optional[str] = None,
                   model_id: Optional[str] = None, include_deprecated: Optional[bool] = None,
                   include_login_required: Optional[bool] = None) -> ListModelsResponse:
        payload = {"provider_id": provider_id} if provider_id else {}
        frame = await _request(self._transport, PROVIDER, "provider.models.list.request", payload, self._timeout)
        if frame.get("type") != "provider.models.list.response":
            raise MakaiProtocolError("expected provider.models.list.response", "malformed_response")
        raw = _payload(frame).get("models")
        if not isinstance(raw, list):
            raise MakaiProtocolError("models must be an array", "malformed_response")
        models: List[ModelDescriptor] = []
        for item in raw:
            if not isinstance(item, dict):
                raise MakaiProtocolError("model entry must be an object", "malformed_response")
            if api and item.get("wire") != api:
                continue
            if model_id and item.get("model_id") != model_id:
                continue
            if include_deprecated is False and item.get("lifecycle") == "deprecated":
                continue
            if include_login_required is False and item.get("auth_status") == "login_required":
                continue
            models.append(ModelDescriptor(
                model_ref=str(item.get("model_ref", "")),
                model_id=str(item.get("model_id", "")),
                display_name=str(item.get("display_name") or item.get("model_id") or ""),
                provider_id=str(item.get("provider_id", "")),
                api=str(item.get("wire", "")),
                auth_status=item.get("auth_status", "unknown"),
                lifecycle=item.get("lifecycle", "stable"),
                capabilities=item.get("capabilities", []),
                source="static_fallback" if item.get("source") == "fallback" else "dynamic",
                context_window=item.get("context_window"),
                max_output_tokens=item.get("max_output_tokens"),
                reasoning_default=item.get("reasoning_default"),
            ))
        return ListModelsResponse(models=models, fetched_at_ms=int(time.time() * 1000), cache_max_age_ms=0)

    async def resolve(self, *, provider_id: str, model_id: str, api: Optional[str] = None) -> ModelDescriptor:
        response = await self.list(provider_id=provider_id, model_id=model_id, api=api)
        if len(response.models) != 1:
            raise MakaiProtocolError("model not found or ambiguous", "model_not_found")
        return response.models[0]


class OAPProviderApi:
    def __init__(self, transport: StdioTransport, *, response_timeout: float = 30.0,
                 auth_retry_policy: Optional[str] = None, auth: Any = None) -> None:
        self._transport = transport
        self._timeout = response_timeout
        self._auth_retry_policy = auth_retry_policy
        self._auth = auth

    def _create_payload(self, model_ref: str, messages: Sequence[ChatMessage],
                        tools: Optional[Sequence[ToolDefinition]], options: Optional[RunOptions]) -> Dict[str, Any]:
        if not model_ref:
            raise MakaiProtocolError("model_ref is required", "invalid_request")
        payload: Dict[str, Any] = {"model_ref": model_ref, "messages": _messages(messages),
                                   "stream": True, "include_snapshot": "never"}
        if tools:
            payload["tools"] = [{"name": tool.name, "description": tool.description,
                                  "input_schema": json.loads(tool.parameters_schema_json)} for tool in tools]
        if options is not None:
            if options.temperature is not None:
                payload["temperature"] = options.temperature
            if options.max_tokens is not None:
                payload["max_output_tokens"] = options.max_tokens
            if options.reasoning_effort is not None:
                payload["reasoning"] = {"enabled": options.reasoning_effort != "off",
                                         "effort": options.reasoning_effort}
            if options.metadata is not None:
                payload["metadata"] = dict(options.metadata)
        return payload

    async def _frames(self, model_ref: str, messages: Sequence[ChatMessage],
                      tools: Optional[Sequence[ToolDefinition]], options: Optional[RunOptions]
                      ) -> AsyncGenerator[Frame, None]:
        request = envelope(PROVIDER, "inference.create.request",
                           self._create_payload(model_ref, messages, tools, options))
        inference_id: Optional[str] = None
        terminal = False
        async with self._transport.route(request_id=request["id"]) as route:
            try:
                await self._transport.send(request)
                while True:
                    frame = await route.next_frame(self._timeout)
                    kind = frame.get("type")
                    if kind == "inference.create.response":
                        response = _payload(frame)
                        if not response.get("accepted"):
                            raise _error(frame, provider_id=_model_parts(model_ref)[0])
                        inference_id = frame.get("inference_id")
                        continue
                    if kind == "error":
                        raise _error(frame, provider_id=_model_parts(model_ref)[0])
                    yield frame
                    if kind in ("inference.completed", "inference.failed"):
                        terminal = True
                        return
            finally:
                if inference_id and not terminal:
                    await self._transport.send_best_effort(envelope(
                        PROVIDER, "inference.cancel.request", {"reason": "caller_closed"},
                        inference_id=inference_id))

    async def complete(self, *, model_ref: str, messages: Sequence[ChatMessage],
                       tools: Optional[Sequence[ToolDefinition]] = None,
                       options: Optional[RunOptions] = None) -> CompletionResponse:
        policy = options.auth_retry_policy if options and options.auth_retry_policy else self._auth_retry_policy
        attempt = 0
        while True:
            try:
                return await self._complete_once(model_ref, messages, tools, options)
            except MakaiAuthRequiredError:
                if policy != "auto_once" or self._auth is None or attempt != 0:
                    raise
                await self._auth.login(_model_parts(model_ref)[0])
                attempt += 1

    async def _complete_once(self, model_ref: str, messages: Sequence[ChatMessage],
                             tools: Optional[Sequence[ToolDefinition]],
                             options: Optional[RunOptions]) -> CompletionResponse:
        emitted_content = False
        async with contextlib.aclosing(self._frames(model_ref, messages, tools, options)) as frames:
            async for frame in frames:
                kind = frame.get("type")
                if kind == "inference.part.delta":
                    emitted_content = True
                if kind == "inference.failed":
                    failure = _error(frame, provider_id=_model_parts(model_ref)[0])
                    if emitted_content and isinstance(failure, MakaiAuthRequiredError):
                        raise MakaiStreamError(failure.message, kind="provider_error",
                                               code=failure.code, provider_id=failure.provider_id)
                    raise failure
                if kind == "inference.completed":
                    payload = _payload(frame)
                    provider, wire, model = _model_parts(model_ref)
                    return CompletionResponse(message=_assistant(payload.get("message")),
                                              provider_id=provider, api=wire, model_id=model,
                                              usage=_usage(payload.get("usage")),
                                              stop_reason=payload.get("stop_reason"))
        raise MakaiStreamError("inference ended without a terminal event", kind="transport_error")

    async def stream(self, *, model_ref: str, messages: Sequence[ChatMessage],
                     tools: Optional[Sequence[ToolDefinition]] = None,
                     options: Optional[RunOptions] = None) -> AsyncGenerator[ProviderStreamEvent, None]:
        policy = options.auth_retry_policy if options and options.auth_retry_policy else self._auth_retry_policy
        attempt = 0
        while True:
            yielded = False
            try:
                async with contextlib.aclosing(self._stream_once(model_ref, messages, tools, options)) as events:
                    async for event in events:
                        yielded = True
                        yield event
                return
            except MakaiAuthRequiredError:
                if policy != "auto_once" or self._auth is None or attempt != 0 or yielded:
                    raise
                await self._auth.login(_model_parts(model_ref)[0])
                attempt += 1

    async def _stream_once(self, model_ref: str, messages: Sequence[ChatMessage],
                           tools: Optional[Sequence[ToolDefinition]],
                           options: Optional[RunOptions]) -> AsyncGenerator[ProviderStreamEvent, None]:
        part_kinds: Dict[int, str] = {}
        provider, wire, model = _model_parts(model_ref)
        async with contextlib.aclosing(self._frames(model_ref, messages, tools, options)) as frames:
            async for frame in frames:
                kind = frame.get("type")
                payload = _payload(frame)
                if kind == "inference.started":
                    yield MessageStart(provider_id=provider, api=wire, model_id=model)
                elif kind == "inference.part.started":
                    part_kinds[int(payload.get("part_index", 0))] = str(payload.get("part_kind", "text"))
                elif kind == "inference.part.delta":
                    index = int(payload.get("part_index", 0))
                    delta = str(payload.get("delta", ""))
                    if part_kinds.get(index) == "reasoning":
                        yield ThinkingDelta(delta=delta)
                    elif part_kinds.get(index, "text") == "text":
                        yield TextDelta(delta=delta)
                elif kind == "inference.part.ended" and payload.get("part_kind") == "tool_call":
                    call = payload.get("tool_call")
                    if isinstance(call, dict):
                        arguments = call.get("arguments_json", "{}")
                        yield ToolCall(tool_call_id=str(call.get("tool_call_id", "")),
                                       name=str(call.get("name", "")),
                                       arguments_json=arguments if isinstance(arguments, str) else json.dumps(arguments))
                elif kind == "inference.completed":
                    yield MessageEnd(usage=_usage(payload.get("usage")),
                                     stop_reason=payload.get("stop_reason"))
                elif kind == "inference.failed":
                    err = _error(frame, provider_id=provider)
                    yield StreamError(message=err.message, code=err.code, provider_id=provider)


class OAPAgentApi:
    def __init__(self, transport: StdioTransport, *, response_timeout: float = 30.0,
                 auth_retry_policy: Optional[str] = None, auth: Any = None,
                 models: OAPModelsApi) -> None:
        self._transport = transport
        self._timeout = response_timeout
        self._auth_retry_policy = auth_retry_policy
        self._auth = auth
        self.models = models

    async def open_session(self, session_id: Optional[str] = None) -> Mapping[str, Any]:
        payload = {"session_id": session_id} if session_id else {}
        frame = await _request(self._transport, AGENT, "session.open.request", payload,
                               self._timeout, **({"session_id": session_id} if session_id else {}))
        if frame.get("type") != "session.open.response":
            raise MakaiProtocolError("expected session.open.response", "malformed_response")
        return _payload(frame)

    async def available_models(self, session_id: str) -> Mapping[str, Any]:
        frame = await _request(self._transport, AGENT, "models.request",
                               {"session_id": session_id}, self._timeout,
                               session_id=session_id)
        if frame.get("type") != "models.response":
            raise MakaiProtocolError("expected models.response", "malformed_response")
        return _payload(frame)

    async def _frames(self, model_ref: Optional[str], messages: Sequence[ChatMessage],
                      tools: Optional[Sequence[ToolDefinition]], options: Optional[RunOptions]
                      ) -> AsyncGenerator[Frame, None]:
        if tools:
            raise MakaiProtocolError("client-executed tools are not advertised by this OAP agent endpoint",
                                     "unsupported_feature")
        if options and (options.max_tokens is not None or options.temperature is not None or
                        options.reasoning_effort is not None):
            raise MakaiProtocolError("agent sampling and token options have no OAP 0.1 projection",
                                     "unsupported_feature")
        if not model_ref and not (options and options.session_id):
            raise MakaiProtocolError("a model_ref or an existing session_id is required", "invalid_request")
        session_id = options.session_id if options and options.session_id else new_ulid()
        async with self._transport.route(session_id=session_id) as events:
            opened = await _request(self._transport, AGENT, "session.open.request",
                                    {"session_id": session_id}, self._timeout, session_id=session_id)
            if opened.get("type") != "session.open.response":
                raise MakaiProtocolError("expected session.open.response", "malformed_response")
            payload: Dict[str, Any] = {"session_id": session_id, "messages": _messages(messages),
                                       "delivery": "auto"}
            if model_ref:
                payload["model_id"] = model_ref
            if options and options.metadata:
                payload["metadata"] = dict(options.metadata)
            submitted = await _request(self._transport, AGENT, "session.message.submit.request",
                                       payload, self._timeout, session_id=session_id)
            if submitted.get("type") != "session.message.submit.response":
                raise MakaiProtocolError("expected session.message.submit.response", "malformed_response")
            admission = _payload(submitted)
            run_id = admission.get("run_id")
            selected_ref = str(admission.get("model_id") or model_ref or "")
            terminal = False
            emitted_content = False
            try:
                while True:
                    frame = await events.next_frame(self._timeout)
                    if run_id and frame.get("run_id") not in (None, run_id):
                        continue
                    kind = frame.get("type")
                    if kind == "run.started":
                        selected_ref = str(_payload(frame).get("model_id") or selected_ref)
                    if kind == "content.delta":
                        emitted_content = True
                    if kind in ("run.failed", "run.cancelled", "error.response"):
                        failure = _error(frame, provider_id=_model_parts(selected_ref)[0])
                        if emitted_content and isinstance(failure, MakaiAuthRequiredError):
                            raise MakaiStreamError(failure.message, kind="provider_error",
                                                   code=failure.code, provider_id=failure.provider_id)
                        raise failure
                    yield frame
                    if kind == "run.completed":
                        terminal = True
                        return
            finally:
                if run_id and not terminal:
                    await self._transport.send_best_effort(envelope(
                        AGENT, "run.cancel.request", {"session_id": session_id,
                                                     "run_id": run_id, "reason": "caller_closed"},
                        session_id=session_id, run_id=run_id))

    async def run(self, *, model_ref: Optional[str] = None, messages: Sequence[ChatMessage],
                  tools: Optional[Sequence[ToolDefinition]] = None,
                  options: Optional[RunOptions] = None) -> CompletionResponse:
        policy = options.auth_retry_policy if options and options.auth_retry_policy else self._auth_retry_policy
        try:
            return await self._run_once(model_ref, messages, tools, options)
        except MakaiAuthRequiredError as failure:
            provider_id = failure.provider_id or _model_parts(model_ref or "")[0]
            if policy != "auto_once" or self._auth is None or not provider_id:
                raise
            await self._auth.login(provider_id)
            retry_options = replace(options, session_id=None) if options and model_ref else options
            return await self._run_once(model_ref, messages, tools, retry_options)

    async def _run_once(self, model_ref: Optional[str], messages: Sequence[ChatMessage],
                        tools: Optional[Sequence[ToolDefinition]],
                        options: Optional[RunOptions]) -> CompletionResponse:
        async with contextlib.aclosing(self._frames(model_ref, messages, tools, options)) as frames:
            async for frame in frames:
                if frame.get("type") == "run.completed":
                    value = _payload(frame)
                    actual_ref = value.get("model_id") or model_ref or ""
                    provider, wire, model = _model_parts(actual_ref)
                    return CompletionResponse(message=_assistant(value.get("final_response")),
                                              provider_id=provider, api=wire, model_id=model,
                                              usage=_usage(value.get("usage")),
                                              stop_reason=value.get("stop_reason"))
        raise MakaiStreamError("agent run ended without completion", kind="transport_error")

    async def stream(self, *, model_ref: Optional[str] = None, messages: Sequence[ChatMessage],
                     tools: Optional[Sequence[ToolDefinition]] = None,
                     options: Optional[RunOptions] = None) -> AsyncGenerator[AgentStreamEvent, None]:
        async with contextlib.aclosing(self._frames(model_ref, messages, tools, options)) as frames:
            async for frame in frames:
                kind = frame.get("type")
                value = _payload(frame)
                if kind == "run.started":
                    yield AgentStart(session_id=frame.get("session_id"))
                elif kind == "content.delta":
                    part = value.get("part")
                    if isinstance(part, dict):
                        if part.get("type") == "text":
                            yield TextDelta(delta=str(part.get("text", "")))
                        elif part.get("type") == "reasoning":
                            yield ThinkingDelta(delta=str(part.get("reasoning", "")))
                elif kind == "run.completed":
                    actual_ref = value.get("model_id") or model_ref or ""
                    provider, wire, _ = _model_parts(actual_ref)
                    yield AgentEnd(usage=_usage(value.get("usage")),
                                   stop_reason=value.get("stop_reason"),
                                   provider_id=provider, api=wire)

    async def switch_model(self, session_id: str, model_ref: str) -> Mapping[str, Any]:
        frame = await _request(self._transport, AGENT, "session.model.switch.request",
                               {"session_id": session_id, "model_id": model_ref},
                               self._timeout, session_id=session_id)
        return _payload(frame)

    async def attach_provider(self, session_id: str, provider: Mapping[str, Any]) -> Mapping[str, Any]:
        frame = await _request(self._transport, AGENT, "session.provider.attach.request",
                               {"session_id": session_id, "provider": dict(provider)},
                               self._timeout, session_id=session_id)
        return _payload(frame)
