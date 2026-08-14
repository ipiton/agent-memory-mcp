package memory

import (
	"errors"
	"fmt"
)

// ErrRetrievalDegraded marks a read that strict mode refuses rather than
// serving in reduced form. Mirrors rag.ErrRetrievalDegraded — the two packages
// do not import each other, and one shared sentinel would mean one importing
// the other for a single value.
//
// T99.
var ErrRetrievalDegraded = errors.New("retrieval degraded")

// StrictRetrievalError builds the refusal, naming the stage and the way out.
func StrictRetrievalError(stage, detail string) error {
	return fmt.Errorf("%w at %s: %s (MCP_RETRIEVAL_STRICT is on — unset it to allow the fallback)",
		ErrRetrievalDegraded, stage, detail)
}

// SetRetrievalStrict toggles strict retrieval. Set once at startup from
// config.Config.RetrievalStrict; retrieval reads the atomic without a lock.
func (ms *Store) SetRetrievalStrict(strict bool) {
	ms.retrievalStrict.Store(strict)
}
