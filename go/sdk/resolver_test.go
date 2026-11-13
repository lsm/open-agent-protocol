package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func writeFakeBinary(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	})
}

func isolateResolverEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OAP_SDK_BINARY_PATH", "OAP_SDK_BINARY_URL", "OAP_SDK_BINARY_SHA256",
		"OXAP_SDK_BINARY_PATH", "OXAP_SDK_BINARY_URL", "OXAP_SDK_BINARY_SHA256",
	} {
		t.Setenv(name, "")
	}
}

func TestResolveBinaryUsesTheExplicitPath(t *testing.T) {
	isolateResolverEnv(t)
	path := writeFakeBinary(t, t.TempDir(), "makai", "#!/bin/sh\n")

	resolved, err := ResolveBinary(context.Background(), &Options{BinaryPath: path})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != path {
		t.Errorf("resolved = %q, want %q", resolved, path)
	}
}

func TestResolveBinaryEnvironmentOverridesTheOption(t *testing.T) {

	isolateResolverEnv(t)
	dir := t.TempDir()
	fromEnv := writeFakeBinary(t, dir, "from-env", "#!/bin/sh\n")
	fromOption := writeFakeBinary(t, dir, "from-option", "#!/bin/sh\n")
	t.Setenv(EnvBinaryPath, fromEnv)

	resolved, err := ResolveBinary(context.Background(), &Options{BinaryPath: fromOption})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != fromEnv {
		t.Errorf("resolved = %q, want the environment's %q", resolved, fromEnv)
	}
}

