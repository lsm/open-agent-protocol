package sdk

import (
	"crypto/rand"
	"sync"
	"time"
)

const (
	ulidLength   = 26
	nanoIDLength = 21
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const nanoAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var ulidState struct {
	sync.Mutex
	lastMillis int64
	lastRandom [10]byte
}

func newULID() string {
	now := time.Now().UnixMilli()

	ulidState.Lock()
	if now != ulidState.lastMillis || !incrementRandom(&ulidState.lastRandom) {
		ulidState.lastMillis = now
		randomBytes(ulidState.lastRandom[:])
	}
	entropy := ulidState.lastRandom
	millis := ulidState.lastMillis
	ulidState.Unlock()

	out := make([]byte, ulidLength)

	ts := uint64(millis) & 0xFFFFFFFFFFFF
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ts&0x1F]
		ts >>= 5
	}

	encodeBase32(out[10:], entropy[:])
	return string(out)
}

func encodeBase32(dst, src []byte) {
	bit := 0
	for i := range dst {
		v := byte(0)
		for k := 0; k < 5; k++ {
			v = v<<1 | ((src[bit>>3] >> (7 - uint(bit&7))) & 1)
			bit++
		}
		dst[i] = crockford[v]
	}
}

func incrementRandom(b *[10]byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

func newNanoID() string {
	out := make([]byte, nanoIDLength)

	const limit = byte(256 - (256 % len(nanoAlphabet)))
	buf := make([]byte, nanoIDLength*2)
	filled := 0
	for filled < nanoIDLength {
		randomBytes(buf)
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out[filled] = nanoAlphabet[int(b)%len(nanoAlphabet)]
			filled++
			if filled == nanoIDLength {
				break
			}
		}
	}
	return string(out)
}

func randomBytes(b []byte) {
	if _, err := rand.Read(b); err != nil {
		now := uint64(time.Now().UnixNano())
		for i := range b {
			now = now*6364136223846793005 + 1442695040888963407
			b[i] = byte(now >> 32)
		}
	}
}
