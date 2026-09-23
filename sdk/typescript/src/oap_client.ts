import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createInterface, type Interface as ReadlineInterface } from "node:readline";
import { ulid } from "ulid";
import { resolveMakaiBinary } from "./binary_resolver";
import { isAbortError, raceWithAbort } from "./abort_signal";
import { AUTH_STATUSES, MakaiAuthError, type AuthFlowHandlers, type MakaiAuthApi, type MakaiAuthEvent, type ProviderAuthInfo } from "./auth_protocol";
import { MakaiAuthRequiredError, MakaiStreamError, type AgentRunRequest, type AgentRunResponse, type AgentStreamEvent, type ChatMessage, type CompletionResponse, type ContentPart, type MakaiProviderApi, type ProviderCompleteRequest, type ProviderStreamEvent, type UsageSummary } from "./execution_types";
import { MakaiProtocolError, type ListModelsRequest, type ListModelsResponse, type MakaiModelsApi, type ModelDescriptor, type ResolveModelRequest, type ResolveModelResponse } from "./models_types";
import type { CreateMakaiClientOptions, MakaiAgentModelsApi, MakaiClient } from "./execution_client";

export const OAP_PROTOCOL = "open-agent-protocol";
export const OAP_VERSION = "0.1";
export const OAP_AGENT_PROFILE = "open-agent-protocol.agent-control-core";
export const OAP_PROVIDER_PROFILE = "open-agent-protocol.model-provider-core";

export type OapEnvelope = {
  protocol: typeof OAP_PROTOCOL;
  version: typeof OAP_VERSION;
  profile: string;
  type: string;
  id: string;
  payload: Record<string, unknown>;
  in_reply_to?: string;
  session_id?: string;
  run_id?: string;
  inference_id?: string;
  sequence?: number;
  capability_revision?: string;
};

export class OapUnsupportedFeatureError extends Error {
  readonly code = "unsupported_feature";
  constructor(readonly feature: string) {
    super(`OAP endpoint does not support ${feature}`);
    this.name = "OapUnsupportedFeatureError";
  }
}

type Pending = { resolve: (frame: OapEnvelope) => void; reject: (error: Error) => void; timer: NodeJS.Timeout };

class FrameQueue {
  private frames: OapEnvelope[] = [];
  private waiting: Array<{ resolve: (frame: OapEnvelope) => void; reject: (error: Error) => void; timer: NodeJS.Timeout; signal?: AbortSignal; onAbort?: () => void }> = [];
  private closed?: Error;

  push(frame: OapEnvelope): void {
    const next = this.waiting.shift();
    if (next) { clearTimeout(next.timer); if (next.signal && next.onAbort) next.signal.removeEventListener("abort", next.onAbort); next.resolve(frame); }
    else this.frames.push(frame);
  }

  next(timeoutMs: number, signal?: AbortSignal): Promise<OapEnvelope> {
    if (signal?.aborted) return Promise.reject(abortError());
    const frame = this.frames.shift();
    if (frame) return Promise.resolve(frame);
    if (this.closed) return Promise.reject(this.closed);
    return new Promise<OapEnvelope>((resolve, reject) => {
      const waiter = { resolve, reject, timer: undefined as unknown as NodeJS.Timeout, signal, onAbort: undefined as (() => void) | undefined };
      const remove = (): void => { const index = this.waiting.indexOf(waiter); if (index >= 0) this.waiting.splice(index, 1); if (signal && waiter.onAbort) signal.removeEventListener("abort", waiter.onAbort); };
      waiter.timer = setTimeout(() => {
        remove();
        reject(new Error(`timed out waiting for OAP event after ${timeoutMs}ms`));
      }, timeoutMs);
      if (signal) {
        waiter.onAbort = () => { clearTimeout(waiter.timer); remove(); reject(abortError()); };
        signal.addEventListener("abort", waiter.onAbort, { once: true });
      }
      this.waiting.push(waiter);
    });
  }

  close(error: Error): void {
    this.closed = error;
    for (const waiter of this.waiting.splice(0)) { clearTimeout(waiter.timer); if (waiter.signal && waiter.onAbort) waiter.signal.removeEventListener("abort", waiter.onAbort); waiter.reject(error); }
  }
}

export class OapStdioTransport {
  private child?: ChildProcessWithoutNullStreams;
  private reader?: ReadlineInterface;
  private pending = new Map<string, Pending>();
  private subscriptions = new Map<string, FrameQueue>();
  private orphaned = new Map<string, OapEnvelope[]>();
  private closed?: Error;
  agentRevision?: string;

