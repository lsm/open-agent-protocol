package adaptertest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func VerifiedBinary(t *testing.T, pathVariable, digestVariable, description string) string {
	t.Helper()
	binary, err := verifyBinary(os.Getenv(pathVariable), os.Getenv(digestVariable), pathVariable, digestVariable, description)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func verifyBinary(binary, expected, pathVariable, digestVariable, description string) (string, error) {
	if binary == "" || !filepath.IsAbs(binary) {
		return "", fmt.Errorf("%s must be an absolute path to %s", pathVariable, description)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return "", fmt.Errorf("%s is not an executable file: %w", pathVariable, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", pathVariable)
	}
	if expected == "" {
		return binary, nil
	}
	if len(expected) != sha256.Size*2 {
		return "", fmt.Errorf("%s must be exactly 64 hexadecimal characters", digestVariable)
	}
	expectedDigest, err := hex.DecodeString(expected)
	if err != nil {
		return "", fmt.Errorf("%s must be exactly 64 hexadecimal characters", digestVariable)
	}
	file, err := os.Open(binary)
	if err != nil {
		return "", fmt.Errorf("open %s for digest verification: %w", pathVariable, err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("hash %s: %w", pathVariable, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close %s after hashing: %w", pathVariable, closeErr)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
		return "", fmt.Errorf("%s SHA-256 does not match %s", pathVariable, digestVariable)
	}
	return binary, nil
}
