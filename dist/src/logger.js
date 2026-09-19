"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.getNoopLogger = getNoopLogger;
exports.isNoopLogger = isNoopLogger;
const noopLogger = {
    debug() { },
    info() { },
    warn() { },
    error() { },
};
function getNoopLogger() {
    return noopLogger;
}
function isNoopLogger(logger) {
    return logger === noopLogger;
}
