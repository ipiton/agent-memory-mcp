package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// MultiHopRequest configures a graph-walk recall query (T50 slice 4).
//
// The pipeline is: semantic Recall(Query) → top-SeedK seed memories → build
// adjacency from their triples + their hop-K neighbours → weighted BFS with
// damping → aggregate per-memory score → return top-Limit memories with the
// chain of triples that earned each one its score.
//
// MaxHops bounds graph depth; effective values are 1-3. Default 2 keeps
// the result set tight and the walk cheap.
type MultiHopRequest struct {
	Query   string
	Limit   int      // max memories to return (default 10, capped at 100)
	MaxHops int      // BFS depth limit (default 2, capped at 4)
	SeedK   int      // how many memories drive seed entity selection (default 5, capped at 20)
	Filters Filters  // applied to the seed Recall step (e.g., context filter)
}

// MultiHopResult is one memory surfaced by RecallMultihop.
//
// Score aggregates damped hop weights across every triple that connects this
// memory to the seed set: a memory referenced by a 1-hop neighbour scores
// higher than one only reachable via 2 hops.
//
// Path is the shortest chain of triples that connected the seed to this
// memory (the same memory may have shorter alternate paths; we keep the one
// the BFS observed first, which by ordering is the cheapest hop count and
// — within that — the highest-weight edge).
type MultiHopResult struct {
	Memory *Memory  `json:"memory"`
	Score  float64  `json:"score"`
	Hops   int      `json:"hops"`
	Path   []Triple `json:"path,omitempty"`
}

// pprDamping is the per-hop weight decay: each additional graph step
// multiplies a result's contribution by this factor. 0.85 mirrors the
// PageRank convention and keeps the cost cheap for our adjacency size.
const pprDamping = 0.85