func TestResolveBinaryRejectsAMissingExplicitPath(t *testing.T) {
	isolateResolverEnv(t)

	_, err := ResolveBinary(context.Background(), &Options{
		BinaryPath: filepath.Join(t.TempDir(), "absent"),
	})
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestResolveBinaryRejectsADirectory(t *testing.T) {
	isolateResolverEnv(t)

	_, err := ResolveBinary(context.Background(), &Options{BinaryPath: t.TempDir()})
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestResolveBinaryRequiresAChecksumForURLs(t *testing.T) {
	isolateResolverEnv(t)

	_, err := ResolveBinary(context.Background(), &Options{
		BinaryURL: "https://example.invalid/makai",
	})
	if !errors.Is(err, ErrChecksumRequired) {
		t.Fatalf("expected ErrChecksumRequired, got %v", err)
	}
}

func TestResolveBinaryDownloadsAndVerifies(t *testing.T) {
	isolateResolverEnv(t)
	payload := []byte("#!/bin/sh\necho makai\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(payload)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	opts := &Options{
		BinaryURL:      server.URL + "/dist/makai",
		ChecksumSHA256: strings.ToUpper(checksum),
		CacheDir:       cacheDir,
	}

	resolved, err := ResolveBinary(context.Background(), opts)
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if want := filepath.Join(cacheDir, "makai"); resolved != want {
		t.Errorf("resolved = %q, want %q", resolved, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(resolved)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("downloaded binary is not executable: %s", info.Mode())
		}
	}

	if _, err := ResolveBinary(context.Background(), opts); err != nil {
		t.Fatalf("second ResolveBinary: %v", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1: the cache was not reused", requests)
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "partial") {
			t.Errorf("temporary download file %q was left behind", entry.Name())
		}
	}
}

func TestResolveBinaryRejectsAChecksumMismatch(t *testing.T) {
	isolateResolverEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not what you asked for"))
	}))
	defer server.Close()

	_, err := ResolveBinary(context.Background(), &Options{
		BinaryURL:      server.URL + "/makai",
		ChecksumSHA256: strings.Repeat("ab", 32),
		CacheDir:       t.TempDir(),
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestResolveBinaryRedownloadsAfterACacheMismatch(t *testing.T) {
	isolateResolverEnv(t)
	payload := []byte("#!/bin/sh\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()

	cacheDir := t.TempDir()

	writeFakeBinary(t, cacheDir, "makai", "tampered")

	resolved, err := ResolveBinary(context.Background(), &Options{
		BinaryURL:      server.URL + "/makai",
		ChecksumSHA256: checksum,
		CacheDir:       cacheDir,
	})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload) {
		t.Errorf("cached content = %q, want the re-downloaded payload", content)
	}
}

func TestResolveBinaryRejectsAFailedDownload(t *testing.T) {
	isolateResolverEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := ResolveBinary(context.Background(), &Options{
		BinaryURL:      server.URL + "/makai",
		ChecksumSHA256: strings.Repeat("ab", 32),
		CacheDir:       t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected the HTTP status to be reported, got %v", err)
	}
}

func TestResolveBinaryPrefersLocalZigBuilds(t *testing.T) {
	isolateResolverEnv(t)
	workdir := t.TempDir()

	first := writeFakeBinary(t, filepath.Join(workdir, "zig-out", "bin"), "oxap", "#!/bin/sh\n")
	writeFakeBinary(t, filepath.Join(workdir, "zig", "zig-out", "bin"), "oxap", "#!/bin/sh\n")

	chdir(t, workdir)
	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}

	wantReal, _ := filepath.EvalSymlinks(first)
	gotReal, _ := filepath.EvalSymlinks(resolved)
	if gotReal != wantReal {
		t.Errorf("resolved = %q, want %q", gotReal, wantReal)
	}
}

func TestResolveBinaryFallsBackToTheNestedZigBuild(t *testing.T) {
	isolateResolverEnv(t)
	workdir := t.TempDir()
	nested := writeFakeBinary(t, filepath.Join(workdir, "zig", "zig-out", "bin"), "oxap", "#!/bin/sh\n")

	chdir(t, workdir)
	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(nested)
	gotReal, _ := filepath.EvalSymlinks(resolved)
	if gotReal != wantReal {
		t.Errorf("resolved = %q, want %q", gotReal, wantReal)
	}
}

func TestResolveBinaryPrefersOxapOverOapxInTheZigBuild(t *testing.T) {
	isolateResolverEnv(t)
	workdir := t.TempDir()
	writeFakeBinary(t, filepath.Join(workdir, "zig-out", "bin"), "oapx", "#!/bin/sh\n")
	want := resolvedPath(t, writeFakeBinary(t, filepath.Join(workdir, "zig", "zig-out", "bin"), "oxap", "#!/bin/sh\n"))

	chdir(t, workdir)
	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != want {
		t.Errorf("resolved = %q, want the nested oxap build %q", resolved, want)
	}
}

func TestResolveBinaryFallsBackToTheOapxZigBuild(t *testing.T) {
	isolateResolverEnv(t)
	workdir := t.TempDir()
	want := resolvedPath(t, writeFakeBinary(t, filepath.Join(workdir, "zig-out", "bin"), "oapx", "#!/bin/sh\n"))

	chdir(t, workdir)
	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != want {
		t.Errorf("resolved = %q, want the legacy name's build %q", resolved, want)
	}
}

func TestResolveBinaryFallsBackToPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH fallback uses a shell script stub")
	}
	isolateResolverEnv(t)
	pathDir := t.TempDir()
	onPath := writeFakeBinary(t, pathDir, "oapx", "#!/bin/sh\n")

	chdir(t, t.TempDir())
	t.Setenv("PATH", pathDir)

	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	wantReal, _ := filepath.EvalSymlinks(onPath)
	gotReal, _ := filepath.EvalSymlinks(resolved)
	if gotReal != wantReal {
		t.Errorf("resolved = %q, want %q", gotReal, wantReal)
	}
}

func TestResolveBinaryPrefersOxapOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH fallback uses a shell script stub")
	}
	isolateResolverEnv(t)
	pathDir := t.TempDir()
	writeFakeBinary(t, pathDir, "oapx", "#!/bin/sh\n")
	onPath := writeFakeBinary(t, pathDir, "oxap", "#!/bin/sh\n")

	chdir(t, t.TempDir())
	t.Setenv("PATH", pathDir)

	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != onPath {
		t.Errorf("resolved = %q, want %q", resolved, onPath)
	}
}

