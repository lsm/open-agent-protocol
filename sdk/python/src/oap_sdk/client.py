"""The top-level client: one transport, four namespaces.

``MakaiClient`` owns a single ``oapx serve agent,provider --stdio`` child process and exposes
``auth``, ``models``, ``provider``, and ``agent`` over it. The protocols
multiplex, so concurrent calls on one client are fine; ordering is guaranteed
only within a stream or session.
"""

from __future__ import annotations

from dataclasses import dataclass
from types import TracebackType
from typing import Any, Generator, Mapping, Optional, Sequence, Type

from .auth import AuthApi
from .binary import BinaryResolverOptions
from ._oap import OAPAgentApi, OAPModelsApi, OAPProviderApi
from .execution import AgentApi, ProviderApi
from .models import ModelsApi
from .transport import DEFAULT_HANDSHAKE_TIMEOUT_S, StdioTransport
from .types import AuthFlowHandlers, AuthRetryPolicy

__all__ = ["MakaiClient", "AuthOptions", "connect"]


@dataclass(frozen=True)
class AuthOptions:
    """Client-level auth defaults.

    ``auth_retry_policy="auto_once"`` lets ``provider`` and ``agent`` calls run
    one login automatically when they hit ``auth_required``. Interactive
    providers need ``handlers`` for that to succeed; without them the call
    fails fast with :class:`~oap_sdk.errors.MakaiAuthRequiredError` rather than
    hanging (spec §3.7).
    """

    auth_retry_policy: Optional[AuthRetryPolicy] = None
    handlers: Optional[AuthFlowHandlers] = None


class MakaiClient:
    """A connected Makai runtime.

    Prefer :func:`connect`, or use this class as an async context manager::

        async with oap_sdk.connect() as client:
            models = await client.models.list()

    Always :meth:`close` a client you created without a ``with`` block --
    otherwise the child process outlives your program's interest in it.
    """

    def __init__(
        self,
        transport: StdioTransport,
        *,
        auth: Optional[AuthOptions] = None,
        response_timeout: Optional[float] = None,
        frame_timeout: Optional[float] = None,
    ) -> None:
        self._transport = transport
        auth_options = auth or AuthOptions()
        models_timeout = response_timeout if response_timeout is not None else 5.0
        execution_timeout = response_timeout if response_timeout is not None else 30.0
        auth_timeout = frame_timeout if frame_timeout is not None else 30.0

        self.auth = AuthApi(
            transport, handlers=auth_options.handlers, frame_timeout=auth_timeout
        )

        self.models: ModelsApi | OAPModelsApi
        self.provider: ProviderApi | OAPProviderApi
        self.agent: AgentApi | OAPAgentApi
        if transport.legacy_wire:
            self.models = ModelsApi(transport, response_timeout=models_timeout)
            self.provider = ProviderApi(
                transport, response_timeout=execution_timeout,
                auth_retry_policy=auth_options.auth_retry_policy, auth=self.auth)
            self.agent = AgentApi(
                transport, response_timeout=execution_timeout,
                auth_retry_policy=auth_options.auth_retry_policy, auth=self.auth,
                models=ModelsApi(transport, response_timeout=models_timeout))
        else:
            self.models = OAPModelsApi(transport, response_timeout=models_timeout)
            self.provider = OAPProviderApi(
                transport, response_timeout=execution_timeout,
                auth_retry_policy=auth_options.auth_retry_policy, auth=self.auth)
            self.agent = OAPAgentApi(
                transport, response_timeout=execution_timeout,
                auth_retry_policy=auth_options.auth_retry_policy, auth=self.auth,
                models=OAPModelsApi(transport, response_timeout=models_timeout))

    @property
    def transport(self) -> StdioTransport:
        """The underlying stdio transport. Useful for diagnostics."""
        return self._transport

    async def close(self) -> None:
        """Terminate the runtime process. Idempotent."""
        await self._transport.close()

    async def __aenter__(self) -> "MakaiClient":
        return self

    async def __aexit__(
        self,
        exc_type: Optional[Type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        await self.close()


class _ClientConnector:
    """Awaitable + async-context-manager returned by :func:`connect`."""

    def __init__(
        self,
        *,
        command: Optional[str],
        args: Optional[Sequence[str]],
        cwd: Optional[str],
        env: Optional[Mapping[str, str]],
        resolver: Optional[BinaryResolverOptions],
        auth: Optional[AuthOptions],
        response_timeout: Optional[float],
        frame_timeout: Optional[float],
        handshake_timeout: float,
        legacy_wire: Optional[bool],
    ) -> None:
        self._command = command
        self._args = args
        self._cwd = cwd
        self._env = env
        self._resolver = resolver
        self._auth = auth
        self._response_timeout = response_timeout
        self._frame_timeout = frame_timeout
        self._handshake_timeout = handshake_timeout
        self._legacy_wire = legacy_wire
        self._client: Optional[MakaiClient] = None

    async def _open(self) -> MakaiClient:
        transport = StdioTransport(
            command=self._command,
            args=self._args,
            cwd=self._cwd,
            env=self._env,
            resolver=self._resolver,
            handshake_timeout=self._handshake_timeout,
            legacy_wire=self._legacy_wire,
        )
        await transport.connect()
        try:
            client = MakaiClient(
                transport,
                auth=self._auth,
                response_timeout=self._response_timeout,
                frame_timeout=self._frame_timeout,
            )
        except BaseException:
            await transport.close()
            raise
        self._client = client
        return client

    def __await__(self) -> Generator[Any, None, MakaiClient]:
        return self._open().__await__()

    async def __aenter__(self) -> MakaiClient:
        return await self._open()

    async def __aexit__(
        self,
        exc_type: Optional[Type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        if self._client is not None:
            await self._client.close()


def connect(
    *,
    command: Optional[str] = None,
    args: Optional[Sequence[str]] = None,
    cwd: Optional[str] = None,
    env: Optional[Mapping[str, str]] = None,
    resolver: Optional[BinaryResolverOptions] = None,
    auth: Optional[AuthOptions] = None,
    response_timeout: Optional[float] = None,
    frame_timeout: Optional[float] = None,
    handshake_timeout: float = DEFAULT_HANDSHAKE_TIMEOUT_S,
    legacy_wire: Optional[bool] = None,
) -> _ClientConnector:
    """Start a combined OAP agent/provider stdio runtime and return a client.

    Usable both ways::

        client = await oap_sdk.connect()      # remember to close() it
        async with oap_sdk.connect() as c:    # closed for you
            ...

    Args:
        command: Explicit binary to run. Defaults to
            :func:`~oap_sdk.binary.resolve_makai_binary`.
        args: Process arguments. Defaults to
            ``["serve", "agent,provider", "--stdio"]``.
        legacy_wire: Explicitly use the pre-OAP Makai V1 wire. Defaults to
            false unless ``OAP_SDK_LEGACY_WIRE=1`` is set.
        cwd: Working directory for the child process.
        env: Environment for the child process. Defaults to the parent's.
        resolver: Binary resolution options.
        auth: Client-level auth defaults.
        response_timeout: Seconds to wait for a provider/agent frame
            (default 30) and a models frame (default 5).
        frame_timeout: Seconds to wait for an auth frame (default 30).
        handshake_timeout: Seconds to wait for OAP initialize or legacy ready.
    """
    return _ClientConnector(
        command=command,
        args=args,
        cwd=cwd,
        env=env,
        resolver=resolver,
        auth=auth,
        response_timeout=response_timeout,
        frame_timeout=frame_timeout,
        handshake_timeout=handshake_timeout,
        legacy_wire=legacy_wire,
    )
