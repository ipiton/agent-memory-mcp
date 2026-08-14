package rag

import (
	"errors"
	"fmt"
)

// ErrRetrievalDegraded marks every failure that strict mode turns from a
// quieter answer into a refusal. Callers match on it with errors.Is; the
// message names the stage and how to switch the mode off.
//
// T99: without this the only way to learn that a provider had fallen over was
// to read the log of a process you are not watching. The point of strict mode
// is that the failure arrives where the query did.
var ErrRetrievalDegraded = errors.New("retrieval degraded")

// StrictModeError builds the refusal for one degradation, naming the stage and
// the way out. The message has to carry both: an operator who sees only "strict
// mode" learns nothing about what actually broke, and one who sees only the
// breakage has no idea a mode is enforcing it.
func StrictModeError(stage, detail string) error {
	return fmt.Errorf("%w at %s: %s (MCP_RETRIEVAL_STRICT is on — unset it to allow the fallback)",
		ErrRetrievalDegraded, stage, detail)
}
