package cache

import (
	"hash"
	"math/bits"

	"github.com/pierrec/xxHash/xxHash64"
)

const chibiHashK uint64 = 0x2B7E151628AED2A7
const chibiHashK3 uint64 = 0x0208a6fd10f4ba56 //(chibiHashK * chibiHashK) ^ chibiHashK

func load32le(p []byte) uint64 {
	return uint64(p[0]) |
		uint64(p[1])<<8 |
		uint64(p[2])<<16 |
		uint64(p[3])<<24
}

func load64le(p []byte) uint64 {
	return load32le(p) | (load32le(p[4:8]) << 32)
}

type StoreKey struct {
	Key      uint64
	Conflict uint64
}

func StoreKeyFromUint64(key uint64) StoreKey {
	return StoreKey{
		Key:      key,
		Conflict: key,
	}
}

func StoreKeyFromString(key string, keySeed, conflictSeed uint64) StoreKey {
	return StoreKeyFromBytes([]byte(key), keySeed, conflictSeed)
}

func StoreKeyFromBytes(key []byte, keySeed, conflictSeed uint64) StoreKey {
	return StoreKey{
		Key:      xxHash64.Checksum(key, keySeed),
		Conflict: ChibiHash64(key, conflictSeed),
	}
}

type StoreKeyHash struct {
	primaryHasher hash.Hash64
	length        int

	buffer    [32]byte
	bufferPos int

	conflictSeed uint64
	conflictH    [4]uint64
}

func NewStoreKeyHash(keySeed, conflictSeed uint64) *StoreKeyHash {
	result := &StoreKeyHash{
		primaryHasher: xxHash64.New(keySeed),
		conflictSeed:  conflictSeed,
	}

	result.Reset()
	return result
}

func (h *StoreKeyHash) Sum(b []byte) []byte {
	storeKey := h.StoreKey()

	return append(b,
		byte(storeKey.Key>>56),
		byte(storeKey.Key>>48),
		byte(storeKey.Key>>40),
		byte(storeKey.Key>>32),
		byte(storeKey.Key>>24),
		byte(storeKey.Key>>16),
		byte(storeKey.Key>>8),
		byte(storeKey.Key),
		byte(storeKey.Conflict>>56),
		byte(storeKey.Conflict>>48),
		byte(storeKey.Conflict>>40),
		byte(storeKey.Conflict>>32),
		byte(storeKey.Conflict>>24),
		byte(storeKey.Conflict>>16),
		byte(storeKey.Conflict>>8),
		byte(storeKey.Conflict),
	)
}

func (h *StoreKeyHash) Reset() {
	seed2 := bits.RotateLeft64(h.conflictSeed-chibiHashK, 15) + bits.RotateLeft64(h.conflictSeed-chibiHashK, 47)

	h.primaryHasher.Reset()
	h.length = 0
	h.conflictH = [4]uint64{
		h.conflictSeed,
		h.conflictSeed + chibiHashK,
		seed2,
		seed2 + chibiHashK3,
	}
	h.bufferPos = 0
}

func (h *StoreKeyHash) Size() int {
	return 16
}

func (h *StoreKeyHash) BlockSize() int {
	return 1
}

func (h *StoreKeyHash) Write(p []byte) (int, error) {
	n, err := h.primaryHasher.Write(p)
	if err != nil {
		return n, err
	}

	l := len(p)
	h.length += l
	pos := 0

	if h.bufferPos > 0 {
		remaining := 32 - h.bufferPos
		if len(p) < remaining {
			copy(h.buffer[h.bufferPos:], p)
			h.bufferPos += l
			return n, nil
		} else {
			copy(h.buffer[h.bufferPos:], p[:remaining])
			pos += remaining

			h.processBlock(h.buffer[:])
			h.bufferPos = 0
		}
	}

	for pos+32 <= l {
		h.processBlock(p[pos : pos+32])
		pos += 32
	}

	if pos < l {
		remaining := l - pos
		copy(h.buffer[:], p[pos:])
		h.bufferPos = remaining
	}

	return n, nil
}

func (h *StoreKeyHash) processBlock(block []byte) {
	i := 0
	for j := 0; j < 4; j++ {
		stripe := load64le(block[i : i+8])
		h.conflictH[j] = (stripe + h.conflictH[j]) * chibiHashK
		h.conflictH[(j+1)&3] += bits.RotateLeft64(stripe, 27)
		i += 8
	}
}

