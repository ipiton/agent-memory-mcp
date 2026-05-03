package memory

import (
	"context"
	"sort"
	"time"
)

// StaleDeadEnd surfaces a dead_end memory that has been silent for longer
// than the hygiene threshold and is therefore a candidate for re-evaluation:
// the constraint that originally killed the approach may no longer apply
// (library upgraded, infrastructure changed, decision superseded). Callers
// receive both the memory and Age so they can prioritise by absolute age,
// not just sort order.
type StaleDeadEnd struct {
	Memory *Memory       `json:"memory"`
	Age    time.Duration `json:"age"`
}

// StaleDeadEnds returns dead_end memories whose age (now − CreatedAt) is at
// least olderThan. Results are sorted by age descending — oldest first —
// so a hygiene CLI prints the highest-priority candidates at the top.
//
// The cutoff is intentionally a single threshold rather than a half-life
// curve: dead_end staleness is a binary "should we re-check this?" signal,
// not a relevance score. Callers that want a multi-band view can re-call
// with successive thresholds.
//
// olderThan ≤ 0 returns every dead_end currently in the store, useful when
// a CLI wants to show ALL dead_ends with their ages and let the operator
// pick visually.
func (ms *Store) StaleDeadEnds(ctx context.Context, olderThan time.Duration) ([]*StaleDeadEnd, error) {
	all, err := ms.List(ctx, Filters{}, 0)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var out []*StaleDeadEnd
	for _, mem := range all {
		if mem == nil {
			continue
		}
		if EngineeringTypeOf(mem) != EngineeringTypeDeadEnd {
			continue
		}
		age := now.Sub(mem.CreatedAt)
		if olderThan > 0 && age < olderThan {
			continue
		}
		out = append(out, &StaleDeadEnd{Memory: mem, Age: age})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Age > out[j].Age
	})
	return out, nil
}
