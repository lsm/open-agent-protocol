"""Provider auth listing and interactive login over agent-profile ``+auth``.

The runtime owns credentials. A local OAP login is a flow-id-scoped exchange
of start, URL/prompt/progress events, prompt replies, and one terminal. The
older Makai V1 exchange remains available only in explicit legacy mode.
"""

from __future__ import annotations

import asyncio
import inspect
import logging
from typing import Any, Dict, List, Mapping, Optional, cast

from ._diagnostics import TimeoutContext, build_diagnostics, format_timeout_message
from ._ids import new_ulid
from ._wire import build_stream_envelope, payload_of
from .errors import TIMEOUT_CODE, MakaiAuthError, MakaiStreamError, is_timeout_error
from .transport import StdioTransport
from .types import (
    AuthErrorEvent,
    AuthEvent,
    AuthFlowHandlers,
    AuthProgressEvent,
    AuthPromptEvent,
    AuthStatus,
    AuthSuccessEvent,
    AuthUrlEvent,
    ProviderAuthInfo,
)

__all__ = ["AuthApi", "flatten_auth_event"]

logger = logging.getLogger("oap_sdk.auth")

DEFAULT_FRAME_TIMEOUT_S = 30.0

_VALID_AUTH_STATUSES = {
    "authenticated",
    "login_required",
    "expired",
    "refreshing",
    "login_in_progress",
    "failed",
    "unknown",
}

_EVENT_VARIANTS = ("auth_url", "prompt", "progress", "success", "error")


