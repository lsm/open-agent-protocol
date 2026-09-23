"""OAP Python SDK with source-compatible Makai class names.

Starts ``oapx serve agent,provider --stdio`` by default and exposes four
namespaces over profiled OAP 0.1 newline-delimited JSON:

``client.auth``
    List auth providers and run interactive logins. Token material stays in
    the runtime and is never returned to callers.
``client.models``
    Discover models. ``model_ref`` values are opaque handles -- pass them
    through unchanged.
``client.provider``
    Direct provider completions (``complete``) and streaming (``stream``).
``client.agent``
    Agent sessions and runs. Client-executed tools are rejected explicitly
    until the OAP endpoint offers ``+control-tools``.

Quick start::

    import asyncio
    import oap_sdk

    async def main() -> None:
        async with oap_sdk.connect() as client:
            model = await client.models.resolve(
                provider_id="anthropic",
                api="anthropic-messages",
                model_id="claude-sonnet-4-5",
            )
            response = await client.provider.complete(
                model_ref=model.model_ref,
                messages=[{"role": "user", "content": "Write a haiku about streams."}],
                options=oap_sdk.RunOptions(max_tokens=128),
            )
            print(response.text)

    asyncio.run(main())
"""

from __future__ import annotations

from .auth import AuthApi
from .binary import BinaryResolverOptions, resolve_makai_binary
from .client import AuthOptions, MakaiClient, connect
from .errors import (
    MakaiAuthError,
    MakaiAuthErrorKind,
    MakaiAuthRequiredError,
    MakaiError,
    MakaiProtocolError,
    MakaiStreamError,
    MakaiStreamErrorKind,
)
from .execution import AgentApi, ProviderApi
from .models import ModelsApi
from .sync import SyncMakaiClient, connect_sync
from .transport import Frame, FrameRoute, StdioTransport
from .types import (
    AgentEnd,
    AgentStart,
    AgentStreamEvent,
    AssistantMessage,
    AuthErrorEvent,
    AuthEvent,
    AuthEventHandler,
    AuthFlowHandlers,
    AuthProgressEvent,
    AuthPromptEvent,
    AuthPromptHandler,
    AuthRetryPolicy,
    AuthStatus,
    AuthSuccessEvent,
    AuthUrlEvent,
    ChatMessage,
    CompletionResponse,
    Content,
    ContentPart,
    ImageContentPart,
    UrlImageContentPart,
    ListModelsResponse,
    MessageEnd,
    MessageStart,
    ModelCapability,
    ModelDescriptor,
    ModelLifecycle,
    ModelSource,
    ProviderAuthInfo,
    ProviderStreamEvent,
    ReasoningEffort,
    Role,
    RunOptions,
    StreamError,
    TextContentPart,
    TextDelta,
    ThinkingContentPart,
    ThinkingDelta,
    ToolCall,
    ToolCallContentPart,
    ToolContext,
    ToolDefinition,
    ToolExecutionEnd,
    ToolExecutionStart,
    ToolExecutor,
    ToolResult,
    ToolResultContentPart,
    TurnEnd,
    TurnStart,
    Usage,
)

__version__ = "0.1.0"

__all__ = [
    "__version__",
    # Entry points
    "connect",
    "connect_sync",
    "MakaiClient",
    "SyncMakaiClient",
    "AuthOptions",
    # Namespaces
    "AuthApi",
    "ModelsApi",
    "ProviderApi",
    "AgentApi",
    # Transport
    "StdioTransport",
    "FrameRoute",
    "Frame",
    "BinaryResolverOptions",
    "resolve_makai_binary",
    # Errors
    "MakaiError",
    "MakaiStreamError",
    "MakaiStreamErrorKind",
    "MakaiAuthRequiredError",
    "MakaiProtocolError",
    "MakaiAuthError",
    "MakaiAuthErrorKind",
    # Requests
    "ChatMessage",
    "Role",
    "Content",
    "ContentPart",
    "TextContentPart",
    "ThinkingContentPart",
    "ImageContentPart",
    "UrlImageContentPart",
    "ToolCallContentPart",
    "ToolResultContentPart",
    "ToolDefinition",
    "ToolExecutor",
    "ToolContext",
    "ToolResult",
    "RunOptions",
    "AuthRetryPolicy",
    "ReasoningEffort",
    # Responses
    "CompletionResponse",
    "AssistantMessage",
    "Usage",
    # Provider events
    "ProviderStreamEvent",
    "MessageStart",
    "TextDelta",
    "ThinkingDelta",
    "ToolCall",
    "MessageEnd",
    "StreamError",
    # Agent events
    "AgentStreamEvent",
    "AgentStart",
    "AgentEnd",
    "TurnStart",
    "TurnEnd",
    "ToolExecutionStart",
    "ToolExecutionEnd",
    # Models
    "ModelDescriptor",
    "ListModelsResponse",
    "ModelCapability",
    "ModelLifecycle",
    "ModelSource",
    "AuthStatus",
    # Auth
    "ProviderAuthInfo",
    "AuthFlowHandlers",
    "AuthEventHandler",
    "AuthPromptHandler",
    "AuthEvent",
    "AuthUrlEvent",
    "AuthPromptEvent",
    "AuthProgressEvent",
    "AuthSuccessEvent",
    "AuthErrorEvent",
]
