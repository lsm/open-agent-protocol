"""Profiled OAP 0.1 newline-delimited stdio transport.

The default connection sends ``protocol.initialize.request`` to a combined
``oapx serve agent,provider --stdio`` host; explicit legacy mode waits for
the Makai V1 ``ready`` frame. One reader routes correlated replies and
session, inference, and auth-flow events to registered consumers. Unroutable
late frames are dropped. Closing the transport reaps its child process and
fails all pending consumers.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import sys
from types import TracebackType
from typing import Any, Dict, Mapping, Optional, Sequence, Type

from .binary import BinaryResolverOptions, resolve_makai_binary
from .errors import TIMEOUT_CODE, MakaiStreamError

__all__ = ["Frame", "StdioTransport", "FrameRoute"]

Frame = Dict[str, Any]

logger = logging.getLogger("oap_sdk.transport")

DEFAULT_HANDSHAKE_TIMEOUT_S = 5.0
DEFAULT_PROTOCOL_VERSION = "0.1"
_EXIT_GRACE_S = 0.5
_TERMINATE_GRACE_S = 1.0
_MAX_LINE_BYTES = 64 * 1024 * 1024


async def _reap_child(process: "asyncio.subprocess.Process") -> None:
    """Close stdin, then escalate wait -> terminate -> kill until the child exits."""
    if process.stdin is not None:
        with contextlib.suppress(Exception):
            process.stdin.close()
    try:
        await asyncio.wait_for(process.wait(), _EXIT_GRACE_S)
    except (asyncio.TimeoutError, asyncio.CancelledError):
        with contextlib.suppress(ProcessLookupError):
            process.terminate()
        try:
            await asyncio.wait_for(process.wait(), _TERMINATE_GRACE_S)
        except (asyncio.TimeoutError, asyncio.CancelledError):
            with contextlib.suppress(ProcessLookupError):
                process.kill()
            with contextlib.suppress(Exception):
                await process.wait()

    # Release asyncio's subprocess transport now that the child has exited.
    # Left to the garbage collector it would run its own cleanup later,
    # potentially after the OS has recycled the pid.
    child_transport = getattr(process, "_transport", None)
    if child_transport is not None:
        with contextlib.suppress(Exception):
            child_transport.close()


class FrameRoute:
    """A queue of frames for one ``stream_id`` or ``session_id``.

    Obtained from :meth:`StdioTransport.route`, which is an async context
    manager: the route is registered on entry and unregistered on exit, so
    late frames for a finished request are dropped rather than leaked.
    """

    __slots__ = ("key", "_queue", "_closed")

    def __init__(self, key: str) -> None:
        self.key = key
        self._queue: asyncio.Queue[Any] = asyncio.Queue()
        self._closed = False

    def _push(self, frame: Frame) -> None:
        self._queue.put_nowait(frame)

    def _fail(self, error: BaseException) -> None:
        if self._closed:
            return
        self._closed = True
        self._queue.put_nowait(error)

    async def next_frame(self, timeout: float) -> Frame:
        """Return the next frame for this route.

        Raises:
            MakaiStreamError: on timeout, or when the child process died.
        """
        try:
            item = await asyncio.wait_for(self._queue.get(), timeout)
        except asyncio.TimeoutError as exc:
            raise MakaiStreamError(
                f"timed out waiting for frame for {self.key} after {int(timeout * 1000)}ms",
                kind="transport_error",
                code=TIMEOUT_CODE,
            ) from exc
        if isinstance(item, BaseException):
            # Re-arm so a second waiter sees the same terminal condition.
            self._queue.put_nowait(item)
            raise item
        frame: Frame = item
        return frame

    async def drain(self, idle: float = 0.05, budget: float = 0.25) -> None:
        """Consume frames until the route goes quiet or ``budget`` elapses.

        Used after cancellation so a subsequent request on the same transport
        does not observe the previous request's trailing frames.
        """
        loop = asyncio.get_running_loop()
        deadline = loop.time() + budget
        while loop.time() < deadline:
            remaining = min(idle, deadline - loop.time())
            if remaining <= 0:
                return
            try:
                item = await asyncio.wait_for(self._queue.get(), remaining)
            except (asyncio.TimeoutError, asyncio.CancelledError):
                return
            except Exception:
                return
            if isinstance(item, BaseException):
                self._queue.put_nowait(item)
                return


class _RouteHandle:
    """Async context manager returned by :meth:`StdioTransport.route`."""

    __slots__ = ("_transport", "_kind", "_key", "_route")

    def __init__(self, transport: "StdioTransport", kind: str, key: str) -> None:
        self._transport = transport
        self._kind = kind
        self._key = key
        self._route: Optional[FrameRoute] = None

    async def __aenter__(self) -> FrameRoute:
        self._route = self._transport._open_route(self._kind, self._key)
        return self._route

    async def __aexit__(
        self,
        exc_type: Optional[Type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        self._transport._close_route(self._kind, self._key, self._route)


class StdioTransport:
    """Owns the combined OAP (or explicit legacy) child process and routes."""

    def __init__(
        self,
        *,
        command: Optional[str] = None,
        args: Optional[Sequence[str]] = None,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        resolver: Optional[BinaryResolverOptions] = None,
        expected_protocol_version: str = DEFAULT_PROTOCOL_VERSION,
        handshake_timeout: float = DEFAULT_HANDSHAKE_TIMEOUT_S,
        stderr_to: Optional[int] = None,
        legacy_wire: Optional[bool] = None,
    ) -> None:
        self._command = command
        self._legacy_wire = (os.environ.get("OAP_SDK_LEGACY_WIRE") == "1") if legacy_wire is None else legacy_wire
        self._args = list(args) if args is not None else (
            ["--stdio"] if self._legacy_wire else ["serve", "agent,provider", "--stdio"])
        self._cwd = cwd
        self._env = dict(env) if env is not None else None
        self._resolver = resolver
        self._expected_protocol_version = expected_protocol_version
        self._handshake_timeout = handshake_timeout
        self._stderr_to = stderr_to

        self._process: Optional[asyncio.subprocess.Process] = None
        self._reader_task: Optional[asyncio.Task[None]] = None
        self._teardown_task: Optional[asyncio.Task[None]] = None
        self._stream_routes: Dict[str, FrameRoute] = {}
        self._session_routes: Dict[str, FrameRoute] = {}
        self._request_routes: Dict[str, FrameRoute] = {}
        self._inference_routes: Dict[str, FrameRoute] = {}
        self._auth_flow_routes: Dict[str, FrameRoute] = {}
        self._handshake: Optional[asyncio.Future[None]] = None
        self._agent_revision: Optional[str] = None
        self._closed = False
        self._write_lock = asyncio.Lock()
        self._connect_lock = asyncio.Lock()
        self.dropped_frames = 0
        """Count of frames that arrived for a route nobody had open."""

    @property
    def connected(self) -> bool:
        return self._process is not None and not self._closed

    @property
    def legacy_wire(self) -> bool:
        """Whether this connection explicitly uses the pre-OAP Makai wire."""
        return self._legacy_wire

    @property
    def pid(self) -> Optional[int]:
        return self._process.pid if self._process is not None else None

    async def connect(self) -> None:
        """Spawn the child and complete initialize or the legacy ready handshake.

        Serialized: the first suspension point used to be the spawn itself, so
        two concurrent calls could both pass the connected check, both spawn a
        child, and leave one of them orphaned with a second reader competing
        for the same stdout.
        """
        async with self._connect_lock:
            await self._connect_locked()

    async def _connect_locked(self) -> None:
        if self._process is not None:
            raise RuntimeError("transport is already connected")

        command = self._command
        if command is None:
            # Resolution may download a binary over HTTP with a 120s timeout;
            # running it inline would stall every other task on this loop.
            command = await asyncio.to_thread(resolve_makai_binary, self._resolver)
        logger.debug("spawning %s %s", command, self._args)

        env = self._env
        if env is None:
            env = dict(os.environ)

        try:
            process = await asyncio.create_subprocess_exec(
                command,
                *self._args,
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=self._stderr_to if self._stderr_to is not None else sys.stderr,
                cwd=self._cwd,
                env=env,
                limit=_MAX_LINE_BYTES,
            )
        except (OSError, ValueError) as exc:
            raise MakaiStreamError(
                f"failed to spawn oapx binary {command!r}: {exc}", kind="transport_error"
            ) from exc

        if self._closed:
            # close() ran while we were resolving the binary or spawning: it
            # saw no process to reap and returned. Installing this one anyway
            # would leave a live child behind a transport that refuses every
            # send until someone calls close() a second time.
            await _reap_child(process)
            raise MakaiStreamError(
                "transport was closed while connecting", kind="transport_error"
            )

        self._process = process
        loop = asyncio.get_running_loop()
        self._handshake = loop.create_future()
        self._reader_task = asyncio.create_task(self._read_loop(), name="oap-stdio-reader")

        # OAP does not emit a spontaneous ready frame. Initialize the agent
        # profile over the same connection used for subsequent requests.
        from ._ids import new_ulid

        if not self._legacy_wire:
            initialize_id = new_ulid()
            self._initialize_id = initialize_id
            await self.send({
                "protocol": "open-agent-protocol",
                "version": "0.1",
                "profile": "open-agent-protocol.agent-control-core",
                "type": "protocol.initialize.request",
                "id": initialize_id,
                "payload": {
                    "protocol_versions": ["0.1"],
                    "profiles": ["open-agent-protocol.agent-control-core"],
                },
            })

        try:
            await asyncio.wait_for(self._handshake, self._handshake_timeout)
        except asyncio.TimeoutError as exc:
            await self.close()
            raise MakaiStreamError(
                f"stdio handshake timed out after {int(self._handshake_timeout * 1000)}ms",
                kind="transport_error",
            ) from exc
        except BaseException:
            await self.close()
            raise
        if not self._legacy_wire:
            from ._ids import new_ulid
            capability_id = new_ulid()
            try:
                async with self.route(request_id=capability_id) as route:
                    await self.send({"protocol": "open-agent-protocol", "version": "0.1",
                                     "profile": "open-agent-protocol.agent-control-core",
                                     "type": "capabilities.request", "id": capability_id, "payload": {}})
                    capabilities = await route.next_frame(self._handshake_timeout)
                revision = capabilities.get("capability_revision")
                if capabilities.get("type") != "capabilities.response" or not isinstance(revision, str) or not revision:
                    raise MakaiStreamError("OAP capabilities response omitted capability_revision", kind="transport_error")
                self._agent_revision = revision
            except BaseException:
                await self.close()
                raise
        logger.debug("handshake complete (pid=%s)", process.pid)

    async def send(self, frame: Frame) -> None:
        """Write one frame to the child's stdin."""
        process = self._process
        if process is None or process.stdin is None or self._closed:
            raise MakaiStreamError("transport is not connected", kind="transport_error")
        if (frame.get("profile") == "open-agent-protocol.agent-control-core"
                and frame.get("type") not in ("protocol.initialize.request", "capabilities.request")
                and self._agent_revision):
            frame["capability_revision"] = self._agent_revision
        line = (json.dumps(frame, separators=(",", ":")) + "\n").encode()
        async with self._write_lock:
            try:
                process.stdin.write(line)
                await process.stdin.drain()
            except (BrokenPipeError, ConnectionResetError, RuntimeError) as exc:
                raise MakaiStreamError(
                    f"failed to write frame to makai stdin: {exc}", kind="transport_error"
                ) from exc

    async def send_best_effort(self, frame: Frame) -> None:
        """Send ``frame``, swallowing transport failures.

        Used for teardown frames (``abort_request``, ``agent_stop``) where a
        dead child is an acceptable outcome.
        """
        with contextlib.suppress(Exception):
            await self.send(frame)

    def route(self, *, stream_id: Optional[str] = None, session_id: Optional[str] = None,
              request_id: Optional[str] = None, inference_id: Optional[str] = None) -> _RouteHandle:
        """Open a frame route for an OAP request or scoped event stream.

        Use as ``async with transport.route(stream_id=...) as route:`` and send
        the request inside the block.
        """
        if sum(value is not None for value in (stream_id, session_id, request_id, inference_id)) != 1:
            raise ValueError("exactly one route key is required")
        if request_id is not None:
            return _RouteHandle(self, "request", request_id)
        if inference_id is not None:
            return _RouteHandle(self, "inference", inference_id)
        if stream_id is not None:
            return _RouteHandle(self, "stream", stream_id)
        assert session_id is not None
        return _RouteHandle(self, "session", session_id)

    def _open_route(self, kind: str, key: str) -> FrameRoute:
        routes = self._route_table(kind)
        existing = routes.get(key)
        if existing is not None:
            raise MakaiStreamError(
                f"a {kind} route for {key} is already open on this transport",
                kind="transport_error",
            )
        route = FrameRoute(key)
        routes[key] = route
        return route

    def _close_route(self, kind: str, key: str, route: Optional[FrameRoute]) -> None:
        routes = self._route_table(kind)
        if routes.get(key) is route:
            del routes[key]
        if kind == "request" and route is not None:
            for inference_id, owner in list(self._inference_routes.items()):
                if owner is route:
                    del self._inference_routes[inference_id]
            for flow_id, owner in list(self._auth_flow_routes.items()):
                if owner is route:
                    del self._auth_flow_routes[flow_id]

    async def close(self) -> None:
        """Terminate the child process and release every route.

        Safe to call more than once, and safe to call while requests are in
        flight -- open routes are failed rather than left hanging.
        """
        teardown = self._teardown_task
        if self._closed and self._process is None and teardown is None:
            return
        self._closed = True
        process = self._process
        self._process = None

        if process is not None:
            await _reap_child(process)

        # A fatal reader exit detaches its own teardown because it cannot
        # cancel-and-await itself; adopt it here so close() still returns only
        # once the child is gone.
        if teardown is not None:
            self._teardown_task = None
            if not teardown.done():
                with contextlib.suppress(asyncio.CancelledError, Exception):
                    await teardown

        task = self._reader_task
        self._reader_task = None
        if task is not None and not task.done():
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await task

        self._fail_all(MakaiStreamError("transport closed", kind="transport_error"))

    def _route_table(self, kind: str) -> Dict[str, FrameRoute]:
        return {
            "stream": self._stream_routes,
            "session": self._session_routes,
            "request": self._request_routes,
            "inference": self._inference_routes,
        }[kind]

    async def __aenter__(self) -> "StdioTransport":
        await self.connect()
        return self

    async def __aexit__(
        self,
        exc_type: Optional[Type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        await self.close()

    async def _read_loop(self) -> None:
        process = self._process
        assert process is not None and process.stdout is not None
        stdout = process.stdout
        try:
            while True:
                try:
                    line = await stdout.readline()
                except (asyncio.LimitOverrunError, ValueError) as exc:
                    self._abandon(
                        MakaiStreamError(
                            f"makai emitted an oversized frame: {exc}", kind="transport_error"
                        )
                    )
                    return
                if not line:
                    break
                text = line.decode("utf-8", errors="replace").strip()
                if not text:
                    continue
                try:
                    frame = json.loads(text)
                except json.JSONDecodeError:
                    self._on_invalid_line(len(line), "invalid_json")
                    continue
                if not isinstance(frame, dict):
                    self._on_invalid_line(len(line), "non_object_json")
                    continue
                self._dispatch(frame)
        except asyncio.CancelledError:
            raise
        except Exception as exc:  # pragma: no cover - defensive
            logger.debug("reader loop failed: %r", exc)
            self._abandon(
                MakaiStreamError(f"stdio reader failed: {exc}", kind="transport_error")
            )
            return

        code = await process.wait() if process.returncode is None else process.returncode
        self._abandon(
            MakaiStreamError(
                f"oapx process exited (code={code}) before the request completed",
                kind="transport_error",
            )
        )

    def _on_invalid_line(self, byte_count: int, category: str) -> None:
        # Auth prompt answers can be sensitive. Never log or return raw pipe
        # bytes, including when the endpoint emits malformed JSON.
        logger.warning("discarding invalid JSON frame category=%s bytes=%d", category, byte_count)
        handshake = self._handshake
        if handshake is not None and not handshake.done():
            handshake.set_exception(
                MakaiStreamError(
                    f"invalid JSON frame (category={category}, bytes={byte_count})",
                    kind="transport_error",
                )
            )

    def _dispatch(self, frame: Frame) -> None:
        handshake = self._handshake
        if handshake is not None and not handshake.done():
            self._settle_handshake(handshake, frame)
            return

        reply_to = frame.get("in_reply_to")
        if isinstance(reply_to, str):
            route = self._request_routes.get(reply_to)
            if route is not None:
                if frame.get("type") == "inference.create.response":
                    inference_id = frame.get("inference_id")
                    if isinstance(inference_id, str) and inference_id:
                        self._inference_routes[inference_id] = route
                if frame.get("type") == "auth.login.start.response":
                    payload = frame.get("payload")
                    flow_id = payload.get("flow_id") if isinstance(payload, dict) else None
                    if isinstance(flow_id, str) and flow_id:
                        self._auth_flow_routes[flow_id] = route
                route._push(frame)
                return
        if frame.get("type") in ("auth.login.event", "auth.login.completed"):
            payload = frame.get("payload")
            flow_id = payload.get("flow_id") if isinstance(payload, dict) else None
            if isinstance(flow_id, str):
                route = self._auth_flow_routes.get(flow_id)
                if route is not None:
                    route._push(frame)
                    return
        inference_id = frame.get("inference_id")
        if isinstance(inference_id, str):
            route = self._inference_routes.get(inference_id)
            if route is not None:
                route._push(frame)
                return
        stream_id = frame.get("stream_id")
        if isinstance(stream_id, str):
            route = self._stream_routes.get(stream_id)
            if route is not None:
                route._push(frame)
                return
        session_id = frame.get("session_id")
        if isinstance(session_id, str):
            route = self._session_routes.get(session_id)
            if route is not None:
                route._push(frame)
                return
        self.dropped_frames += 1
        if frame.get("type") == "error":
            # A host-level rejection (for example `unknown_envelope` for a
            # malformed or ambiguous frame) carries no routing key, so no
            # caller can observe it. Surface it rather than swallowing it.
            logger.warning(
                "makai reported a host-level error: code=%r message=%r",
                frame.get("code"),
                frame.get("message"),
            )
            return
        logger.debug(
            "dropping unroutable frame type=%r stream_id=%r session_id=%r",
            frame.get("type"),
            stream_id,
            session_id,
        )

    def _settle_handshake(self, handshake: "asyncio.Future[None]", frame: Frame) -> None:
        frame_type = frame.get("type")
        if self._legacy_wire and frame_type == "ready":
            version = str(frame.get("protocol_version", ""))
            if version != "1":
                handshake.set_exception(MakaiStreamError(
                    f"protocol version mismatch (expected 1, got {version})",
                    kind="transport_error", code="version_mismatch"))
            else:
                handshake.set_result(None)
            return
        if frame_type in ("error", "error.response"):
            raw_payload = frame.get("payload")
            payload = raw_payload if isinstance(raw_payload, dict) else frame
            nested_error = payload.get("error")
            if isinstance(nested_error, dict):
                payload = nested_error
            code = payload.get("code")
            handshake.set_exception(
                MakaiStreamError(
                    str(payload.get("message") or "stdio initialization failed"),
                    kind="transport_error",
                    code=code if isinstance(code, str) else None,
                )
            )
            return
        if frame.get("in_reply_to") != getattr(self, "_initialize_id", None) or frame_type != "protocol.initialize.response":
            handshake.set_exception(
                MakaiStreamError(
                    f"unexpected handshake frame type: {frame_type}", kind="transport_error"
                )
            )
            return
        raw_payload = frame.get("payload")
        payload = raw_payload if isinstance(raw_payload, dict) else {}
        version = str(payload.get("protocol_version", ""))
        if version != self._expected_protocol_version:
            handshake.set_exception(
                MakaiStreamError(
                    "protocol version mismatch "
                    f"(expected {self._expected_protocol_version}, got {version})",
                    kind="transport_error",
                    code="version_mismatch",
                )
            )
            return
        handshake.set_result(None)

    def _abandon(self, error: MakaiStreamError) -> None:
        """Give up on the connection from inside the reader loop.

        The reader stops consuming stdout on these paths, so leaving the
        transport open would strand a live child and hand later callers a
        ``connected`` transport whose routes never receive a frame. ``close()``
        cancels and awaits the reader, so the reader cannot call it on itself;
        mark the transport closed synchronously and reap the child in a
        detached task that ``close()`` adopts.
        """
        self._closed = True
        process = self._process
        self._process = None
        self._reader_task = None
        self._fail_all(error)
        if process is not None:
            self._teardown_task = asyncio.create_task(
                _reap_child(process), name="makai-stdio-teardown"
            )

    def _fail_all(self, error: BaseException) -> None:
        handshake = self._handshake
        if handshake is not None and not handshake.done():
            handshake.set_exception(error)
        for route in (list(self._stream_routes.values()) + list(self._session_routes.values())
                      + list(self._request_routes.values()) + list(self._inference_routes.values())
                      + list(self._auth_flow_routes.values())):
            route._fail(error)
