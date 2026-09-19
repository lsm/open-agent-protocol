"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.checkAbort = checkAbort;
exports.raceWithAbort = raceWithAbort;
exports.isAbortError = isAbortError;
function checkAbort(signal, context = "operation aborted") {
    if (signal?.aborted) {
        const error = new Error(context);
        error.name = "AbortError";
        throw error;
    }
}
function raceWithAbort(promise, signal, context = "operation aborted") {
    if (!signal)
        return promise;
    if (signal.aborted) {
        const error = new Error(context);
        error.name = "AbortError";
        promise.catch(() => { });
        return Promise.reject(error);
    }
    return new Promise((resolve, reject) => {
        let settled = false;
        const onAbort = () => {
            queueMicrotask(() => {
                if (settled)
                    return;
                settled = true;
                const error = new Error(context);
                error.name = "AbortError";
                reject(error);
            });
        };
        signal.addEventListener("abort", onAbort, { once: true });
        promise.then((result) => {
            if (settled)
                return;
            settled = true;
            signal.removeEventListener("abort", onAbort);
            resolve(result);
        }, (error) => {
            if (settled)
                return;
            settled = true;
            signal.removeEventListener("abort", onAbort);
            reject(error);
        });
    });
}
function isAbortError(error) {
    return error instanceof Error && error.name === "AbortError";
}
