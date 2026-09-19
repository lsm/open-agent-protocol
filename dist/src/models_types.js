"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.MakaiProtocolError = void 0;
class MakaiProtocolError extends Error {
    code;
    diagnostics;
    constructor(message, code, options = {}) {
        super(message);
        this.code = code;
        this.name = "MakaiProtocolError";
        this.diagnostics = options.diagnostics;
    }
}
exports.MakaiProtocolError = MakaiProtocolError;