  constructor(private readonly command: string, private readonly args: string[], private readonly options: { cwd?: string; env?: NodeJS.ProcessEnv; timeoutMs: number; handshakeTimeoutMs?: number }) {}

  async connect(): Promise<void> {
    this.child = spawn(this.command, this.args, { cwd: this.options.cwd, env: this.options.env, stdio: "pipe" });
    this.child.on("error", (error) => this.fail(error));
    this.child.on("exit", (code, signal) => this.fail(new Error(`OAP process exited (code=${code}, signal=${signal})`)));
    this.child.stderr.resume();
    this.reader = createInterface({ input: this.child.stdout });
    this.reader.on("line", (line) => this.receive(line));
    try {
      const initialized = await this.request(OAP_AGENT_PROFILE, "protocol.initialize.request", {
        protocol_versions: [OAP_VERSION], profiles: [OAP_AGENT_PROFILE], participant: { id: "sdk" },
      }, {}, this.options.handshakeTimeoutMs);
      if (initialized.type !== "protocol.initialize.response" || initialized.payload.protocol_version !== OAP_VERSION || initialized.payload.profile !== OAP_AGENT_PROFILE) {
        throw new MakaiProtocolError("OAP agent initialization did not negotiate agent-control-core", "protocol_mismatch");
      }
      const capabilities = await this.request(OAP_AGENT_PROFILE, "capabilities.request", {}, {}, this.options.handshakeTimeoutMs);
      if (capabilities.type !== "capabilities.response") throw new MakaiProtocolError("OAP agent did not return capabilities", "protocol_mismatch");
      this.agentRevision = capabilities.capability_revision;
      const described = await this.request(OAP_PROVIDER_PROFILE, "provider.describe.request", {}, {}, this.options.handshakeTimeoutMs);
      if (described.type !== "provider.describe.response") throw new MakaiProtocolError("OAP provider did not return a descriptor", "protocol_mismatch");
    } catch (error) {
      await this.close();
      throw error;
    }
  }

  request(profile: string, type: string, payload: Record<string, unknown>, scope: Partial<OapEnvelope> = {}, timeoutMs = this.options.timeoutMs): Promise<OapEnvelope> {
    const id = ulid();
    return new Promise<OapEnvelope>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`timed out waiting for ${type} response after ${timeoutMs}ms`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      try { this.send(profile, type, payload, { ...scope, id }); }
      catch (error) { clearTimeout(timer); this.pending.delete(id); reject(error as Error); }
    }).then((response) => {
      if (response.type === "error.response") throw protocolError(response);
      return response;
    });
  }

  send(profile: string, type: string, payload: Record<string, unknown>, scope: Partial<OapEnvelope> = {}): string {
    if (!this.child || this.closed) throw this.closed ?? new Error("OAP transport is not connected");
    const id = scope.id ?? ulid();
    const envelope: OapEnvelope = {
      protocol: OAP_PROTOCOL, version: OAP_VERSION, profile, type, id, payload,
      ...(scope.inference_id ? { inference_id: scope.inference_id } : {}),
      ...(scope.session_id ? { session_id: scope.session_id } : {}),
      ...(scope.run_id ? { run_id: scope.run_id } : {}),
      ...(profile === OAP_AGENT_PROFILE && this.agentRevision && type !== "protocol.initialize.request" && type !== "capabilities.request" ? { capability_revision: this.agentRevision } : {}),
    };
    this.child.stdin.write(`${JSON.stringify(envelope)}\n`);
    return id;
  }

  subscribe(profile: string, kind: "session" | "inference" | "flow", id: string): FrameQueue {
    const key = `${profile}|${kind}|${id}`;
    if (this.subscriptions.has(key)) throw new Error(`OAP route already subscribed: ${key}`);
    const queue = new FrameQueue();
    this.subscriptions.set(key, queue);
    for (const frame of this.orphaned.get(key) ?? []) queue.push(frame);
    this.orphaned.delete(key);
    return queue;
  }

  unsubscribe(profile: string, kind: "session" | "inference" | "flow", id: string): void {
    this.subscriptions.delete(`${profile}|${kind}|${id}`);
  }

  private receive(line: string): void {
    let frame: OapEnvelope;
    try {
      frame = JSON.parse(line) as OapEnvelope;
      if (frame.protocol !== OAP_PROTOCOL || frame.version !== OAP_VERSION || ![OAP_AGENT_PROFILE, OAP_PROVIDER_PROFILE].includes(frame.profile) || typeof frame.type !== "string" || typeof frame.id !== "string" || !isRecord(frame.payload)) {
        throw new Error("invalid OAP envelope");
      }
    } catch { this.fail(new MakaiProtocolError("invalid OAP frame", "malformed_response")); return; }
    if (frame.in_reply_to) {
      const pending = this.pending.get(frame.in_reply_to);
      if (pending) { this.pending.delete(frame.in_reply_to); clearTimeout(pending.timer); pending.resolve(frame); return; }
    }
    const flowId = frame.profile === OAP_AGENT_PROFILE ? str(frame.payload.flow_id) : "";
    const kind = frame.inference_id ? "inference" : frame.session_id ? "session" : flowId ? "flow" : undefined;
    const routeId = frame.inference_id ?? frame.session_id ?? flowId;
    if (kind && routeId) {
      const key = `${frame.profile}|${kind}|${routeId}`;
      const queue = this.subscriptions.get(key);
      if (queue) queue.push(frame);
      else if (this.orphaned.has(key) || this.orphaned.size < 64) {
        const waiting = this.orphaned.get(key) ?? [];
        if (waiting.length < 256) waiting.push(frame);
        this.orphaned.set(key, waiting);
      }
    }
  }

  private fail(error: Error): void {
    if (this.closed) return;
    this.closed = error;
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear();
    for (const queue of this.subscriptions.values()) queue.close(error);
  }

  async close(): Promise<void> {
    const child = this.child;
    if (!child) return;
    this.child = undefined;
    this.reader?.close();
    this.fail(new Error("OAP transport closed"));
    child.stdin.end();
    if (child.exitCode === null && child.signalCode === null) {
      await new Promise<void>((resolve) => {
        const timer = setTimeout(() => { child.kill(); resolve(); }, 500);
        child.once("exit", () => { clearTimeout(timer); resolve(); });
      });
    }
  }
}

