package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	EnvBinaryPath = "OAP_SDK_BINARY_PATH"

	EnvBinaryURL = "OAP_SDK_BINARY_URL"

	EnvBinarySHA256 = "OAP_SDK_BINARY_SHA256"
)

func ResolveBinary(ctx context.Context, opts *Options) (string, error) {
	if opts == nil {
		opts = &Options{}
	}
	logger := opts.logger()

	if explicit := firstNonEmpty(os.Getenv(EnvBinaryPath), opts.BinaryPath); explicit != "" {
		resolved, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("oap sdk: cannot resolve binary path %q: %w", explicit, err)
		}
		if err := checkExecutable(resolved); err != nil {
			return "", err
		}
		logger.Debug("oap sdk: binary resolved from explicit path", "path", resolved)
		return resolved, nil
	}

	binaryURL := firstNonEmpty(os.Getenv(EnvBinaryURL), opts.BinaryURL)
	if binaryURL != "" {
		checksum := firstNonEmpty(os.Getenv(EnvBinarySHA256), opts.ChecksumSHA256)
		if checksum == "" {
			return "", fmt.Errorf("%w: %s", ErrChecksumRequired, binaryURL)
		}
		return resolveFromURL(ctx, binaryURL, strings.ToLower(checksum), opts.CacheDir, logger)
	}

	for _, candidate := range localCandidates() {
		if err := checkExecutable(candidate); err == nil {
			logger.Debug("oap sdk: binary resolved from local build", "path", candidate)
			return candidate, nil
		}
	}

	name := binaryName()
	found, err := exec.LookPath(name)
	if err == nil {
		logger.Debug("oap sdk: binary resolved from PATH", "path", found)
		return found, nil
	}
	return "", fmt.Errorf("%w: no %s on PATH and no local build under %s",
		ErrBinaryNotFound, name, strings.Join(localCandidates(), " or "))
}

func localCandidates() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	name := binaryName()
	return []string{
		filepath.Join(cwd, "zig-out", "bin", name),
		filepath.Join(cwd, "zig", "zig-out", "bin", name),
	}
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "oapx.exe"
	}
	return "oapx"
}

func resolveFromURL(ctx context.Context, rawURL, checksum, cacheDir string, logger *slog.Logger) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("oap sdk: invalid binary URL %q: %w", rawURL, err)
	}
	if cacheDir == "" {
		cacheDir, err = defaultCacheDir()
		if err != nil {
			return "", err
		}
	}
	fileName := filepath.Base(parsed.Path)
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		fileName = binaryName()
	}
	cachePath := filepath.Join(cacheDir, fileName)

	if _, err := os.Stat(cachePath); err == nil {
		if verifyErr := verifyChecksum(cachePath, checksum); verifyErr == nil {
			logger.Debug("oap sdk: cached binary checksum verified", "path", cachePath)
			return cachePath, nil
		} else if !errors.Is(verifyErr, ErrChecksumMismatch) {
			return "", verifyErr
		}
		logger.Warn("oap sdk: cached binary checksum mismatch, re-downloading", "path", cachePath)
		if err := os.Remove(cachePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("oap sdk: cannot evict cached binary %q: %w", cachePath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("oap sdk: cannot inspect cached binary %q: %w", cachePath, err)
	}

	logger.Debug("oap sdk: downloading binary", "url", rawURL, "target", cachePath)
	if err := downloadToCache(ctx, rawURL, cachePath, checksum); err != nil {
		return "", err
	}
	logger.Info("oap sdk: binary download complete", "path", cachePath, "sha256", checksum)
	return cachePath, nil
}

func downloadToCache(ctx context.Context, rawURL, targetPath, checksum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("oap sdk: cannot build binary download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("oap sdk: cannot download binary from %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oap sdk: cannot download binary from %s: %s", rawURL, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("oap sdk: cannot create binary cache directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(targetPath), filepath.Base(targetPath)+".*.partial")
	if err != nil {
		return fmt.Errorf("oap sdk: cannot create temporary download file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temp, digest), resp.Body); err != nil {
		temp.Close()
		return fmt.Errorf("oap sdk: binary download failed: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("oap sdk: binary download failed: %w", err)
	}

	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != checksum {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, checksum, actual)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tempPath, 0o755); err != nil {
			return fmt.Errorf("oap sdk: cannot make downloaded binary executable: %w", err)
		}
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return fmt.Errorf("oap sdk: cannot install downloaded binary: %w", err)
	}
	return nil
}

func verifyChecksum(path, checksum string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("oap sdk: cannot read binary %q: %w", path, err)
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("oap sdk: cannot read binary %q: %w", path, err)
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != checksum {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, checksum, actual)
	}
	return nil
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrBinaryNotFound, path)
		}
		return fmt.Errorf("oap sdk: cannot inspect binary %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %s is a directory", ErrBinaryNotFound, path)
	}
	return nil
}

func defaultCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("oap sdk: cannot determine a binary cache directory: %w", err)
	}
	return filepath.Join(base, "oapx", "bin"), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
