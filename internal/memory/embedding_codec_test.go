package memory

import (
	"encoding/binary"
	"math"
	"testing"
)

// T109. The blob format was sniffed from a single byte, so a binary embedding
// whose first float happened to have 0x5B ('[') as its low byte was routed to
// the JSON parser and lost. The record still loaded — only its embedding was
// dropped — which is why this cost nothing visible for months while the
// affected memories quietly stopped appearing in semantic recall.
func TestEmbeddingRoundTripSurvivesJSONLookalikeBytes(t *testing.T) {
	// A float32 whose little-endian encoding starts with 0x5B. Any value with
	// that low byte will do; this one is a plausible embedding component.
	var bits uint32
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(0.1))
	buf[0] = '['
	bits = binary.LittleEndian.Uint32(buf)
	leading := math.Float32frombits(bits)

	original := []float32{leading, -0.25, 0.5, 1}
	blob := marshalEmbeddingBinary(original)
	if blob[0] != '[' {
		t.Fatalf("test setup: blob does not start with '[' (got %#x)", blob[0])
	}

	got, err := unmarshalEmbeddingBinary(blob)
	if err != nil {
		t.Fatalf("decode failed for a blob that merely looks like JSON: %v", err)
	}
	if len(got) != len(original) {
		t.Fatalf("decoded %d values, want %d", len(got), len(original))
	}
	for i := range original {
		if got[i] != original[i] {
			t.Errorf("value %d = %v, want %v", i, got[i], original[i])
		}
	}
}

// The legacy JSON format must keep decoding — the fallback reordering must not
// have turned real JSON into a misparsed float array.
func TestEmbeddingLegacyJSONStillDecodes(t *testing.T) {
	got, err := unmarshalEmbeddingBinary([]byte("[0.5,-1.5,2]"))
	if err != nil {
		t.Fatalf("legacy JSON decode: %v", err)
	}
	want := []float32{0.5, -1.5, 2}
	if len(got) != len(want) {
		t.Fatalf("decoded %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// A genuinely corrupt blob must still be reported rather than reinterpreted:
// the point of the change is to stop misclassifying valid data, not to make
// every input decode into something.
func TestEmbeddingRejectsMisalignedBlob(t *testing.T) {
	if _, err := unmarshalEmbeddingBinary([]byte{'[', 0x01, 0x02}); err == nil {
		t.Fatal("a 3-byte blob decoded without error; want a size complaint")
	}
}