function isRecord(value: unknown): value is Record<string, unknown> { return value !== null && typeof value === "object" && !Array.isArray(value); }
function abortError(): Error { const error = new Error("operation aborted"); error.name = "AbortError"; return error; }
function str(value: unknown): string { return typeof value === "string" ? value : ""; }
function payload(frame: OapEnvelope): Record<string, unknown> { return frame.payload; }
function protocolError(frame: OapEnvelope): Error {
  const error = isRecord(frame.payload.error) ? frame.payload.error : frame.payload;
  if (error.code === "unsupported_feature") return new OapUnsupportedFeatureError(str(error.message) || frame.type);
  return new MakaiProtocolError(str(error.message) || `${frame.type} failed`, str(error.code) || undefined);
}
function unsupported(feature: string): never { throw new OapUnsupportedFeatureError(feature); }
function authFailure(error: unknown): boolean {
  const code = error instanceof MakaiStreamError || error instanceof MakaiProtocolError ? error.code : undefined;
  return code === "auth_required" || code === "credential_missing" || code === "credential_expired" || code === "credential_rejected" || code === "auth_expired";
}
function providerFromRef(modelRef: string): string | undefined {
  const slash = modelRef.indexOf("/");
  return slash > 0 ? modelRef.slice(0, slash) : undefined;
}

function toOapMessages(messages: ChatMessage[]): Array<{ role: ChatMessage["role"]; content: string | Record<string, unknown>[] }> {
  return messages.map((message) => {
    const content = typeof message.content === "string" ? message.content : message.content.map((part) => {
      switch (part.type) {
        case "text": return { type: "text", text: part.text };
        case "thinking": return { type: "reasoning", reasoning: part.thinking, ...(part.thinking_signature ? { carry: part.thinking_signature } : {}) };
        case "tool_call": return { type: "tool_call", tool_call_id: part.tool_call_id, name: part.name, arguments_json: JSON.parse(part.arguments_json), ...(part.carry ? { carry: part.carry } : {}) };
        case "tool_result": return { type: "tool_result", tool_call_id: part.tool_call_id, result: part.content, is_error: part.is_error ?? false };
        case "image": return { type: "image", image: { data: part.data, media_type: part.mime_type } };
      }
    });
    if (message.role === "tool") {
      if (!message.tool_call_id) throw new MakaiProtocolError("tool message requires tool_call_id on OAP", "invalid_request");
      if (Array.isArray(content) && content.length === 1 && content[0]?.type === "tool_result") {
        if (content[0].tool_call_id !== message.tool_call_id) throw new MakaiProtocolError("tool result id disagrees with message tool_call_id", "invalid_request");
        return { role: "tool", content };
      }
      return { role: "tool", content: [{ type: "tool_result", tool_call_id: message.tool_call_id, result: content }] };
    }
    return { role: message.role, content };
  });
}

