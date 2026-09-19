"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_test_1 = __importDefault(require("node:test"));
const model_ref_1 = require("../src/diagnostics/model_ref");
(0, node_test_1.default)("parseModelRef parses canonical refs with reserved chars and UTF-8 model IDs", () => {
    const reserved = (0, model_ref_1.parseModelRef)("anthropic/anthropic-messages@claude%3Asonnet%2F4%40latest%3Fx%3D1%26y%3D2");
    strict_1.default.equal(reserved.providerId, "anthropic");
    strict_1.default.equal(reserved.api, "anthropic-messages");
    strict_1.default.equal(reserved.modelId, "claude:sonnet/4@latest?x=1&y=2");
    const utf8 = (0, model_ref_1.parseModelRef)("openai/openai-responses@%E6%A8%A1%E5%9E%8B%3A%C3%9F%F0%9F%9A%80");
    strict_1.default.equal(utf8.providerId, "openai");
    strict_1.default.equal(utf8.api, "openai-responses");
    strict_1.default.equal(utf8.modelId, "模型:ß🚀");
});
(0, node_test_1.default)("parseModelRef rejects malformed refs with Zig-matching error categories", () => {
    expectParseError("/api@model", "missing_provider_id");
    expectParseError("provider/@model", "missing_api");
    expectParseError("provider/api@", "missing_model_id");
    expectParseError("provider-api-model", "missing_separators");
    expectParseError("provider/api@bad%2", "invalid_percent_escape");
    expectParseError("provider/api@bad%GG", "invalid_percent_escape");
    expectParseError("prov@ider/api@model", "ambiguous_separators");
    expectParseError("provider/api/v1@model", "ambiguous_separators");
    expectParseError("provider/api@bad/model", "ambiguous_separators");
    expectParseError("provider/api@bad@model", "ambiguous_separators");
    expectParseError("pro%vider/api@model", "invalid_provider_id");
    expectParseError("provider/ap%i@model", "invalid_api");
    expectParseError("provider/api@bad name", "invalid_model_id_encoding");
});
function expectParseError(modelRef, expectedCode) {
    strict_1.default.throws(() => (0, model_ref_1.parseModelRef)(modelRef), (error) => error instanceof model_ref_1.ModelRefParseError && error.code === expectedCode);
}
