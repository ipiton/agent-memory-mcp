package dbutil

import (
	"encoding/binary"
	"math"
	"testing"
)

// T109. The blob format was sniffed from a single byte, so a binary embedding
// whose first float happened to have 0x5B ('[') as its low byte was routed to
// the JSON parser and lost. The row still loaded — only its embedding was
// dropped — which is why this cost nothing visible for months while the
// affected records quietly stopped appearing in semantic recall.
func TestEmbeddingRoundTripSurvivesJSONLookalikeBytes(t *testing.T) {
	original := []float32{floatWithLowByte(0.1, '['), -0.25, 0.5, 1}
	blob := EncodeEmbedding(original)
	if blob[0] != '[' {
		t.Fatalf("test setup: blob does not start with '[' (got %#x)", blob[0])
	}

	got, err := DecodeEmbedding(blob)
	if err != nil {
		t.Fatalf("decode failed for a blob that merely looks like JSON: %v", err)
	}
	assertEmbedding(t, got, original)
}

// The 1-in-65536 case the closing byte cannot rule out: a binary blob that
// both starts with '[' and ends with ']'. It reaches json.Unmarshal, fails
// there, and must come back through the binary path rather than as an error.
func TestEmbeddingRoundTripSurvivesBracketsAtBothEnds(t *testing.T) {
	original := []float32{
		floatWithLowByte(0.1, '['),
		-0.25,
		math.Float32frombits(0x5D2A3B4C), // high byte 0x5D (']')
	}
	blob := EncodeEmbedding(original)
	if blob[0] != '[' || blob[len(blob)-1] != ']' {
		t.Fatalf("test setup: blob is %#x…%#x, want '['…']'", blob[0], blob[len(blob)-1])
	}

	got, err := DecodeEmbedding(blob)
	if err != nil {
		t.Fatalf("decode failed for a blob bracketed at both ends: %v", err)
	}
	assertEmbedding(t, got, original)
}

// The legacy JSON format must keep decoding — the fallback ordering must not
// have turned real JSON into a misparsed float array.
func TestEmbeddingLegacyJSONStillDecodes(t *testing.T) {
	got, err := DecodeEmbedding([]byte("[0.5,-1.5,2]"))
	if err != nil {
		t.Fatalf("legacy JSON decode: %v", err)
	}
	assertEmbedding(t, got, []float32{0.5, -1.5, 2})
}

// A genuinely corrupt blob must still be reported rather than reinterpreted:
// the point of the change is to stop misclassifying valid data, not to make
// every input decode into something.
func TestEmbeddingRejectsMisalignedBlob(t *testing.T) {
	if _, err := DecodeEmbedding([]byte{'[', 0x01, 0x02}); err == nil {
		t.Fatal("a 3-byte blob decoded without error; want a size complaint")
	}
}

// An absent embedding is a row state, not a decode failure.
func TestEmbeddingEmptyBlobDecodesToNothing(t *testing.T) {
	got, err := DecodeEmbedding(nil)
	if err != nil {
		t.Fatalf("empty blob: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
	if EncodeEmbedding(nil) != nil {
		t.Error("an empty embedding must encode to nil so it stores as NULL")
	}
}

// floatWithLowByte returns a float32 close to want whose little-endian
// encoding starts with the given byte.
func floatWithLowByte(want float32, low byte) float32 {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(want))
	buf[0] = low
	return math.Float32frombits(binary.LittleEndian.Uint32(buf))
}

func assertEmbedding(t *testing.T, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("decoded %d values, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value %d = %v, want %v", i, got[i], want[i])
		}
	}
}