function fromOapContent(content: unknown): string | ContentPart[] {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.flatMap((raw): ContentPart[] => {
    if (!isRecord(raw)) return [];
    if (raw.type === "text") return [{ type: "text", text: str(raw.text) }];
    if (raw.type === "reasoning") return [{ type: "thinking", thinking: str(raw.reasoning), ...(typeof raw.carry === "string" ? { thinking_signature: raw.carry } : {}) }];
    if (raw.type === "tool_call") return [{ type: "tool_call", tool_call_id: str(raw.tool_call_id), name: str(raw.name), arguments_json: JSON.stringify(raw.arguments_json ?? {}), ...(typeof raw.carry === "string" ? { carry: raw.carry } : {}) }];
    if (raw.type === "tool_result") return [{ type: "tool_result", tool_call_id: str(raw.tool_call_id), tool_name: "", content: typeof raw.result === "string" ? raw.result : JSON.stringify(raw.result ?? ""), is_error: raw.is_error === true }];
    if (raw.type === "image") {
      const value = isRecord(raw.image) ? raw.image : {};
      if (typeof value.data === "string" && typeof value.media_type === "string") return [{ type: "image", data: value.data, mime_type: value.media_type }];
      unsupported("URL image content in an OAP response");
    }
    unsupported(`OAP response content part ${str(raw.type) || "unknown"}`);
  });
}

function parseInputSchema(raw: string): unknown {
  try { return JSON.parse(raw); }
  catch { throw new MakaiProtocolError("tool parameters_schema_json is not valid JSON", "invalid_request"); }
}

function usage(value: unknown): UsageSummary | undefined {
  if (!isRecord(value)) return undefined;
  return { input: typeof value.input_tokens === "number" ? value.input_tokens : 0, output: typeof value.output_tokens === "number" ? value.output_tokens : 0 };
}

function modelParts(modelRef: string): { provider_id: string; api: string; model_id: string } {
  const match = /^([^/]+)\/([^@]+)@(.+)$/.exec(modelRef);
  if (!match) throw new TypeError("model_ref must be provider_id/wire@model_id");
  return { provider_id: match[1], api: match[2], model_id: match[3] };
}

class OapProviderApi implements MakaiProviderApi {
  constructor(private readonly transport: OapStdioTransport, private readonly timeoutMs: number, private readonly authRetryPolicy?: string, private readonly auth?: MakaiAuthApi, private readonly authHandlers?: AuthFlowHandlers) {}

  async complete(request: ProviderCompleteRequest): Promise<CompletionResponse> {
    let final: CompletionResponse | undefined;
    let text = "";
    for await (const event of this.stream(request)) {
      if (event.type === "text_delta") text += event.delta;
      if (event.type === "message_end") {
        const ref = modelParts(request.model_ref);
        final = { message: event.message ?? { role: "assistant", content: text }, usage: event.usage, stop_reason: event.stop_reason, ...ref };
      }
    }
    if (!final) throw new MakaiStreamError("inference ended without inference.completed", { kind: "transport_error" });
    return final;
  }

  async *stream(request: ProviderCompleteRequest): AsyncIterable<ProviderStreamEvent> {
    let retried = false;
    for (;;) {
      let yieldedContent = false;
      const preamble: ProviderStreamEvent[] = [];
      try {
        for await (const event of this.streamOnce(request)) {
          if (!yieldedContent && event.type === "message_start") { preamble.push(event); continue; }
          if (!yieldedContent) { yieldedContent = true; for (const earlier of preamble) yield earlier; }
          yield event;
        }
        return;
      } catch (error) {
        if (authFailure(error)) {
          const providerId = providerFromRef(request.model_ref);
          if (!yieldedContent && !retried && providerId && (request.options?.auth_retry_policy ?? this.authRetryPolicy) === "auto_once" && this.auth) {
            retried = true;
            try { await this.auth.login(providerId, this.authHandlers, { signal: request.options?.signal }); }
            catch (loginError) {
              if (request.options?.signal?.aborted) throw new MakaiStreamError("provider inference aborted", { kind: "aborted" });
              throw new MakaiAuthRequiredError(providerId, error instanceof Error ? error.message : "authentication required");
            }
            continue;
          }
          if (providerId) throw new MakaiAuthRequiredError(providerId, error instanceof Error ? error.message : "authentication required");
        }
        throw error;
      }
    }
  }

