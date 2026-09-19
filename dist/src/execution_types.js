"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.MakaiAuthRequiredError = exports.MakaiStreamError = void 0;
class MakaiStreamError extends Error {
    kind;
    code;
    provider_id;
    diagnostics;
    constructor(message, options = {}) {
        super(message);
        this.name = "MakaiStreamError";
        this.kind = options.kind ?? "unknown";
        this.code = options.code;
        this.provider_id = options.provider_id;
        this.diagnostics = options.diagnostics;
    }
}
exports.MakaiStreamError = MakaiStreamError;
class MakaiAuthRequiredError extends MakaiStreamError {
    code = "auth_required";
    provider_id;
    constructor(providerId, message = `authentication required for provider ${providerId}`) {
        super(message, { kind: "provider_error", code: "auth_required", provider_id: providerId });
        this.name = "MakaiAuthRequiredError";
        this.provider_id = providerId;
    }
}
exports.MakaiAuthRequiredError = MakaiAuthRequiredError;
