package config

import (
	"reflect"
	"testing"
)

func TestParseArchiveRoots_DeduplicatesPreservingOrder(t *testing.T) {
	// Absolute paths, with duplicates scattered and mixed with extra spaces.
	raw := "/a/b:/c/d:/a/b: :/c/d:/e/f"
	got := parseArchiveRoots(raw, "/root")
	want := []string{"/a/b", "/c/d", "/e/f"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseArchiveRoots dedup failed\n got=%v\nwant=%v", got, want)
	}
}

func TestParseArchiveRoots_EmptyAndWhitespaceOnly(t *testing.T) {
	if got := parseArchiveRoots("", "/root"); got != nil {
		t.Fatalf("empty string should return nil, got %v", got)
	}
	if got := parseArchiveRoots("   ", "/root"); got != nil {
		t.Fatalf("whitespace-only should return nil, got %v", got)
	}
	if got := parseArchiveRoots(": : :", "/root"); got != nil {
		t.Fatalf("separator-only should return nil, got %v", got)
	}
}

func TestParseArchiveRoots_RelativeJoinedToRoot(t *testing.T) {
	got := parseArchiveRoots("archive:/abs/path:archive", "/root")
	want := []string{"/root/archive", "/abs/path"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relative-join dedup failed\n got=%v\nwant=%v", got, want)
	}
}