  private async *streamOnce(request: ProviderCompleteRequest): AsyncIterable<ProviderStreamEvent> {
    const signal = request.options?.signal;
    if (signal?.aborted) throw abortError();
    if (request.tools?.some((tool) => tool.execute)) unsupported("client-executed tools on direct provider inference");
    const create = await this.transport.request(OAP_PROVIDER_PROFILE, "inference.create.request", {
      model_ref: request.model_ref, messages: toOapMessages(request.messages), stream: true,
      ...(request.tools?.length ? { tools: request.tools.map((tool) => ({ name: tool.name, description: tool.description, input_schema: parseInputSchema(tool.parameters_schema_json) })) } : {}),
      ...(request.options?.max_tokens !== undefined ? { max_output_tokens: request.options.max_tokens } : {}),
      ...(request.options?.temperature !== undefined ? { temperature: request.options.temperature } : {}),
      ...(request.options?.reasoning_effort && request.options.reasoning_effort !== "off" ? { reasoning: { enabled: true, effort: request.options.reasoning_effort } } : {}),
      ...(request.options?.metadata ? { metadata: request.options.metadata } : {}),
    });
    if (create.type !== "inference.create.response") throw new MakaiProtocolError(`unexpected ${create.type}`, "malformed_response");
    if (create.payload.accepted !== true) {
      const err = isRecord(create.payload.error) ? create.payload.error : {};
      throw new MakaiStreamError(str(err.message) || "inference rejected", { kind: "provider_error", code: str(err.code) || undefined });
    }
    const inferenceId = create.inference_id;
    if (!inferenceId) throw new MakaiProtocolError("inference.create.response has no inference_id", "malformed_response");
    const queue = this.transport.subscribe(OAP_PROVIDER_PROFILE, "inference", inferenceId);
    const ref = modelParts(request.model_ref);
    const partKinds = new Map<number, string>();
    let settled = false;
    try {
      while (true) {
        if (signal?.aborted) throw abortError();
        const frame = await queue.next(this.timeoutMs, signal);
        const data = payload(frame);
        if (frame.type === "inference.started") { yield { type: "message_start", ...ref }; continue; }
        if (frame.type === "inference.part.started") {
          if (typeof data.part_index === "number") partKinds.set(data.part_index, str(data.part_kind));
          continue;
        }
        if (frame.type === "inference.part.delta") {
          const kind = partKinds.get(typeof data.part_index === "number" ? data.part_index : -1);
          if (kind === "reasoning") yield { type: "thinking_delta", delta: str(data.delta) };
          else if (kind === "text") yield { type: "text_delta", delta: str(data.delta) };
          else if (kind !== "tool_call") throw new MakaiProtocolError("inference delta has no known part kind", "malformed_response");
          continue;
        }
        if (frame.type === "inference.part.ended" && data.part_kind === "tool_call" && isRecord(data.tool_call)) {
          yield { type: "tool_call", tool_call_id: str(data.tool_call.tool_call_id), name: str(data.tool_call.name), arguments_json: JSON.stringify(data.tool_call.arguments_json ?? {}) };
          continue;
        }
        if (frame.type === "inference.completed") {
          settled = true;
          const finalMessage = isRecord(data.message) ? data.message : {};
          yield { type: "message_end", usage: usage(data.usage), stop_reason: str(data.stop_reason), message: { role: "assistant", content: fromOapContent(finalMessage.content) } };
          return;
        }
        if (frame.type === "inference.failed") {
          settled = true;
          const err = isRecord(data.error) ? data.error : {};
          throw new MakaiStreamError(str(err.message) || "inference failed", { kind: "provider_error", code: str(err.code) || undefined });
        }
      }
    } finally {
      this.transport.unsubscribe(OAP_PROVIDER_PROFILE, "inference", inferenceId);
      if (!settled) {
        try { this.transport.send(OAP_PROVIDER_PROFILE, "inference.cancel.request", { reason: "client stopped" }, { inference_id: inferenceId }); } catch {}
      }
    }
  }
}

