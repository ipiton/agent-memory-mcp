package dbutil

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// Embedding blobs are stored the same way by the memory store and the vector
// store, so the codec lives here rather than once per package.

// EncodeEmbedding serializes a float32 slice as little-endian binary,
// 4 bytes per dimension. An empty embedding encodes to nil, which stores
// as SQL NULL rather than an empty blob.
func EncodeEmbedding(embedding []float32) []byte {
	if len(embedding) == 0 {
		return nil
	}
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// DecodeEmbedding deserializes an embedding blob written by EncodeEmbedding,
// falling back to the legacy JSON array format. An empty blob decodes to no
// embedding, not an error: a row is allowed to carry none.
//
// T109: the format used to be decided by one byte — blob[0] == '[' meant the
// legacy JSON array, anything else meant binary. A binary blob is 4 bytes per
// dimension of arbitrary float bits, so roughly one in 256 begins with 0x5B
// ('[') by chance and was handed to the JSON parser, which failed. Both stores
// hit it, months apart, with the same symptom: the row still loaded, but
// without its embedding, so it silently dropped out of semantic recall.
// Measured at 21 of 4536 memory blobs and 83 of 20565 vector chunks — both
// within noise of the 0.39% the birthday math predicts.
//
// The prefix is a hint, not a verdict: JSON has to actually parse, and a blob
// that merely starts like it falls through to the binary path it belonged to
// all along. Nothing about the stored format changes, so affected rows recover
// on the next load without a re-embed.
func DecodeEmbedding(blob []byte) ([]float32, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	if blob[0] == '[' {
		var embedding []float32
		if err := json.Unmarshal(blob, &embedding); err == nil {
			return embedding, nil
		}
	}
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding blob size %d (must be multiple of 4)", len(blob))
	}
	embedding := make([]float32, len(blob)/4)
	for i := range embedding {
		embedding[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return embedding, nil
}