class AuthApi:
    """Auth protocol client."""

    def __init__(
        self,
        transport: StdioTransport,
        *,
        handlers: Optional[AuthFlowHandlers] = None,
        frame_timeout: float = DEFAULT_FRAME_TIMEOUT_S,
    ) -> None:
        self._transport = transport
        self._default_handlers = handlers
        self._frame_timeout = frame_timeout

    async def list_providers(self) -> List[ProviderAuthInfo]:
        """Return every auth provider the runtime knows about, with status.

        Raises:
            MakaiAuthError: the request was rejected, timed out, or the
                response was malformed.
        """
        if not self._transport.legacy_wire:
            return await self._oap_list_providers()
        stream_id = new_ulid()
        envelope = build_stream_envelope("auth_providers_request", stream_id, {})
        context = TimeoutContext(
            "auth_providers_response",
            self._frame_timeout,
            stream_id=stream_id,
            message_id=stream_id,
        )
        logger.debug("auth_providers_request stream_id=%s", stream_id)

        async with self._transport.route(stream_id=stream_id) as route:
            await self._send(envelope)
            while True:
                frame = await self._next_frame(route, context)
                frame_type = frame.get("type")
                if frame_type == "ack":
                    continue
                if frame_type == "nack":
                    raise _nack_to_auth_error(frame)
                if frame_type == "auth_providers_response":
                    return _parse_providers(frame)
                raise MakaiAuthError(
                    "unexpected envelope type while awaiting auth_providers_response: "
                    f"{frame_type}",
                    kind="transport_error",
                )

    async def login(
        self,
        provider_id: str,
        handlers: Optional[AuthFlowHandlers] = None,
    ) -> None:
        """Run one interactive login flow for ``provider_id``.

        Handler resolution is per-call handlers first, then the client-level
        handlers, then none (spec §3.7). A ``prompt`` event with no
        ``on_prompt`` handler cancels the flow rather than hanging.

        Returns ``None`` on success -- the TypeScript SDK's ``{status:
        "success"}`` carries no additional information.

        Raises:
            asyncio.CancelledError: when the caller cancels the awaiting task.
                This propagates rather than being converted, so a cancelled
                login is indistinguishable from any other cancelled await; the
                SDK still sends ``auth_cancel`` before re-raising.
            MakaiAuthError: ``kind="cancelled"`` when the runtime reports the
                flow as cancelled, or when the SDK cancels it because a prompt
                arrived with no ``on_prompt`` handler;
                ``kind="provider_error"`` when the provider rejected the
                login; ``kind="transport_error"`` on timeouts.
        """
        if not self._transport.legacy_wire:
            return await self._oap_login(provider_id, handlers)
        effective = handlers or self._default_handlers
        flow_id = new_ulid()
        sequence = 1
        last_error: Optional[Dict[str, Optional[str]]] = None
        cancelled_locally = False
        settled = False

        logger.info("auth login provider_id=%s flow_id=%s", provider_id, flow_id)
        context = TimeoutContext(
            "auth_login_result/auth_event",
            self._frame_timeout,
            stream_id=flow_id,
            provider_id=provider_id,
        )

        async with self._transport.route(stream_id=flow_id) as route:
            await self._send(
                build_stream_envelope(
                    "auth_login_start",
                    flow_id,
                    {"provider_id": provider_id},
                    message_id=new_ulid(),
                    sequence=sequence,
                )
            )
            sequence += 1

            try:
                while True:
                    frame = await self._next_frame(route, context)
                    frame_type = frame.get("type")
                    if frame_type == "ack":
                        continue
                    if frame_type == "nack":
                        raise _nack_to_auth_error(frame)

                    if frame_type == "auth_event":
                        event = flatten_auth_event(_require_payload(frame))
                        await self._emit(effective, event)

                        if isinstance(event, AuthErrorEvent):
                            last_error = {"code": event.code, "message": event.message}
                            continue
                        if isinstance(event, AuthPromptEvent):
                            if effective is None or effective.on_prompt is None:
                                cancelled_locally = True
                                await self._cancel(flow_id, sequence)
                                sequence += 1
                                continue
                            answer = await self._ask(effective, event)
                            await self._send(
                                build_stream_envelope(
                                    "auth_prompt_response",
                                    flow_id,
                                    {
                                        "flow_id": flow_id,
                                        "prompt_id": event.prompt_id,
                                        "answer": answer,
                                    },
                                    message_id=new_ulid(),
                                    sequence=sequence,
                                )
                            )
                            sequence += 1
                        continue

                    if frame_type == "auth_login_result":
                        settled = True
                        status = _require_payload(frame).get("status")
                        if status == "success":
                            logger.info("auth login succeeded provider_id=%s", provider_id)
                            return
                        if status == "cancelled":
                            message = (last_error or {}).get("message")
                            if message is None:
                                message = (
                                    "auth login cancelled (no on_prompt handler configured)"
                                    if cancelled_locally
                                    else "auth login cancelled"
                                )
                            raise MakaiAuthError(
                                message, kind="cancelled", code=(last_error or {}).get("code")
                            )
                        if status == "failed":
                            raise MakaiAuthError(
                                (last_error or {}).get("message") or "auth login failed",
                                kind="provider_error",
                                code=(last_error or {}).get("code"),
                            )
                        raise MakaiAuthError(
                            f"unexpected auth_login_result status: {status}", kind="unknown"
                        )

                    raise MakaiAuthError(
                        f"unexpected envelope type during login flow: {frame_type}",
                        kind="transport_error",
                    )
            except BaseException:
                # Also covers asyncio.CancelledError, which is re-raised
                # unchanged: converting it to a normal exception would hide
                # the cancellation from wait_for, task groups, and
                # Task.cancelled(). The flow is still cancelled on the wire.
                if not settled:
                    await self._cancel(flow_id, sequence)
                raise

    async def _oap_list_providers(self) -> List[ProviderAuthInfo]:
        from ._oap import AGENT, envelope

        request = envelope(AGENT, "auth.providers.request", {})
        async with self._transport.route(request_id=request["id"]) as route:
            await self._transport.send(request)
            frame = await route.next_frame(self._frame_timeout)
        if frame.get("type") == "error.response":
            raise _oap_auth_error(frame)
        if frame.get("type") != "auth.providers.response":
            raise MakaiAuthError("expected auth.providers.response", kind="transport_error")
        providers = _require_payload(frame).get("providers")
        if not isinstance(providers, list):
            raise MakaiAuthError("auth.providers.response has no providers array", kind="transport_error")
        result: List[ProviderAuthInfo] = []
        for item in providers:
            if not isinstance(item, dict):
                raise MakaiAuthError("auth provider is not an object", kind="transport_error")
            status = item.get("auth_status", "unknown")
            if status not in _VALID_AUTH_STATUSES:
                status = "unknown"
            result.append(ProviderAuthInfo(
                id=str(item.get("id", "")), name=str(item.get("name", "")),
                auth_status=cast(AuthStatus, status),
                last_error=_optional_string_field(item, "last_error"),
            ))
        return result

    async def _oap_login(self, provider_id: str, handlers: Optional[AuthFlowHandlers]) -> None:
        from ._oap import AGENT, envelope

        if not provider_id:
            raise MakaiAuthError("login requires a provider id", kind="provider_error", code="invalid_request")
        effective = handlers or self._default_handlers
        start = envelope(AGENT, "auth.login.start.request", {"provider_id": provider_id})
        flow_id: Optional[str] = None
        settled = False
        expected_sequence = 1
        cancelled_locally = False
        async with self._transport.route(request_id=start["id"]) as flow:
            try:
                await self._transport.send(start)
                response = await flow.next_frame(self._frame_timeout)
                if response.get("type") == "error.response":
                    raise _oap_auth_error(response)
                if response.get("type") != "auth.login.start.response":
                    raise MakaiAuthError("expected auth.login.start.response", kind="transport_error")
                flow_id = _require_payload(response).get("flow_id")
                if not isinstance(flow_id, str) or not flow_id:
                    raise MakaiAuthError("auth login start omitted flow_id", kind="transport_error")

                while True:
                    frame = await flow.next_frame(self._frame_timeout)
                    kind = frame.get("type")
                    if kind == "error.response":
                        raise _oap_auth_error(frame)
                    if kind not in ("auth.login.event", "auth.login.completed"):
                        raise MakaiAuthError(f"unexpected auth flow envelope: {kind}", kind="transport_error")
                    sequence = frame.get("sequence")
                    if not isinstance(sequence, int) or sequence != expected_sequence:
                        raise MakaiAuthError("auth flow sequence gap", kind="transport_error", code="protocol_violation")
                    expected_sequence += 1
                    payload = _require_payload(frame)
                    if payload.get("flow_id") != flow_id or payload.get("provider_id") != provider_id:
                        raise MakaiAuthError("auth flow identity changed", kind="transport_error", code="protocol_violation")

                    if kind == "auth.login.completed":
                        settled = True
                        status = payload.get("status")
                        if status == "success":
                            await self._emit(effective, AuthSuccessEvent(flow_id=flow_id, provider_id=provider_id))
                            return
                        raw_error = payload.get("error")
                        error = raw_error if isinstance(raw_error, dict) else {}
                        message = error.get("message") or ("auth login cancelled" if status == "cancelled" else "auth login failed")
                        if cancelled_locally and status == "cancelled":
                            message = "auth login cancelled: no on_prompt handler is configured"
                        raise MakaiAuthError(str(message), kind="cancelled" if status == "cancelled" else "provider_error",
                                             code=error.get("code") if isinstance(error.get("code"), str) else None)

                    event_kind = payload.get("kind")
                    if event_kind == "url":
                        event: AuthEvent = AuthUrlEvent(flow_id=flow_id, provider_id=provider_id,
                            url=str(payload.get("url", "")), instructions=_optional_string_field(payload, "instructions"))
                    elif event_kind == "progress":
                        event = AuthProgressEvent(flow_id=flow_id, provider_id=provider_id,
                                                  message=str(payload.get("message", "")))
                    elif event_kind == "prompt":
                        event = AuthPromptEvent(flow_id=flow_id, provider_id=provider_id,
                            prompt_id=str(payload.get("prompt_id", "")), message=str(payload.get("message", "")),
                            allow_empty=payload.get("allow_empty") is True)
                    else:
                        raise MakaiAuthError("unknown OAP auth event kind", kind="transport_error")
                    await self._emit(effective, event)
                    if isinstance(event, AuthPromptEvent):
                        if effective is None or effective.on_prompt is None:
                            cancelled_locally = True
                            await self._transport.send_best_effort(envelope(
                                AGENT, "auth.login.cancel.request", {"flow_id": flow_id}))
                            continue
                        answer = await self._ask(effective, event)
                        if len(answer) > 4096 or (not answer and not event.allow_empty):
                            raise MakaiAuthError("auth prompt answer violates endpoint limits", kind="provider_error", code="invalid_request")
                        reply = envelope(AGENT, "auth.login.reply.request", {
                            "flow_id": flow_id, "prompt_id": event.prompt_id, "answer": answer})
                        async with self._transport.route(request_id=reply["id"]) as answer_route:
                            await self._transport.send(reply)
                            acknowledgement = await answer_route.next_frame(self._frame_timeout)
                        if acknowledgement.get("type") == "error.response":
                            raise _oap_auth_error(acknowledgement)
                        if acknowledgement.get("type") != "auth.login.reply.response" or not _require_payload(acknowledgement).get("accepted"):
                            raise MakaiAuthError("auth prompt answer was not accepted", kind="provider_error", code="invalid_request")
            except BaseException:
                if flow_id and not settled:
                    await self._transport.send_best_effort(envelope(
                        AGENT, "auth.login.cancel.request", {"flow_id": flow_id}))
                raise

    async def _ask(self, handlers: AuthFlowHandlers, event: AuthPromptEvent) -> str:
        assert handlers.on_prompt is not None
        try:
            result = handlers.on_prompt(event)
            if inspect.isawaitable(result):
                result = await result
        except (MakaiAuthError, asyncio.CancelledError):
            raise
        except Exception as exc:
            raise MakaiAuthError(str(exc), kind="unknown") from exc
        return result if isinstance(result, str) else ""

    async def _emit(self, handlers: Optional[AuthFlowHandlers], event: AuthEvent) -> None:
        if handlers is None or handlers.on_event is None:
            return
        try:
            result = handlers.on_event(event)
            if inspect.isawaitable(result):
                await result
        except MakaiAuthError:
            raise
        except Exception as exc:
            raise MakaiAuthError(str(exc), kind="unknown") from exc

    async def _cancel(self, flow_id: str, sequence: int) -> None:
        await self._transport.send_best_effort(
            build_stream_envelope(
                "auth_cancel",
                flow_id,
                {"flow_id": flow_id},
                message_id=new_ulid(),
                sequence=sequence,
            )
        )

    async def _send(self, envelope: Mapping[str, Any]) -> None:
        try:
            await self._transport.send(dict(envelope))
        except MakaiStreamError as exc:
            raise MakaiAuthError(str(exc), kind="transport_error") from exc

    async def _next_frame(self, route: Any, context: TimeoutContext) -> Dict[str, Any]:
        try:
            frame: Dict[str, Any] = await route.next_frame(self._frame_timeout)
        except MakaiStreamError as exc:
            if is_timeout_error(exc):
                raise MakaiAuthError(
                    format_timeout_message(context),
                    kind="transport_error",
                    code=TIMEOUT_CODE,
                    diagnostics=build_diagnostics(context),
                ) from exc
            raise MakaiAuthError(exc.message, kind="transport_error", code=exc.code) from exc
        return frame


