package cache_test

import (
	"testing"

	"github.com/pierrec/xxHash/xxHash64"
	"github.com/yerTools/go_reverse_http_cache/src/go/cache"
)

func TestStoreKeyFromBytes(t *testing.T) {
	tests := []struct {
		name                          string
		input                         string
		keySeed, conflictSeed         uint64
		expectedKey, expectedConflict uint64
	}{
		{
			name:             "empty input",
			input:            "",
			keySeed:          12345,
			conflictSeed:     54321,
			expectedKey:      10761433736200290445,
			expectedConflict: 15923434876930560991,
		},
		{
			name:             "simple input",
			input:            "test",
			keySeed:          12345,
			conflictSeed:     54321,
			expectedKey:      7624679986283906467,
			expectedConflict: 16437614941612851701,
		},
		{
			name:             "longer input",
			input:            "The quick brown fox jumps over the lazy dog",
			keySeed:          9999,
			conflictSeed:     8888,
			expectedKey:      5818969631507634107,
			expectedConflict: 4068965658285126768,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inBytes := []byte(tc.input)

			expectedKey := xxHash64.Checksum(inBytes, tc.keySeed)
			expectedConflict := cache.ChibiHash64(inBytes, tc.conflictSeed)

			sk := cache.StoreKeyFromBytes(inBytes, tc.keySeed, tc.conflictSeed)

			if sk.Key != expectedKey {
				t.Errorf("StoreKeyFromBytes(%q) Key = %d, expected (xxHash64.Checksum) %d", tc.input, sk.Key, expectedKey)
			}
			if sk.Conflict != expectedConflict {
				t.Errorf("StoreKeyFromBytes(%q) Conflict = %d, expected (ChibiHash64) %d", tc.input, sk.Conflict, expectedConflict)
			}

			if sk.Key != tc.expectedKey {
				t.Errorf("StoreKeyFromBytes(%q) Key = %d, expected Key = %d", tc.input, sk.Key, tc.expectedKey)
			}
			if sk.Conflict != tc.expectedConflict {
				t.Errorf("StoreKeyFromBytes(%q) Conflict = %d, expected Conflict = %d", tc.input, sk.Conflict, tc.expectedConflict)
			}
		})
	}
}

func TestStoreKeyHash_SingleWrite(t *testing.T) {
	input := []byte("The quick brown fox jumps over the lazy dog")
	keySeed := uint64(12345)
	conflictSeed := uint64(54321)

	expectedSK := cache.StoreKeyFromBytes(input, keySeed, conflictSeed)

	h := cache.NewStoreKeyHash(keySeed, conflictSeed)
	n, err := h.Write(input)
	if err != nil {
		t.Fatalf("Write() returned error: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write() wrote %d bytes, expected %d", n, len(input))
	}

	sk := h.StoreKey()

	if sk.Key != expectedSK.Key {
		t.Errorf("StoreKeyHash Key = %d, expected %d", sk.Key, expectedSK.Key)
	}
	if sk.Conflict != expectedSK.Conflict {
		t.Errorf("StoreKeyHash Conflict = %d, expected %d", sk.Conflict, expectedSK.Conflict)
	}

	if sk.Key != uint64(15110577153032780705) {
		t.Errorf("StoreKeyHash Key = %d, expected Key = %d", sk.Key, uint64(15110577153032780705))
	}
	if sk.Conflict != uint64(8011604537839412478) {
		t.Errorf("StoreKeyHash Conflict = %d, expected Conflict = %d", sk.Conflict, uint64(8011604537839412478))
	}
}

func TestStoreKeyHash_MultipleWrites(t *testing.T) {
	input := []byte("The quick brown fox jumps over the lazy dog")
	keySeed := uint64(12345)
	conflictSeed := uint64(54321)

	expectedSK := cache.StoreKeyFromBytes(input, keySeed, conflictSeed)

	h := cache.NewStoreKeyHash(keySeed, conflictSeed)
	for i := 0; i < len(input); i++ {
		n, err := h.Write(input[i : i+1])
		if err != nil {
			t.Fatalf("Write() error: %v", err)
		}
		if n != 1 {
			t.Errorf("Write() returned %d bytes, expected 1", n)
		}
	}
	sk := h.StoreKey()

	if sk.Key != expectedSK.Key {
		t.Errorf("Multiple Writes: Key = %d, expected %d", sk.Key, expectedSK.Key)
	}
	if sk.Conflict != expectedSK.Conflict {
		t.Errorf("Multiple Writes: Conflict = %d, expected %d", sk.Conflict, expectedSK.Conflict)
	}

	if sk.Key != uint64(15110577153032780705) {
		t.Errorf("Multiple Writes: Key = %d, expected Key = %d", sk.Key, uint64(15110577153032780705))
	}
	if sk.Conflict != uint64(8011604537839412478) {
		t.Errorf("Multiple Writes: Conflict = %d, expected Conflict = %d", sk.Conflict, uint64(8011604537839412478))
	}
}

func TestStoreKeyHash_Reset(t *testing.T) {
	input1 := []byte("First input")
	input2 := []byte("Second input")
	keySeed := uint64(11111)
	conflictSeed := uint64(22222)

	h := cache.NewStoreKeyHash(keySeed, conflictSeed)
	_, err := h.Write(input1)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	sk1 := h.StoreKey()

	h.Reset()

	_, err = h.Write(input2)
	if err != nil {
		t.Fatalf("Write() after Reset error: %v", err)
	}
	sk2 := h.StoreKey()

	if sk1.Key == sk2.Key {
		t.Errorf("Reset did not change Key: both are %d", sk1.Key)
	}
	if sk1.Conflict == sk2.Conflict {
		t.Errorf("Reset did not change Conflict: both are %d", sk1.Conflict)
	}
}

func TestSum_Method(t *testing.T) {
	input := []byte("Sample input for Sum method")
	keySeed := uint64(12345)
	conflictSeed := uint64(54321)

	h := cache.NewStoreKeyHash(keySeed, conflictSeed)
	_, err := h.Write(input)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	sk := h.StoreKey()

	sumBytes := h.Sum(nil)
	if len(sumBytes) != 16 {
		t.Errorf("Sum() returned %d bytes, expected 16", len(sumBytes))
	}

	recKey := uint64(sumBytes[0])<<56 | uint64(sumBytes[1])<<48 | uint64(sumBytes[2])<<40 |
		uint64(sumBytes[3])<<32 | uint64(sumBytes[4])<<24 | uint64(sumBytes[5])<<16 |
		uint64(sumBytes[6])<<8 | uint64(sumBytes[7])
	recConflict := uint64(sumBytes[8])<<56 | uint64(sumBytes[9])<<48 | uint64(sumBytes[10])<<40 |
		uint64(sumBytes[11])<<32 | uint64(sumBytes[12])<<24 | uint64(sumBytes[13])<<16 |
		uint64(sumBytes[14])<<8 | uint64(sumBytes[15])

	if recKey != sk.Key {
		t.Errorf("Sum() reconstructed Key = %d, expected %d", recKey, sk.Key)
	}
	if recConflict != sk.Conflict {
		t.Errorf("Sum() reconstructed Conflict = %d, expected %d", recConflict, sk.Conflict)
	}

	if recKey != uint64(7321870291149185130) {
		t.Errorf("Sum() Key = %d, expected Key = %d", recKey, uint64(7321870291149185130))
	}
	if recConflict != uint64(17359760234500271770) {
		t.Errorf("Sum() Conflict = %d, expected Conflict = %d", recConflict, uint64(17359760234500271770))
	}
}