class OapModelsApi implements MakaiModelsApi {
  constructor(private readonly transport: OapStdioTransport) {}
  async list(request: ListModelsRequest = {}): Promise<ListModelsResponse> {
    const frame = await this.transport.request(OAP_PROVIDER_PROFILE, "provider.models.list.request", request.provider_id ? { provider_id: request.provider_id } : {});
    if (frame.type !== "provider.models.list.response" || !Array.isArray(frame.payload.models)) throw new MakaiProtocolError("invalid provider.models.list.response", "malformed_response");
    const models: ModelDescriptor[] = frame.payload.models.filter(isRecord).map((raw) => ({
      model_ref: str(raw.model_ref), model_id: str(raw.model_id), display_name: str(raw.display_name) || str(raw.model_id),
      provider_id: str(raw.provider_id), api: str(raw.wire), auth_status: (str(raw.auth_status) || "unknown") as ModelDescriptor["auth_status"],
      lifecycle: (str(raw.lifecycle) || "stable") as ModelDescriptor["lifecycle"],
      capabilities: Array.isArray(raw.capabilities) ? raw.capabilities.filter((v): v is ModelDescriptor["capabilities"][number] => typeof v === "string") : [],
      source: raw.source === "fallback" || raw.source === "static_fallback" ? "static_fallback" as const : "dynamic" as const,
      ...(typeof raw.context_window === "number" ? { context_window: raw.context_window } : {}),
      ...(typeof raw.max_output_tokens === "number" ? { max_output_tokens: raw.max_output_tokens } : {}),
    })).filter((model) => (!request.api || model.api === request.api) && (!request.model_id || model.model_id === request.model_id) && (request.include_deprecated || model.lifecycle !== "deprecated") && (request.include_login_required || model.auth_status !== "login_required"));
    return { models, fetched_at_ms: Date.now(), cache_max_age_ms: 0 };
  }
  async resolve(request: ResolveModelRequest): Promise<ResolveModelResponse> {
    const listed = await this.list({ provider_id: request.provider_id, api: request.api, model_id: request.model_id, include_deprecated: true, include_login_required: true });
    const model = listed.models[0];
    if (!model) throw new MakaiProtocolError("model not found", "model_not_found");
    return { model };
  }
}

class OapAuthApi implements MakaiAuthApi {
  constructor(private readonly transport: OapStdioTransport, private readonly timeoutMs: number, private readonly defaults?: AuthFlowHandlers) {}

  async listProviders(): Promise<ProviderAuthInfo[]> {
    const frame = await this.transport.request(OAP_AGENT_PROFILE, "auth.providers.request", {});
    if (frame.type !== "auth.providers.response" || !Array.isArray(frame.payload.providers)) {
      throw new MakaiProtocolError("invalid auth.providers.response", "malformed_response");
    }
    return frame.payload.providers.map((raw) => {
      if (!isRecord(raw) || !str(raw.id) || !str(raw.name) || !AUTH_STATUSES.some((status) => status === raw.auth_status)) {
        throw new MakaiProtocolError("invalid auth provider descriptor", "malformed_response");
      }
      return {
        id: str(raw.id), name: str(raw.name), auth_status: raw.auth_status as ProviderAuthInfo["auth_status"],
        ...(typeof raw.last_error === "string" ? { last_error: raw.last_error } : {}),
      };
    });
  }

  async login(providerId: string, handlers?: AuthFlowHandlers, options?: { signal?: AbortSignal }): Promise<{ status: "success" }> {
    const signal = options?.signal;
    if (signal?.aborted) throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
    const started = await this.transport.request(OAP_AGENT_PROFILE, "auth.login.start.request", { provider_id: providerId });
    if (started.type !== "auth.login.start.response" || !str(started.payload.flow_id)) {
      throw new MakaiProtocolError("invalid auth.login.start.response", "malformed_response");
    }
    const flowId = str(started.payload.flow_id);
    const queue = this.transport.subscribe(OAP_AGENT_PROFILE, "flow", flowId);
    const effective = handlers ?? this.defaults;
    const notify = (event: MakaiAuthEvent): void => {
      try { effective?.onEvent?.(event); }
      catch (error) { throw new MakaiAuthError(error instanceof Error ? error.message : String(error), { kind: "unknown" }); }
    };
    let settled = false;
    let sequence = 0;
    try {
      while (true) {
        let frame: OapEnvelope;
        try { frame = await queue.next(this.timeoutMs, signal); }
        catch (error) {
          if (isAbortError(error)) throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
          throw error;
        }
        if (!Number.isInteger(frame.sequence) || frame.sequence !== sequence + 1) {
          throw new MakaiProtocolError("auth flow sequence is not contiguous", "malformed_response");
        }
        sequence = frame.sequence;
        const data = frame.payload;
        if (str(data.flow_id) !== flowId || str(data.provider_id) !== providerId) {
          throw new MakaiProtocolError("auth flow identity mismatch", "malformed_response");
        }
        if (frame.type === "auth.login.event") {
          let event: MakaiAuthEvent;
          if (data.kind === "url" && str(data.url)) {
            event = { type: "auth_url", flow_id: flowId, provider_id: providerId, url: str(data.url), ...(typeof data.instructions === "string" ? { instructions: data.instructions } : {}) };
          } else if (data.kind === "prompt") {
            throw new MakaiAuthError("manual login input cannot be sent over OAP", { kind: "provider_error", code: "auth_input_unavailable" });
          } else if (data.kind === "progress" && str(data.message)) {
            event = { type: "progress", flow_id: flowId, provider_id: providerId, message: str(data.message) };
          } else {
            throw new MakaiProtocolError("invalid auth.login.event", "malformed_response");
          }
          notify(event);
          continue;
        }
        if (frame.type !== "auth.login.completed") throw new MakaiProtocolError(`unexpected auth flow frame: ${frame.type}`, "malformed_response");
        settled = true;
        if (data.status === "success") {
          notify({ type: "success", flow_id: flowId, provider_id: providerId });
          return { status: "success" };
        }
        const err = isRecord(data.error) ? data.error : {};
        const message = str(err.message) || (data.status === "cancelled" ? "auth login cancelled" : "auth login failed");
        notify({ type: "error", flow_id: flowId, provider_id: providerId, message, ...(str(err.code) ? { code: str(err.code) } : {}) });
        if (data.status === "cancelled") throw new MakaiAuthError(message, { kind: "cancelled", code: str(err.code) || undefined });
        if (data.status === "failed") throw new MakaiAuthError(message, { kind: "provider_error", code: str(err.code) || undefined });
        throw new MakaiProtocolError("invalid auth.login.completed status", "malformed_response");
      }
    } finally {
      this.transport.unsubscribe(OAP_AGENT_PROFILE, "flow", flowId);
      if (!settled) {
        try { this.transport.send(OAP_AGENT_PROFILE, "auth.login.cancel.request", { flow_id: flowId }); } catch {}
      }
    }
  }
}