def flatten_auth_event(payload: Mapping[str, Any]) -> AuthEvent:
    """Convert an ``auth_event`` payload into a typed event.

    The wire shape nests the event under its variant key, for example
    ``{"prompt": {...}}``.
    """
    for variant in _EVENT_VARIANTS:
        value = payload.get(variant)
        if isinstance(value, dict):
            return _normalize(variant, value)
    raise MakaiAuthError(f"unknown auth_event variant: {dict(payload)!r}", kind="unknown")


def _oap_auth_error(frame: Mapping[str, Any]) -> MakaiAuthError:
    payload = frame.get("payload")
    if not isinstance(payload, dict):
        payload = {}
    detail = payload.get("error")
    if not isinstance(detail, dict):
        detail = payload
    code = detail.get("code")
    return MakaiAuthError(
        str(detail.get("message") or "OAP auth request failed"),
        kind="provider_error", code=code if isinstance(code, str) else None,
    )


def _normalize(variant: str, data: Mapping[str, Any]) -> AuthEvent:
    flow_id = _string_field(data, "flow_id")
    provider_id = _string_field(data, "provider_id")
    if variant == "auth_url":
        return AuthUrlEvent(
            flow_id=flow_id,
            provider_id=provider_id,
            url=_string_field(data, "url"),
            instructions=_optional_string_field(data, "instructions"),
        )
    if variant == "prompt":
        allow_empty = data.get("allow_empty")
        return AuthPromptEvent(
            flow_id=flow_id,
            prompt_id=_string_field(data, "prompt_id"),
            provider_id=provider_id,
            message=_string_field(data, "message"),
            allow_empty=allow_empty if isinstance(allow_empty, bool) else False,
        )
    if variant == "progress":
        return AuthProgressEvent(
            flow_id=flow_id, provider_id=provider_id, message=_string_field(data, "message")
        )
    if variant == "success":
        return AuthSuccessEvent(flow_id=flow_id, provider_id=provider_id)
    return AuthErrorEvent(
        flow_id=flow_id,
        provider_id=provider_id,
        message=_string_field(data, "message"),
        code=_optional_string_field(data, "code"),
    )