// RecallMultihop runs the graph-walk recall pipeline and returns memories
// ranked by aggregated PPR-style score. Returns an empty slice (no error) when
// no seeds match — callers can fall through to plain semantic Recall.
//
// Implementation notes:
//   - We build a fresh adjacency map per call. For < 100k triples this is
//     well under 50ms in practice and avoids cache invalidation footguns.
//     If a deployment outgrows this, the path forward is a long-lived
//     adjacency cached on Store and invalidated on AddTriples / DeleteTriples.
//   - "Weighted BFS with damping" is used in lieu of full power-iteration
//     PPR. For MaxHops <= 4 the two converge to the same ranking; this form
//     gives us a natural depth bound and per-result path tracking that PPR
//     would have to recover separately.
func (ms *Store) RecallMultihop(ctx context.Context, req MultiHopRequest) ([]*MultiHopResult, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return nil, fmt.Errorf("multihop: query is required")
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	if req.MaxHops <= 0 {
		req.MaxHops = 2
	}
	if req.MaxHops > 4 {
		req.MaxHops = 4
	}
	if req.SeedK <= 0 {
		req.SeedK = 5
	}
	if req.SeedK > 20 {
		req.SeedK = 20
	}

	// Step 1: semantic seeds via the existing Recall path. We over-fetch a
	// little (SeedK) so the BFS has multiple anchors; if Recall returns
	// nothing, the multihop query has no anchor in our memory and we
	// gracefully report zero results.
	seedHits, err := ms.Recall(ctx, req.Query, req.Filters, req.SeedK)
	if err != nil {
		return nil, fmt.Errorf("multihop seed recall: %w", err)
	}
	if len(seedHits) == 0 {
		return nil, nil
	}
	for _, h := range seedHits {
		if h != nil && h.Memory != nil {
			ms.logger.Debug("multihop seed", zap.String("id", h.Memory.ID), zap.String("title", h.Memory.Title), zap.Float64("score", h.Score))
		}
	}

	seedMemoryIDs := make(map[string]struct{}, len(seedHits))
	seedMemoryScore := make(map[string]float64, len(seedHits))
	for _, hit := range seedHits {
		if hit == nil || hit.Memory == nil {
			continue
		}
		seedMemoryIDs[hit.Memory.ID] = struct{}{}
		// Normalise initial seed score into [0, 1]. Recall scores are
		// often already cosine-like; clamp to be safe.
		s := hit.Score
		if s < 0 {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		seedMemoryScore[hit.Memory.ID] = s
	}

	// Step 2: collect every triple touching a seed memory; those entities
	// are our walk seeds. We also need every triple in the graph to
	// support multi-hop expansion, so we load the full corpus once.
	allTriples, err := ms.allTriples(ctx)
	if err != nil {
		return nil, fmt.Errorf("multihop load triples: %w", err)
	}
	if len(allTriples) == 0 {
		return nil, nil
	}

	// Build adjacency: per-entity outgoing edges.
	adj := buildEntityAdjacency(allTriples)

	// Seed entities: subjects and objects of triples whose memory is in
	// the seed set, weighted by the seed memory's recall score.
	seedEntities := map[string]float64{}
	for _, t := range allTriples {
		if _, ok := seedMemoryIDs[t.MemoryID]; !ok {
			continue
		}
		boost := seedMemoryScore[t.MemoryID]
		if boost == 0 {
			boost = 0.5 // floor so seeds without a Recall score still propagate
		}
		seedEntities[t.Subject] = max64(seedEntities[t.Subject], boost)
		seedEntities[t.Object] = max64(seedEntities[t.Object], boost)
	}
	if len(seedEntities) == 0 {
		// Seed memories exist but have no triples — graph layer is empty
		// for this corner of the corpus. Return seed memories as-is so
		// the caller still gets something useful.
		return resultsFromSeeds(seedHits, req.Limit), nil
	}

	// Step 3: weighted BFS with damping. We track the best (highest-score)
	// path that reaches every (entity, triple) pair we visit, so the path
	// in the final result is the one that earned it the most score.
	type visit struct {
		score float64
		hops  int
		path  []Triple
	}
	visited := map[string]visit{}
	for entity, score := range seedEntities {
		visited[entity] = visit{score: score, hops: 0}
	}

	// Frontier: BFS by hop. Each iteration walks all entities at the
	// current hop level, propagates damped scores to their neighbours.
	frontier := make(map[string]struct{}, len(seedEntities))
	for entity := range seedEntities {
		frontier[entity] = struct{}{}
	}

	// Snapshot per level: when iterating over frontier we must read the
	// entity's score/path AS THEY WERE ON LEVEL ENTRY, not as live
	// updates from within this level's inner loop. Without the snapshot,
	// an entity we update mid-level would feed its own (longer) path back
	// into the propagation and we'd walk past MaxHops in one iteration.
	for hop := 0; hop < req.MaxHops; hop++ {
		levelScore := make(map[string]float64, len(frontier))
		levelPath := make(map[string][]Triple, len(frontier))
		for entity := range frontier {
			snap := visited[entity]
			levelScore[entity] = snap.score
			levelPath[entity] = snap.path
		}

		next := map[string]struct{}{}
		for entity := range frontier {
			curScore := levelScore[entity]
			curPath := levelPath[entity]
			for _, edge := range adj[entity] {
				neighbour := edge.target
				newScore := curScore * pprDamping * edge.weight
				if newScore <= 0 {
					continue
				}
				// Hops is the BFS level we are about to add — never
				// derived from a possibly-stale visited entry.
				newHops := hop + 1
				existing, seen := visited[neighbour]
				if seen && existing.score >= newScore {
					continue
				}
				newPath := make([]Triple, 0, len(curPath)+1)
				newPath = append(newPath, curPath...)
				newPath = append(newPath, edge.triple)
				visited[neighbour] = visit{score: newScore, hops: newHops, path: newPath}
				next[neighbour] = struct{}{}
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}

	// Step 4: aggregate score per memory. A memory may be referenced by
	// multiple visited triples (different entities, different paths); we
	// sum the contributions and remember the path that gave the highest
	// single contribution.
	type memoryAgg struct {
		score    float64
		bestPath []Triple
		bestHop  int
	}
	perMemory := map[string]*memoryAgg{}
	for _, t := range allTriples {
		// The triple lifts a memory's relevance once for its subject and
		// once for its object end. We take the max so an entity that
		// appears on both ends of the edge isn't double-counted.
		subjVisit, subjOK := visited[t.Subject]
		objVisit, objOK := visited[t.Object]
		if !subjOK && !objOK {
			continue
		}
		var contrib float64
		var contribPath []Triple
		var contribHop int
		if subjOK && (!objOK || subjVisit.score >= objVisit.score) {
			contrib = subjVisit.score
			contribPath = subjVisit.path
			contribHop = subjVisit.hops
		} else {
			contrib = objVisit.score
			contribPath = objVisit.path
			contribHop = objVisit.hops
		}
		if contrib <= 0 {
			continue
		}
		agg, ok := perMemory[t.MemoryID]
		if !ok {
			agg = &memoryAgg{}
			perMemory[t.MemoryID] = agg
		}
		agg.score += contrib
		if contrib > 0 && (agg.bestPath == nil || contrib > peakScoreOfPath(agg.bestPath, agg.bestHop)) {
			agg.bestPath = contribPath
			agg.bestHop = contribHop
		}
	}

	// Step 5: sort and materialise top-Limit memories.
	type rankedID struct {
		id    string
		score float64
		hops  int
		path  []Triple
	}
	ranked := make([]rankedID, 0, len(perMemory))
	for id, agg := range perMemory {
		ranked = append(ranked, rankedID{id: id, score: agg.score, hops: agg.bestHop, path: agg.bestPath})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		// Tiebreak: fewer hops wins (closer to seed = more relevant).
		return ranked[i].hops < ranked[j].hops
	})
	if len(ranked) > req.Limit {
		ranked = ranked[:req.Limit]
	}

	// Round 3 H3: batch-load all top-K ids in one SQL roundtrip instead of
	// N separate Get calls. For limit=100 this drops 100 SELECTs to 1.
	ids := make([]string, len(ranked))
	for i, r := range ranked {
		ids[i] = r.id
	}
	loaded, err := ms.getBatch(ids)
	if err != nil {
		return nil, fmt.Errorf("multihop batch load: %w", err)
	}

	out := make([]*MultiHopResult, 0, len(ranked))
	for _, r := range ranked {
		mem, ok := loaded[r.id]
		if !ok {
			ms.logger.Debug("multihop: memory disappeared between recall and graph walk", zap.String("id", r.id))
			continue
		}
		out = append(out, &MultiHopResult{
			Memory: mem,
			Score:  r.score,
			Hops:   r.hops,
			Path:   r.path,
		})
	}
	return out, nil
}

// allTriples loads the full triple corpus. For our scale this is acceptable
// per call; see the godoc on RecallMultihop for the cache strategy if a
// deployment outgrows it.
func (ms *Store) allTriples(ctx context.Context) ([]Triple, error) {
	return ms.queryTriples(ctx, `
		SELECT id, subj, rel, obj, memory_id, link_type, weight, created_at
		FROM memory_triples
	`)
}

// directedEdge is a single outgoing graph edge, carrying its source triple
// for path reconstruction.
type directedEdge struct {
	target string
	weight float64
	triple Triple
}

// buildEntityAdjacency materialises an undirected adjacency map: for each
// entity (subj or obj) we list the triples that touch it and the
// counterpart entity. This lets the BFS walk in either direction; for
// engineering memory graphs the relation direction is rarely strict
// (semantic edges go both ways for retrieval purposes).
func buildEntityAdjacency(triples []Triple) map[string][]directedEdge {
	adj := make(map[string][]directedEdge, len(triples)*2)
	for _, t := range triples {
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		adj[t.Subject] = append(adj[t.Subject], directedEdge{target: t.Object, weight: w, triple: t})
		adj[t.Object] = append(adj[t.Object], directedEdge{target: t.Subject, weight: w, triple: t})
	}
	return adj
}

// resultsFromSeeds materialises a degraded-mode result list when seeds
// matched but the graph layer is empty. Returns plain memories with score
// equal to seed Recall score and zero hops.
func resultsFromSeeds(seedHits []*SearchResult, limit int) []*MultiHopResult {
	out := make([]*MultiHopResult, 0, len(seedHits))
	for _, hit := range seedHits {
		if hit == nil || hit.Memory == nil {
			continue
		}
		out = append(out, &MultiHopResult{Memory: hit.Memory, Score: hit.Score, Hops: 0})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// peakScoreOfPath returns a stable proxy for "how strong is this path":
// since path stores raw triples we approximate by the damping factor at the
// path's hop depth. Used only as a tiebreak inside aggregation, not exposed.
func peakScoreOfPath(path []Triple, hops int) float64 {
	if len(path) == 0 {
		return 1
	}
	score := 1.0
	for i := 0; i < hops; i++ {
		score *= pprDamping
	}
	return score
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