class OapAgentApi {
  readonly models: MakaiModelsApi;
  private readonly selectedModels = new Map<string, string>();
  constructor(private readonly transport: OapStdioTransport, private readonly timeoutMs: number, models: MakaiModelsApi, private readonly authRetryPolicy?: string, private readonly auth?: MakaiAuthApi, private readonly authHandlers?: AuthFlowHandlers) { this.models = models; }

  async run(request: AgentRunRequest): Promise<AgentRunResponse> {
    let text = "";
    let end: Extract<AgentStreamEvent, { type: "agent_end" }> | undefined;
    for await (const event of this.stream(request)) {
      if (event.type === "text_delta") text += event.delta;
      if (event.type === "agent_end") end = event;
    }
    if (!end) throw new MakaiStreamError("agent run ended without run.completed", { kind: "transport_error" });
    return { message: end.message ?? { role: "assistant", content: text }, usage: end.usage, stop_reason: end.stop_reason, ...modelParts(request.model_ref || end.model_id || "") };
  }

  async runSelected(sessionId: string, messages: ChatMessage[]): Promise<AgentRunResponse> {
    return this.run({ model_ref: "", messages, options: { session_id: sessionId } });
  }

  streamSelected(sessionId: string, messages: ChatMessage[]): AsyncIterable<AgentStreamEvent> {
    return this.stream({ model_ref: "", messages, options: { session_id: sessionId } });
  }

  async *stream(request: AgentRunRequest): AsyncIterable<AgentStreamEvent> {
    let retried = false;
    for (;;) {
      let yieldedContent = false;
      const preamble: AgentStreamEvent[] = [];
      try {
        for await (const event of this.streamOnce(request)) {
          if (!yieldedContent && (event.type === "agent_start" || event.type === "message_start" || event.type === "turn_start")) { preamble.push(event); continue; }
          if (!yieldedContent) { yieldedContent = true; for (const earlier of preamble) yield earlier; }
          yield event;
        }
        return;
      } catch (error) {
        if (authFailure(error)) {
          const modelRef = request.model_ref || this.selectedModels.get(request.options?.session_id ?? "") || "";
          const providerId = providerFromRef(modelRef);
          if (!yieldedContent && !retried && providerId && (request.options?.auth_retry_policy ?? this.authRetryPolicy) === "auto_once" && this.auth) {
            retried = true;
            try { await this.auth.login(providerId, this.authHandlers, { signal: request.options?.signal }); }
            catch {
              if (request.options?.signal?.aborted) throw new MakaiStreamError("agent run aborted", { kind: "aborted" });
              throw new MakaiAuthRequiredError(providerId, error instanceof Error ? error.message : "authentication required");
            }
            continue;
          }
          if (providerId) throw new MakaiAuthRequiredError(providerId, error instanceof Error ? error.message : "authentication required");
        }
        throw error;
      }
    }
  }