def _string_field(data: Mapping[str, Any], key: str) -> str:
    value = data.get(key)
    if not isinstance(value, str):
        raise MakaiAuthError(
            f'auth_event field "{key}" missing or not a string', kind="transport_error"
        )
    return value


def _optional_string_field(data: Mapping[str, Any], key: str) -> Optional[str]:
    value = data.get(key)
    if isinstance(value, str) and value:
        return value
    return None


def _require_payload(frame: Mapping[str, Any]) -> Dict[str, Any]:
    payload = frame.get("payload")
    if not isinstance(payload, dict):
        raise MakaiAuthError(
            f"envelope {frame.get('type')} missing payload object", kind="transport_error"
        )
    return payload


def _parse_providers(frame: Mapping[str, Any]) -> List[ProviderAuthInfo]:
    payload = _require_payload(frame)
    providers = payload.get("providers")
    if not isinstance(providers, list):
        raise MakaiAuthError(
            "auth_providers_response payload missing providers array", kind="transport_error"
        )
    return [_parse_provider(entry, index) for index, entry in enumerate(providers)]


def _parse_provider(entry: Any, index: int) -> ProviderAuthInfo:
    if not isinstance(entry, dict):
        raise MakaiAuthError(
            f"provider entry at index {index} is not an object", kind="transport_error"
        )
    identifier = entry.get("id")
    name = entry.get("name")
    if not isinstance(identifier, str) or not isinstance(name, str):
        raise MakaiAuthError(
            f"provider entry at index {index} missing id/name", kind="transport_error"
        )
    status = entry.get("auth_status")
    auth_status: AuthStatus = (
        cast(AuthStatus, status)
        if isinstance(status, str) and status in _VALID_AUTH_STATUSES
        else "unknown"
    )
    last_error = entry.get("last_error")
    return ProviderAuthInfo(
        id=identifier,
        name=name,
        auth_status=auth_status,
        last_error=last_error if isinstance(last_error, str) and last_error else None,
    )


def _nack_to_auth_error(frame: Mapping[str, Any]) -> MakaiAuthError:
    payload = payload_of(frame)
    reason = payload.get("reason")
    code = payload.get("error_code")
    return MakaiAuthError(
        reason if isinstance(reason, str) else "transport nack",
        kind="transport_error",
        code=code if isinstance(code, str) else None,
    )
