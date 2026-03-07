package scoring

import (
	"strings"
	"time"
	"unicode"
)

func ContainsAny(value string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

// TokenizeWords splits text into words at non-alphanumeric boundaries.
// Returns lowercased tokens consisting only of letters and digits.
func TokenizeWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func FreshnessScore(lastVerifiedAt time.Time, now time.Time) float64 {
	if lastVerifiedAt.IsZero() {
		return 0.20
	}

	age := now.Sub(lastVerifiedAt)
	switch {
	case age <= 7*24*time.Hour:
		return 1.00
	case age <= 30*24*time.Hour:
		return 0.80
	case age <= 90*24*time.Hour:
		return 0.60
	case age <= 180*24*time.Hour:
		return 0.35
	default:
		return 0.15
	}
}