func TestResolveBinaryReportsWhenNothingIsFound(t *testing.T) {
	isolateResolverEnv(t)
	chdir(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())

	_, err := ResolveBinary(context.Background(), &Options{})
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}

	message := err.Error()
	for _, want := range []string{"oxap", "oapx", "zig-out"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q; it should mention %q", message, want)
		}
	}
}

func TestResolveBinaryPrefersTheOxapEnvironmentOverTheLegacyOne(t *testing.T) {
	isolateResolverEnv(t)
	dir := t.TempDir()
	preferred := writeFakeBinary(t, dir, "from-oxap-env", "#!/bin/sh\n")
	legacy := writeFakeBinary(t, dir, "from-oap-env", "#!/bin/sh\n")
	t.Setenv("OXAP_SDK_BINARY_PATH", preferred)
	t.Setenv("OAP_SDK_BINARY_PATH", legacy)

	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != preferred {
		t.Errorf("resolved = %q, want OXAP_SDK_BINARY_PATH's %q", resolved, preferred)
	}
}

func TestResolveBinaryReadsTheLegacyEnvironmentWhenTheOxapOneIsAbsent(t *testing.T) {
	isolateResolverEnv(t)
	legacy := writeFakeBinary(t, t.TempDir(), "from-oap-env", "#!/bin/sh\n")
	t.Setenv("OAP_SDK_BINARY_PATH", legacy)

	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if resolved != legacy {
		t.Errorf("resolved = %q, want the legacy variable's %q", resolved, legacy)
	}
}

func TestResolveBinaryPrefersTheOxapURLAndChecksumOverTheLegacyOnes(t *testing.T) {
	isolateResolverEnv(t)
	legacyPayload := []byte("#!/bin/sh\necho legacy\n")
	preferredPayload := []byte("#!/bin/sh\necho preferred\n")
	legacyDigest := sha256.Sum256(legacyPayload)
	preferredDigest := sha256.Sum256(preferredPayload)

	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if r.URL.Path == "/preferred/oxap" {
			w.Write(preferredPayload)
			return
		}
		w.Write(legacyPayload)
	}))
	defer server.Close()

	t.Setenv("OXAP_SDK_BINARY_URL", server.URL+"/preferred/oxap")
	t.Setenv("OXAP_SDK_BINARY_SHA256", hex.EncodeToString(preferredDigest[:]))
	t.Setenv("OAP_SDK_BINARY_URL", server.URL+"/legacy/oapx")
	t.Setenv("OAP_SDK_BINARY_SHA256", hex.EncodeToString(legacyDigest[:]))

	resolved, err := ResolveBinary(context.Background(), &Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if want := filepath.Join(filepath.Dir(resolved), "oxap"); resolved != want {
		t.Errorf("resolved = %q, want the preferred URL's %q", resolved, want)
	}
	if !slices.Equal(requested, []string{"/preferred/oxap"}) {
		t.Errorf("requested %q, want only the preferred URL", requested)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(preferredPayload) {
		t.Errorf("downloaded %q, want the preferred payload", content)
	}
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestLegacyMakaiDoesNotShadowOapx(t *testing.T) {
	isolateResolverEnv(t)
	dir := t.TempDir()
	binDir := filepath.Join(dir, "zig-out", "bin")
	writeFakeBinary(t, binDir, "makai", "#!/bin/sh\n")
	want := resolvedPath(t, writeFakeBinary(t, binDir, "oapx", "#!/bin/sh\n"))
	chdir(t, dir)
	resolved, err := ResolveBinary(context.Background(), &Options{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Errorf("resolved = %q, want %q", resolved, want)
	}
}

func TestAnInstallPredatingTheRenameIsNotResolved(t *testing.T) {
	isolateResolverEnv(t)
	dir := t.TempDir()
	writeFakeBinary(t, filepath.Join(dir, "zig-out", "bin"), "makai", "#!/bin/sh\n")
	chdir(t, dir)
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveBinary(context.Background(), &Options{})
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}