func (h *StoreKeyHash) Sum32() uint32 {
	return uint32(h.Sum64())
}

func (h *StoreKeyHash) Sum64() uint64 {
	return h.primaryHasher.Sum64()
}

func (h *StoreKeyHash) StoreKey() StoreKey {
	result := StoreKey{
		Key: h.primaryHasher.Sum64(),
	}

	i := 0
	conflictH := [4]uint64{
		h.conflictH[0],
		h.conflictH[1],
		h.conflictH[2],
		h.conflictH[3],
	}

	for h.bufferPos-i >= 8 {
		conflictH[0] ^= load32le(h.buffer[i : i+4])
		conflictH[0] *= chibiHashK
		conflictH[1] ^= load32le(h.buffer[i+4 : i+8])
		conflictH[1] *= chibiHashK
		i += 8
	}

	remaining := h.bufferPos - i
	if remaining >= 4 {
		conflictH[2] ^= load32le(h.buffer[i : i+4])
		conflictH[3] ^= load32le(h.buffer[i+remaining-4 : i+remaining])
	} else if remaining > 0 {
		conflictH[2] ^= uint64(h.buffer[i])
		conflictH[3] ^= uint64(h.buffer[i+remaining/2]) |
			(uint64(h.buffer[i+remaining-1]) << 8)
	}

	conflictH[0] += bits.RotateLeft64(conflictH[2]*chibiHashK, 31) ^ (conflictH[2] >> 31)
	conflictH[1] += bits.RotateLeft64(conflictH[3]*chibiHashK, 31) ^ (conflictH[3] >> 31)
	conflictH[0] *= chibiHashK
	conflictH[0] ^= conflictH[0] >> 31
	conflictH[1] += conflictH[0]

	result.Conflict = uint64(h.length) * chibiHashK
	result.Conflict ^= bits.RotateLeft64(result.Conflict, 29)
	result.Conflict += h.conflictSeed
	result.Conflict ^= conflictH[1]

	result.Conflict ^= bits.RotateLeft64(result.Conflict, 15) ^ bits.RotateLeft64(result.Conflict, 42)
	result.Conflict *= chibiHashK
	result.Conflict ^= bits.RotateLeft64(result.Conflict, 13) ^ bits.RotateLeft64(result.Conflict, 31)

	return result
}

func ChibiHash64(key []byte, seed uint64) uint64 {

	l := len(key)
	i := 0

	seed2 := bits.RotateLeft64(seed-chibiHashK, 15) + bits.RotateLeft64(seed-chibiHashK, 47)
	h := [4]uint64{
		seed,
		seed + chibiHashK,
		seed2,
		seed2 + chibiHashK3,
	}

	for l-i >= 32 {
		for j := 0; j < 4; j++ {
			stripe := load64le(key[i : i+8])
			h[j] = (stripe + h[j]) * chibiHashK
			h[(j+1)&3] += bits.RotateLeft64(stripe, 27)
			i += 8
		}
	}

	for l-i >= 8 {
		h[0] ^= load32le(key[i : i+4])
		h[0] *= chibiHashK
		h[1] ^= load32le(key[i+4 : i+8])
		h[1] *= chibiHashK
		i += 8
	}

	remaining := l - i
	if remaining >= 4 {
		h[2] ^= load32le(key[i : i+4])
		h[3] ^= load32le(key[i+remaining-4 : i+remaining])
	} else if remaining > 0 {
		h[2] ^= uint64(key[i])
		h[3] ^= uint64(key[i+remaining/2]) | (uint64(key[i+remaining-1]) << 8)
	}

	h[0] += bits.RotateLeft64(h[2]*chibiHashK, 31) ^ (h[2] >> 31)
	h[1] += bits.RotateLeft64(h[3]*chibiHashK, 31) ^ (h[3] >> 31)
	h[0] *= chibiHashK
	h[0] ^= h[0] >> 31
	h[1] += h[0]

	x := uint64(l) * chibiHashK
	x ^= bits.RotateLeft64(x, 29)
	x += seed
	x ^= h[1]

	x ^= bits.RotateLeft64(x, 15) ^ bits.RotateLeft64(x, 42)
	x *= chibiHashK
	x ^= bits.RotateLeft64(x, 13) ^ bits.RotateLeft64(x, 31)

	return x
}
