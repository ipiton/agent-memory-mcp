//go:build corpus

// Diagnostic for the load-error counter (T109).
//
// memory_stats reported a steady "20 load errors" for months and nobody had
// looked at what they were. The counter is deliberately soft — a row that fails
// to decode one field is still cached, so the service works and the number just
// sits there. This walks the same rows the cache loader walks and names the
// failing field per record, which the counter by itself cannot do.
//
//	go test -tags=corpus ./internal/memory/ -run TestLoadErrorCorpus -v -corpus-db /tmp/snap.db
package memory

import (
	"database/sql"
	"encoding/json"
	"flag"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/dbutil"
)

var corpusDB = flag.String("corpus-db", "", "path to a read-only copy of a memories.db snapshot")

func TestLoadErrorCorpus(t *testing.T) {
	if *corpusDB == "" {
		t.Skip("no -corpus-db given")
	}

	store, err := NewStore(*corpusDB, nil, nil)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Logf("LoadErrors() = %d", store.LoadErrors())

	// Re-walk the rows attributing each soft failure to a field.
	rows, err := store.db.Query("SELECT id, tags, embedding, content, title FROM memories")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	tagsFailed, embFailed := 0, 0
	for rows.Next() {
		var id string
		var tagsJSON sql.NullString
		var blob []byte
		var content, title sql.NullString
		if err := rows.Scan(&id, &tagsJSON, &blob, &content, &title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if tagsJSON.Valid && tagsJSON.String != "" {
			var tags []string
			if err := json.Unmarshal([]byte(tagsJSON.String), &tags); err != nil {
				tagsFailed++
				if tagsFailed <= 25 {
					t.Logf("tags decode failed: %s → %q (%v)", id, truncateForLog(tagsJSON.String), err)
				}
			}
		}
		if len(blob) > 0 {
			if _, err := dbutil.DecodeEmbedding(blob); err != nil {
				embFailed++
				if embFailed <= 25 {
					t.Logf("embedding decode failed: %s → %d bytes (%v)", id, len(blob), err)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	t.Logf("tags decode failures: %d, embedding decode failures: %d", tagsFailed, embFailed)
}

func truncateForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
