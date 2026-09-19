import { type MakaiLogger } from "./logger";
type BinaryResolverBaseOptions = {
    cacheDir?: string;
    logger?: MakaiLogger;
};
type BinaryPathResolverOptions = BinaryResolverBaseOptions & {
    type?: "path";
    binaryPath: string;
    binaryUrl?: undefined;
    checksumSha256?: string;
};
type BinaryUrlResolverOptions = BinaryResolverBaseOptions & {
    type?: "url";
    binaryPath?: undefined;
    binaryUrl: string;
    checksumSha256: string;
};
type BinaryAutoResolverOptions = BinaryResolverBaseOptions & {
    type?: "auto";
    binaryPath?: undefined;
    binaryUrl?: undefined;
    checksumSha256?: string;
};
export type BinaryResolverOptions = BinaryPathResolverOptions | BinaryUrlResolverOptions | BinaryAutoResolverOptions;
export declare function resolveMakaiBinary(options?: BinaryResolverOptions): Promise<string>;
export {};
