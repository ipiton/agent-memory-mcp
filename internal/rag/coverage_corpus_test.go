//go:build corpus

// Checks the coverage attribution against the live index (T97).
//
// The unit tests build a three-file corpus; this one runs the same attribution
// over the real vectors.db, where the numbers can be checked independently with
// SQL. Excluded from the default build because it needs a database that is not
// in the repository. Point it at a copy, never the live file:
//
//	cp /opt/homebrew/var/agent-memory-mcp/rag-index/vectors.db /tmp/vectors.db
//	go test -tags=corpus ./internal/rag/ -run TestCoverageAgainstLiveIndex -v \
//	    -index-db /tmp/vectors.db -index-roots docs,tasks/archive,memory-bank
package rag

import (
	"database/sql"
	"flag"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

var (
	indexDB    = flag.String("index-db", "", "path to a read-only copy of a vectors.db snapshot")
	indexRoots = flag.String("index-roots", "docs", "comma-separated MCP_INDEX_DIRS value to attribute against")
)

func TestCoverageAgainstLiveIndex(t *testing.T) {
	if *indexDB == "" {
		t.Skip("no -index-db given")
	}

	db, err := sql.Open("sqlite", "file:"+*indexDB+"?mode=ro")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("SELECT file_path, chunk_count FROM indexed_files")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	roots := strings.Split(*indexRoots, ",")
	files := map[string]int{}
	chunks := map[string]int{}
	other := 0

	for rows.Next() {
		var path string
		var n int
		if err := rows.Scan(&path, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		best, bestLen := "", -1
		for _, root := range roots {
			root = strings.Trim(strings.TrimSpace(root), "/")
			if root == "" {
				continue
			}
			if matchesRootPrefix(path, root) && len(root) > bestLen {
				best, bestLen = root, len(root)
			}
		}
		if best == "" {
			other++
			continue
		}
		files[best]++
		chunks[best] += n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for _, root := range roots {
		root = strings.Trim(strings.TrimSpace(root), "/")
		t.Logf("%s — %d files, %d chunks", root, files[root], chunks[root])
	}
	t.Logf("outside the configured roots: %d files", other)

	// Every indexed file must land somewhere. A file the attribution cannot
	// place is the same blind spot in a different disguise.
	if other > 0 {
		t.Errorf("%d indexed files matched no configured root — attribution or config is wrong", other)
	}
}
