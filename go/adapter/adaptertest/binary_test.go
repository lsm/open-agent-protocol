package adaptertest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, contents string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pinned")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256([]byte(contents))
	return path, hex.EncodeToString(sum[:])
}

func TestVerifyBinaryAcceptsAnAbsoluteExecutableWithNoDigestDemanded(t *testing.T) {
	path, _ := writeExecutable(t, "#!/bin/sh\n")

	got, err := verifyBinary(path, "", "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Fatalf("returned %q, want %q", got, path)
	}
}

func TestVerifyBinaryAcceptsTheDigestItWasGiven(t *testing.T) {
	path, digest := writeExecutable(t, "#!/bin/sh\necho pinned\n")

	if _, err := verifyBinary(path, digest, "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable"); err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	if _, err := verifyBinary(path, strings.ToUpper(digest), "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable"); err != nil {
		t.Fatalf("digest compared case-sensitively: %v", err)
	}
}

func TestVerifyBinaryRefusesAnArtifactThatIsNotTheOneNamed(t *testing.T) {
	path, _ := writeExecutable(t, "#!/bin/sh\necho pinned\n")
	_, other := writeExecutable(t, "#!/bin/sh\necho substituted\n")

	_, err := verifyBinary(path, other, "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable")
	if err == nil {
		t.Fatal("a binary whose digest does not match the demanded one was accepted")
	}
	if !strings.Contains(err.Error(), "SHA-256 does not match") {
		t.Fatalf("error does not name the mismatch: %v", err)
	}
}

func TestVerifyBinaryRefusesADigestThatCannotBeOne(t *testing.T) {
	path, digest := writeExecutable(t, "#!/bin/sh\n")

	for name, supplied := range map[string]string{
		"too short":   digest[:63],
		"too long":    digest + "0",
		"not hex":     strings.Repeat("z", 64),
		"empty-ish":   strings.Repeat(" ", 64),
		"hex-ish odd": strings.Repeat("a", 63) + "!",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifyBinary(path, supplied, "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable")
			if err == nil {
				t.Fatal("accepted a value that cannot be a sha-256 digest")
			}
			if !strings.Contains(err.Error(), "64 hexadecimal characters") {
				t.Fatalf("error does not say what was wrong: %v", err)
			}
		})
	}
}

func TestVerifyBinaryRefusesAPathThatIsNotAnAbsoluteExecutable(t *testing.T) {
	directory := t.TempDir()
	unreadable := filepath.Join(directory, "not-executable")
	if err := os.WriteFile(unreadable, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for name, path := range map[string]string{
		"empty":         "",
		"relative":      "bin/pinned",
		"missing":       filepath.Join(directory, "absent"),
		"a directory":   directory,
		"not +x":        unreadable,
		"relative dots": "../pinned",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyBinary(path, "", "OAP_X_BIN", "OAP_X_SHA256", "the pinned executable"); err == nil {
				t.Fatalf("accepted %q", path)
			}
		})
	}
}

func TestVerifyBinaryNamesTheVariableTheCallerUsed(t *testing.T) {
	_, err := verifyBinary("", "", "OAP_HERMES_BIN", "OAP_HERMES_SHA256", "the pinned interpreter")
	if err == nil || !strings.Contains(err.Error(), "OAP_HERMES_BIN") {
		t.Fatalf("path error does not name the caller's variable: %v", err)
	}

	path, _ := writeExecutable(t, "#!/bin/sh\n")
	_, err = verifyBinary(path, "nope", "OAP_HERMES_BIN", "OAP_HERMES_SHA256", "the pinned interpreter")
	if err == nil || !strings.Contains(err.Error(), "OAP_HERMES_SHA256") {
		t.Fatalf("digest error does not name the caller's variable: %v", err)
	}
}
