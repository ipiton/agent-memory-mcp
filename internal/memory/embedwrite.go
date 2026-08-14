package memory

import (
	"context"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/textfmt"
	"go.uber.org/zap"
)

// T120: a record whose body the encoder refuses is stored without a vector and
// confirmed as if nothing happened — Store's read-after-write veto inspects the
// row, not the vector, and Recall never reaches the cosine because the model id
// does not match. Eight such records were in the live bank, bodies from 3.7 KB
// to 102 KB.
//
// The refusal is not about context length. llama-server rejects an input that
// exceeds the *physical batch* (-ub): at -ub 512 records from 792 tokens up
// failed, at -ub 2048 only those eight did. The diagnosis ("increase the
// physical batch size") appears only in the server's own log; our side sees an
// ordinary provider error.
//
// So a refusal now costs a second attempt on a rune-safe prefix, and the record
// carries a mark saying so. A partial vector beats no vector — a 102 KB session
// summary encoded from its opening is at least reachable — but only if the
// partiality is visible, which is what MetadataEmbeddingTruncated is for.

// embedRetryRunes is the prefix length used for the retry. Sized against the
// physical batch the local encoder runs with (-ub 2048 tokens) at roughly 2.5
// characters per token on Russian text, with margin. Not configurable: it is a
// property of the failure mode, not a preference, and a knob here would be one
// more thing to set wrong.
const embedRetryRunes = 4000

// embedForWrite encodes text, retrying once on a rune-safe prefix when the
// provider refuses the whole body. The bool reports whether the result came
// from the truncated retry.
func (ms *Store) embedForWrite(ctx context.Context, id, text string) (*embedder.EmbeddingResult, bool, error) {
	result, err := ms.embedder.EmbedDetailed(ctx, text)
	if err == nil {
		return result, false, nil
	}

	runes := utf8.RuneCountInString(text)
	if runes <= embedRetryRunes {
		ms.logger.Warn("Encoder refused a record; it will be invisible to semantic recall",
			zap.String("id", id),
			zap.Int("bytes", len(text)),
			zap.Int("runes", runes),
			zap.Error(err),
		)
		return nil, false, err
	}

	prefix := textfmt.Truncate(text, embedRetryRunes)
	retried, retryErr := ms.embedder.EmbedDetailed(ctx, prefix)
	if retryErr != nil {
		ms.logger.Warn("Encoder refused a record whole and truncated; it will be invisible to semantic recall",
			zap.String("id", id),
			zap.Int("bytes", len(text)),
			zap.Int("runes", runes),
			zap.Int("retry_runes", embedRetryRunes),
			zap.Error(retryErr),
		)
		return nil, false, retryErr
	}

	ms.logger.Warn("Encoder refused a record whole; embedded its opening instead",
		zap.String("id", id),
		zap.Int("runes", runes),
		zap.Int("embedded_runes", embedRetryRunes),
		zap.Error(err),
	)
	return retried, true, nil
}

// markEmbeddingTruncated records that the vector covers only the opening of the
// body, so the partiality is queryable rather than folklore.
func markEmbeddingTruncated(m *Memory) {
	if m.Metadata == nil {
		m.Metadata = make(map[string]string)
	}
	m.Metadata[MetadataEmbeddingTruncated] = "true"
}