  private async *streamOnce(request: AgentRunRequest): AsyncIterable<AgentStreamEvent> {
    if (request.tools?.length) unsupported("client-executed tools (+tools)");
    if (request.options?.temperature !== undefined || request.options?.max_tokens !== undefined || request.options?.reasoning_effort) unsupported("agent sampling controls");
    const sessionId = request.options?.session_id ?? ulid();
    const opened = await this.transport.request(OAP_AGENT_PROFILE, "session.open.request", { session_id: sessionId }, { session_id: sessionId });
    if (opened.type !== "session.open.response") throw new MakaiProtocolError(`unexpected ${opened.type}`, "malformed_response");
    const queue = this.transport.subscribe(OAP_AGENT_PROFILE, "session", sessionId);
    let runId: string | undefined;
    let settled = false;
    try {
      const submitted = await this.transport.request(OAP_AGENT_PROFILE, "session.message.submit.request", {
        session_id: sessionId, messages: toOapMessages(request.messages),
        ...(request.model_ref ? { model_id: request.model_ref } : {}), delivery: "auto",
      }, { session_id: sessionId });
      if (submitted.type !== "session.message.submit.response" || submitted.payload.accepted !== true) throw new MakaiProtocolError(`unexpected ${submitted.type}`, "malformed_response");
      runId = str(submitted.payload.run_id);
      while (true) {
        if (request.options?.signal?.aborted) {
          throw abortError();
        }
        const frame = await queue.next(this.timeoutMs, request.options?.signal);
        if (runId && frame.run_id && frame.run_id !== runId) continue;
        const data = payload(frame);
        if (frame.type === "run.started") { yield { type: "agent_start", session_id: sessionId }; continue; }
        if (frame.type === "content.delta" && isRecord(data.part)) {
          if (data.part.type === "text") yield { type: "text_delta", delta: str(data.part.text) };
          if (data.part.type === "reasoning") yield { type: "thinking_delta", delta: str(data.part.reasoning) };
          continue;
        }
        if (frame.type === "run.completed") {
          settled = true;
          const finalMessage = isRecord(data.final_response) ? data.final_response : {};
          yield { type: "agent_end", stop_reason: str(data.stop_reason), usage: usage(data.usage), model_id: str(data.model_id), message: { role: "assistant", content: fromOapContent(finalMessage.content) } };
          return;
        }
        if (frame.type === "run.failed") {
          settled = true;
          const err = isRecord(data.error) ? data.error : {};
          throw new MakaiStreamError(str(err.message) || "agent run failed", { kind: "provider_error", code: str(err.code) || undefined });
        }
        if (frame.type === "run.cancelled") { settled = true; throw new MakaiStreamError("agent run cancelled", { kind: "aborted" }); }
      }
    } finally {
      this.transport.unsubscribe(OAP_AGENT_PROFILE, "session", sessionId);
      if (runId && !settled) {
        try { this.transport.send(OAP_AGENT_PROFILE, "run.cancel.request", { session_id: sessionId, run_id: runId }, { session_id: sessionId, run_id: runId }); } catch {}
      }
    }
  }

  async switchModel(sessionId: string, modelId: string): Promise<void> {
    const response = await this.transport.request(OAP_AGENT_PROFILE, "session.model.switch.request", { session_id: sessionId, model_id: modelId }, { session_id: sessionId });
    if (response.type !== "session.model.switch.response") throw new MakaiProtocolError(`unexpected ${response.type}`, "malformed_response");
    this.selectedModels.set(sessionId, modelId);
  }
}

export async function createOapClient(options: CreateMakaiClientOptions = {}): Promise<MakaiClient> {
  const command = options.command ?? await resolveMakaiBinary(options.resolver ?? {});
  const transport = new OapStdioTransport(command, options.args ?? ["serve", "agent,provider", "--stdio"], {
    cwd: options.cwd, env: options.env, timeoutMs: options.responseTimeoutMs ?? options.frameTimeoutMs ?? 30_000,
    handshakeTimeoutMs: options.handshakeTimeoutMs ?? 10_000,
  });
  await transport.connect();
  const models = new OapModelsApi(transport);
  const auth = new OapAuthApi(transport, options.frameTimeoutMs ?? 30_000, options.auth?.handlers);
  return {
    auth, models,
    provider: new OapProviderApi(transport, options.responseTimeoutMs ?? 30_000, options.auth?.auth_retry_policy, auth, options.auth?.handlers),
    agent: new OapAgentApi(transport, options.responseTimeoutMs ?? 30_000, models, options.auth?.auth_retry_policy, auth, options.auth?.handlers) as MakaiAgentModelsApi,
    close: () => transport.close(),
  };
}
