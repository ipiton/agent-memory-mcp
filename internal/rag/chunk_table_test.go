package rag

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// T143: a table longer than the budget used to be cut on the nearest
// whitespace, so the second and later pieces began mid-row and carried rows
// with no header saying which number is the amount and which the date.

const (
	tableHeader    = "| Рачун | Поставщик | Сумма EUR | Срок оплаты | Статус |"
	tableDelimiter = "|---|---|---:|---|---|"
)

// tableRows returns n rows with Cyrillic cells, so a cut on a byte grid would
// land mid-rune as well as mid-row (the T118 regression on the same splitter).
func tableRows(n int) []string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf("| %d | ООО «Поставщик номер %d» | %d | 2026-%02d-%02d | оплачено |",
			6100+i, i, 1000+i*37, i%12+1, i%28+1)
	}
	return rows
}

// checkTableChunks asserts the T143 contract on chunks cut from a document
// holding one table: every piece that carries table rows opens them with the
// header and the delimiter row, every row line is a whole original row, every
// row lands in some piece, no piece exceeds chunkSize, and all are valid UTF-8.
func checkTableChunks(t *testing.T, chunks []string, rows []string, chunkSize int) {
	t.Helper()
	whole := make(map[string]bool, len(rows))
	for _, r := range rows {
		whole[r] = true
	}
	seen := make(map[string]bool, len(rows))
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
		if len(c) > chunkSize {
			t.Errorf("chunk %d is %d bytes, over the %d budget", i, len(c), chunkSize)
		}
		lines := strings.Split(c, "\n")
		first := -1
		for j, l := range lines {
			if strings.HasPrefix(l, "|") {
				first = j
				break
			}
		}
		if first < 0 {
			continue
		}
		if lines[first] != tableHeader || first+1 >= len(lines) || lines[first+1] != tableDelimiter {
			t.Errorf("chunk %d: table rows without the header in front:\n%s", i, c)
			continue
		}
		for _, l := range lines[first+2:] {
			if !strings.HasPrefix(l, "|") {
				continue
			}
			if !whole[l] {
				t.Errorf("chunk %d carries a broken row: %q", i, l)
			}
			seen[l] = true
		}
	}
	for _, r := range rows {
		if !seen[r] {
			t.Errorf("row lost from every chunk: %q", r)
		}
	}
}

func TestSplitMarkdownRepeatsTableHeaderInEveryChunk(t *testing.T) {
	rows := tableRows(60)
	// An odd-length prose lead puts the table off any round byte offset.
	doc := "# Рачуны 2026\n\nx Реестр входящих счетов за год.\n\n" +
		tableHeader + "\n" + tableDelimiter + "\n" + strings.Join(rows, "\n") +
		"\n\nПосле таблицы — короткая приписка о сверке.\n"

	for _, size := range []int{700, 1000, 2000} {
		for _, overlap := range []int{0, 90, 200} {
			t.Run(fmt.Sprintf("size=%d/overlap=%d", size, overlap), func(t *testing.T) {
				chunks := splitMarkdownWithBreadcrumbs(doc, "Рачуны", size, overlap, false)
				if len(chunks) < 3 {
					t.Fatalf("expected the table to span several chunks, got %d", len(chunks))
				}
				// The breadcrumb budget leaves out the "\n\n" joining the
				// breadcrumb to the piece — two bytes of slack that predate T143.
				checkTableChunks(t, chunks, rows, size+2)
			})
		}
	}
}

// A piece whose overlap reaches back into the header must not cut between the
// header and the delimiter row, nor carry the header twice.
func TestSplitMarkdownBodyTableRightAfterProse(t *testing.T) {
	rows := tableRows(40)
	body := strings.Repeat("Вводный абзац перед таблицей. ", 20) + "\n" +
		tableHeader + "\n" + tableDelimiter + "\n" + strings.Join(rows, "\n")

	for _, budget := range []int{300, 640, 1100} {
		chunks := splitMarkdownBodyByBudget(body, budget, 250)
		checkTableChunks(t, chunks, rows, budget)
		for i, c := range chunks {
			if n := strings.Count(c, tableHeader); n > 1 {
				t.Errorf("budget=%d chunk %d carries the header %d times", budget, i, n)
			}
		}
	}
}

// Without a table the Markdown body goes through the plain splitter, so the
// chunks of every table-free section stay byte-identical to the old index and
// their embeddings stay reusable.
func TestSplitMarkdownBodyWithoutTableUnchanged(t *testing.T) {
	prose := strings.Repeat("alpha | beta gamma delta ", 120)
	fenced := "```\n" + tableHeader + "\n" + tableDelimiter + "\n" +
		strings.Join(tableRows(30), "\n") + "\n```\n" + prose

	for name, body := range map[string]string{"prose with pipes": prose, "table in code fence": fenced} {
		got := splitMarkdownBodyByBudget(body, 400, 60)
		want := splitTextByBudget(body, 400, 60)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: chunks differ from splitTextByBudget", name)
		}
	}
}

func TestIsTableDelimiterRow(t *testing.T) {
	for line, want := range map[string]bool{
		"|---|---|":         true,
		"| :--- | ---: |":   true,
		"|:-:|":             true,
		"--- | ---":         true,
		"  |---|---|  ":     true,
		"---":               false, // a thematic break, not a table
		"| a | b |":         false,
		"|---|abc|":         false,
		"||":                false,
		"| -- x -- | --- |": false,
	} {
		if got := isTableDelimiterRow(line); got != want {
			t.Errorf("isTableDelimiterRow(%q) = %v, want %v", line, got, want)
		}
	}
}
