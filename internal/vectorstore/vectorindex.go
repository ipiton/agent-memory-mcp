package vectorstore

import (
	"encoding/binary"
	"math"
	"slices"
	"time"
)

// vectorIndex keeps every vector of the store in one contiguous []float32
// (T164/T177). Before it, each chunk was a *Chunk in a map with its own
// embedding slice: 115 thousand small allocations the collector had to walk,
// map overhead on top of them, and a decoded copy of every blob left behind as
// garbage by the load. One array is a single pointer-free allocation, scanned
// in memory order by the ranking loop.
//
// Slot i owns vecs[i*dim:(i+1)*dim] and meta[i]. Removal moves the last slot
// into the hole, so the order of slots means nothing.
type vectorIndex struct {
	dim  int
	vecs []float32
	meta []chunkMeta
	pos  map[string]int
}

// chunkMeta is the identity of a chunk a search result needs besides the text.
type chunkMeta struct {
	ID           string
	DocPath      string
	Title        string
	LastModified time.Time
}

func newVectorIndex(dim, capacity int) *vectorIndex {
	return &vectorIndex{
		dim:  dim,
		vecs: make([]float32, 0, capacity*dim),
		meta: make([]chunkMeta, 0, capacity),
		pos:  make(map[string]int, capacity),
	}
}

func (x *vectorIndex) len() int { return len(x.meta) }

func (x *vectorIndex) vector(i int) []float32 {
	return x.vecs[i*x.dim : (i+1)*x.dim : (i+1)*x.dim]
}

// put stores the vector under meta.ID, replacing an earlier one. A vector of
// the wrong length removes the chunk instead: it could never be compared.
func (x *vectorIndex) put(meta chunkMeta, embedding []float32) {
	if len(embedding) != x.dim {
		x.remove(meta.ID)
		return
	}
	if i, ok := x.pos[meta.ID]; ok {
		copy(x.vector(i), embedding)
		x.meta[i] = meta
		return
	}
	x.pos[meta.ID] = len(x.meta)
	x.meta = append(x.meta, meta)
	x.vecs = append(x.vecs, embedding...)
}

func (x *vectorIndex) remove(id string) {
	i, ok := x.pos[id]
	if !ok {
		return
	}
	last := len(x.meta) - 1
	if i != last {
		copy(x.vector(i), x.vector(last))
		x.meta[i] = x.meta[last]
		x.pos[x.meta[i].ID] = i
	}
	x.meta[last] = chunkMeta{}
	x.meta = x.meta[:last]
	x.vecs = x.vecs[:last*x.dim]
	delete(x.pos, id)
}

// appendBlob decodes a stored embedding straight into the array, without the
// intermediate slice dbutil.DecodeEmbedding allocates. It reports false for a
// blob that does not hold exactly dim float32 values. Legacy JSON blobs ('['
// first) go through the caller's slow path.
func (x *vectorIndex) appendBlob(meta chunkMeta, blob []byte) bool {
	if len(blob) != x.dim*4 {
		return false
	}
	if _, dup := x.pos[meta.ID]; dup {
		return false
	}
	start := len(x.vecs)
	x.vecs = slices.Grow(x.vecs, x.dim)[:start+x.dim]
	dst := x.vecs[start:]
	for j := range dst {
		dst[j] = math.Float32frombits(binary.LittleEndian.Uint32(blob[j*4:]))
	}
	x.pos[meta.ID] = len(x.meta)
	x.meta = append(x.meta, meta)
	return true
}
