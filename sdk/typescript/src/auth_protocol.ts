import type { TimeoutDiagnostics } from "./timeout_diagnostics";

export type ProviderId = string;

export const AUTH_STATUSES = [
  "authenticated",
  "login_required",
  "expired",
  "refreshing",
  "login_in_progress",
  "failed",
  "unknown",
] as const;

export type AuthStatus = (typeof AUTH_STATUSES)[number];

const AUTH_KINDS = ["api_key", "oauth", "none"] as const;

export type AuthKind = (typeof AUTH_KINDS)[number];

export { AUTH_KINDS };

export interface ProviderAuthInfo {
  id: ProviderId;
  name: string;
  auth_kinds: AuthKind[];
  auth_status: AuthStatus;
  last_error?: string;
  override_host?: string;
}

export type MakaiAuthEvent =
  | {
      type: "auth_url";
      flow_id: string;
      provider_id: ProviderId;
      url: string;
      instructions?: string;
    }
  | {
      type: "prompt";
      flow_id: string;
      prompt_id: string;
      provider_id: ProviderId;
      message: string;
      allow_empty: boolean;
    }
  | {
      type: "progress";
      flow_id: string;
      provider_id: ProviderId;
      message: string;
    }
  | {
      type: "success";
      flow_id: string;
      provider_id: ProviderId;
    }
  | {
      type: "error";
      flow_id: string;
      provider_id: ProviderId;
      code?: string;
      message: string;
    };

export interface AuthFlowHandlers {
  onEvent?: (event: MakaiAuthEvent) => void;
}

export type MakaiAuthErrorKind =
  | "provider_error"
  | "cancelled"
  | "transport_error"
  | "unknown";

export class MakaiAuthError extends Error {
  public readonly kind: MakaiAuthErrorKind;
  public readonly code?: string;
  public readonly diagnostics?: TimeoutDiagnostics;

  constructor(
    message: string,
    options: { kind?: MakaiAuthErrorKind; code?: string; diagnostics?: TimeoutDiagnostics } = {},
  ) {
    super(message);
    this.name = "MakaiAuthError";
    this.kind = options.kind ?? "unknown";
    this.code = options.code;
    this.diagnostics = options.diagnostics;
  }
}

export interface MakaiAuthApi {
  listProviders(): Promise<ProviderAuthInfo[]>;
  login(
    providerId: ProviderId,
    handlers?: AuthFlowHandlers,
    options?: { signal?: AbortSignal },
  ): Promise<{ status: "success" }>;
}

const AUTH_EVENT_VARIANTS = [
  "auth_url",
  "prompt",
  "progress",
  "success",
  "error",
] as const;
type AuthEventVariant = (typeof AUTH_EVENT_VARIANTS)[number];

export function flattenAuthEvent(payload: Record<string, unknown>): MakaiAuthEvent {
  for (const variant of AUTH_EVENT_VARIANTS) {
    const value = payload[variant];
    if (value && typeof value === "object" && !Array.isArray(value)) {
      return normalizeAuthEvent(variant, value as Record<string, unknown>);
    }
  }
  throw new MakaiAuthError(
    `unknown auth_event variant: ${JSON.stringify(payload)}`,
    { kind: "unknown" },
  );
}

function normalizeAuthEvent(
  variant: AuthEventVariant,
  data: Record<string, unknown>,
): MakaiAuthEvent {
  const flow_id = stringField(data, "flow_id");
  const provider_id = stringField(data, "provider_id");
  switch (variant) {
    case "auth_url": {
      const event: Extract<MakaiAuthEvent, { type: "auth_url" }> = {
        type: "auth_url",
        flow_id,
        provider_id,
        url: stringField(data, "url"),
      };
      const instructions = optionalStringField(data, "instructions");
      if (instructions !== undefined) event.instructions = instructions;
      return event;
    }
    case "prompt":
      return {
        type: "prompt",
        flow_id,
        prompt_id: stringField(data, "prompt_id"),
        provider_id,
        message: stringField(data, "message"),
        allow_empty:
          typeof data["allow_empty"] === "boolean" ? (data["allow_empty"] as boolean) : false,
      };
    case "progress":
      return {
        type: "progress",
        flow_id,
        provider_id,
        message: stringField(data, "message"),
      };
    case "success":
      return {
        type: "success",
        flow_id,
        provider_id,
      };
    case "error": {
      const event: Extract<MakaiAuthEvent, { type: "error" }> = {
        type: "error",
        flow_id,
        provider_id,
        message: stringField(data, "message"),
      };
      const code = optionalStringField(data, "code");
      if (code !== undefined) event.code = code;
      return event;
    }
  }
}

function stringField(data: Record<string, unknown>, key: string): string {
  const value = data[key];
  if (typeof value !== "string") {
    throw new MakaiAuthError(`auth_event field "${key}" missing or not a string`, {
      kind: "transport_error",
    });
  }
  return value;
}

function optionalStringField(
  data: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = data[key];
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "string") return undefined;
  return value.length > 0 ? value : undefined;
}
