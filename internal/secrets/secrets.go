// Package secrets finds credentials in text before it is stored. Text that goes
// to a store may also go to a remote embedding provider, so the check belongs at
// the write boundary, ahead of the embedding call.
package secrets

import (
	"regexp"
	"strings"
)

type provider struct {
	name    string
	pattern *regexp.Regexp
}

// providers match by a documented prefix, so a hit is almost never a false
// positive: this is what justifies refusing the write outright. Order matters
// where one prefix contains another (sk-ant- before sk-).
var providers = []provider{
	{"private key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abeoprs]-[A-Za-z0-9-]{10,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}`)},
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
}

var bearerPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s]+)`)

// documentAssignmentPattern takes the rest of the line: an indexed document is
// public-facing reference text, where over-masking a config line costs little.
var documentAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|secret|token|password|passwd|client_secret|access_key)\b\s*[:=]\s*)(.+)$`)

// noteAssignmentPattern is narrower. A note is prose, where "token: строка в
// заголовке" is a sentence and not a credential, and masking it would silently
// destroy what the agent wrote. A value is one word of 8+ characters that is
// not a placeholder (${TOKEN}, <token>, {{ token }}).
var noteAssignmentPattern = regexp.MustCompile(`(?i)((?:[a-z0-9_-]*(?:api[_-]?key|secret|token|password|passwd|access[_-]?key))\s*[:=]\s*["']?)([^\s"'$<{][^\s"']{7,})`)

// FindProviderToken names the first provider whose token occurs in text, or
// returns "". It never returns the value, so the result is safe to log.
func FindProviderToken(text string) string {
	for _, p := range providers {
		if p.pattern.MatchString(text) {
			return p.name
		}
	}
	return ""
}

// MaskProviderTokens replaces provider tokens with a marker naming the provider.
func MaskProviderTokens(text string) string {
	for _, p := range providers {
		text = p.pattern.ReplaceAllString(text, "[REDACTED "+strings.ToUpper(p.name)+"]")
	}
	return text
}

// RedactNote masks credential-shaped assignments in free-form text.
func RedactNote(text string) string {
	text = bearerPattern.ReplaceAllString(text, "${1}[REDACTED]")
	return noteAssignmentPattern.ReplaceAllString(text, "${1}[REDACTED]")
}

// RedactDocument masks everything secret-looking in a document being indexed.
func RedactDocument(content string) string {
	lines := strings.Split(MaskProviderTokens(content), "\n")
	for i, line := range lines {
		line = bearerPattern.ReplaceAllString(line, "${1}[REDACTED]")
		lines[i] = documentAssignmentPattern.ReplaceAllString(line, "${1}[REDACTED]")
	}
	return strings.Join(lines, "\n")
}
