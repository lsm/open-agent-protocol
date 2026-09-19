"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const REQUEST = {
    model_ref: "fixture/openai-responses@gpt-test",
    messages: [{ role: "user", content: "hello" }],
};
class FakeTransport {
    sent = [];
    streams;
    constructor(streams) {
        this.streams = streams;
    }
    send(frame) {
        this.sent.push(frame);
    }
    async nextFrameForStream(streamId, _timeoutMs) {
        const stream = this.streams[0];
        if (!stream)
            throw new Error("no frames configured");
        const frame = stream.shift();
        if (!frame)
            throw new Error("stream exhausted");
        if (stream.length === 0)
            this.streams.shift();
        return { stream_id: streamId, ...frame };
    }
}
class FakeAuth {
    outcomes;
    loginCalls = [];
    constructor(outcomes = []) {
        this.outcomes = outcomes;
    }
    async listProviders() { return []; }
    async login(providerId, handlers) {
        this.loginCalls.push({ providerId, handlers });
        const outcome = this.outcomes.shift() ?? "success";
        if (outcome === "interactive_required") {
            throw new src_1.MakaiAuthError("auth login cancelled (no onPrompt handler configured)", { kind: "cancelled" });
        }
        return { status: "success" };
    }
}
const authRequired = (provider_id = "fixture") => [
    { type: "nack", payload: { error_code: "auth_required", reason: "login required", provider_id } },
];
const success = (text = "ok") => [
    { type: "ack" },
    {
        type: "result",
        payload: {
            message: { role: "assistant", content: text },
            provider_id: "fixture",
            api: "openai-responses",
            model_id: "gpt-test",
        },
    },
];
(0, node_test_1.default)("manual policy throws typed auth_required error with provider_id", async () => {
    const auth = new FakeAuth();
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture")]), { auth });
    await strict_1.default.rejects(() => provider.complete(REQUEST), (error) => error instanceof src_1.MakaiAuthRequiredError && error.provider_id === "fixture");
    strict_1.default.equal(auth.loginCalls.length, 0);
});
(0, node_test_1.default)("auto_once policy logs in then retries original request once", async () => {
    const auth = new FakeAuth(["success"]);
    const transport = new FakeTransport([authRequired("fixture"), success("retried")]);
    const provider = (0, src_1.createMakaiProviderApi)(transport, {
        auth,
        authRetryPolicy: "auto_once",
        authHandlers: { onPrompt: () => "code" },
    });
    const response = await provider.complete(REQUEST);
    strict_1.default.equal(response.message.content, "retried");
    strict_1.default.deepEqual(auth.loginCalls.map((call) => call.providerId), ["fixture"]);
    strict_1.default.equal(transport.sent.length, 2);
});
(0, node_test_1.default)("auto_once with no handlers and required interaction fails fast as auth_required", async () => {
    const auth = new FakeAuth(["interactive_required"]);
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture")]), { auth, authRetryPolicy: "auto_once" });
    await strict_1.default.rejects(() => provider.complete(REQUEST), (error) => error instanceof src_1.MakaiAuthRequiredError && error.provider_id === "fixture");
    strict_1.default.equal(auth.loginCalls.length, 1);
});
(0, node_test_1.default)("auto_once with non-interactive auth succeeds without handlers", async () => {
    const auth = new FakeAuth(["success"]);
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture"), success("non-interactive")]), { auth, authRetryPolicy: "auto_once" });
    const response = await provider.complete(REQUEST);
    strict_1.default.equal(response.message.content, "non-interactive");
    strict_1.default.equal(auth.loginCalls.length, 1);
});
(0, node_test_1.default)("per-call policy overrides client-level default", async () => {
    const auth = new FakeAuth(["success"]);
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture"), success("per-call")]), { auth, authRetryPolicy: "manual", authHandlers: { onPrompt: () => "client" } });
    const response = await provider.complete({
        ...REQUEST,
        options: { auth_retry_policy: "auto_once" },
    });
    strict_1.default.equal(response.message.content, "per-call");
    strict_1.default.equal(auth.loginCalls.length, 1);
});
(0, node_test_1.default)("client-level auto_once is overridden by per-call manual", async () => {
    const auth = new FakeAuth(["success"]);
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture")]), { auth, authRetryPolicy: "auto_once", authHandlers: { onPrompt: () => "client" } });
    await strict_1.default.rejects(() => provider.complete({ ...REQUEST, options: { auth_retry_policy: "manual" } }), src_1.MakaiAuthRequiredError);
    strict_1.default.equal(auth.loginCalls.length, 0);
});
(0, node_test_1.default)("auto_once uses client-level default handlers, not per-call request policy", async () => {
    const auth = new FakeAuth(["success"]);
    const handler = () => "client";
    const provider = (0, src_1.createMakaiProviderApi)(new FakeTransport([authRequired("fixture"), success("handler-default")]), { auth, authRetryPolicy: "auto_once", authHandlers: { onPrompt: handler } });
    await provider.complete(REQUEST);
    strict_1.default.equal(auth.loginCalls[0]?.handlers?.onPrompt, handler);
});
