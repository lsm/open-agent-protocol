export type ModelRefParseErrorCode = "missing_provider_id" | "missing_api" | "missing_model_id" | "missing_separators" | "ambiguous_separators" | "invalid_provider_id" | "invalid_api" | "invalid_percent_escape" | "invalid_model_id_encoding" | "invalid_utf8_model_id";
export declare class ModelRefParseError extends Error {
    readonly code: ModelRefParseErrorCode;
    constructor(message: string, code: ModelRefParseErrorCode);
}
export type ParsedModelRef = {
    providerId: string;
    api: string;
    modelId: string;
};
export declare function parseModelRef(modelRef: string): ParsedModelRef;
