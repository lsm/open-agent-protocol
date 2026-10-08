package sdk

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestNewULIDHasTheWireFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := newULID()
		if len(id) != ulidLength {
			t.Fatalf("len(%q) = %d, want %d", id, len(id), ulidLength)
		}
		if id != strings.ToUpper(id) {
			t.Fatalf("%q is not uppercase", id)
		}
		for _, char := range id {
			if !strings.ContainsRune(crockford, char) {
				t.Fatalf("%q contains %q, which is outside Crockford Base32", id, char)
			}
		}

		if strings.ContainsAny(id, "ILOU") {
			t.Fatalf("%q contains an excluded Crockford letter", id)
		}
		if seen[id] {
			t.Fatalf("%q was generated twice", id)
		}
		seen[id] = true
	}
}

func TestNewULIDIsMonotonicWithinAMillisecond(t *testing.T) {
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = newULID()
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }) {
		t.Error("ULIDs generated in a burst should sort in generation order")
	}
}

func TestNewULIDIsSafeForConcurrentUse(t *testing.T) {
	const workers, perWorker = 8, 200

	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, perWorker)
			for j := range local {
				local[j] = newULID()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if seen[id] {
					t.Errorf("%q was generated twice", id)
				}
				seen[id] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*perWorker {
		t.Errorf("got %d unique ids, want %d", len(seen), workers*perWorker)
	}
}

func TestNewNanoIDHasTheWireFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := newNanoID()
		if !regexp.MustCompile(`^[0-9A-Za-z]{21}$`).MatchString(id) {
			t.Fatalf("%q is not a 21-character alphanumeric NanoID", id)
		}
		if seen[id] {
			t.Fatalf("%q was generated twice", id)
		}
		seen[id] = true
	}
}

func TestEncodeBase32CoversTheAlphabet(t *testing.T) {

	src := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	dst := make([]byte, 16)
	encodeBase32(dst, src)

	for _, char := range dst {
		if !strings.ContainsRune(crockford, rune(char)) {
			t.Fatalf("encoded %q is outside the alphabet", char)
		}
	}

	if got := string(dst); got != "008J4CT4ANK7F24S" {
		t.Errorf("encodeBase32 = %q", got)
	}
}
