package secrets

import (
	"strings"
	"testing"
)

// Fixtures are assembled at run time so the repository itself never contains a
// string a secret scanner would flag.
func tokens() map[string]string {
	alnum36 := strings.Repeat("a1B2", 9)
	return map[string]string{
		"GitHub token":   "gh" + "p_" + alnum36,
		"GitLab token":   "gl" + "pat-" + alnum36[:20],
		"AWS access key": "AK" + "IA" + "ABCDEFGHIJKLMNOP",
		"Slack token":    "xo" + "xb-" + "1234567890-abcdef",
		"Google API key": "AI" + "za" + strings.Repeat("x1", 17) + "y",
		"Anthropic key":  "sk-" + "ant-" + alnum36,
		"API key":        "sk-" + alnum36,
		"JWT":            "ey" + "JhbGciOiJIUzI1.ey" + "JzdWIiOiIxMjM0.abcdefghijkl",
		"private key":    "-----BEGIN RSA " + "PRIVATE KEY-----\nbody\n-----END RSA " + "PRIVATE KEY-----",
	}
}

func TestFindAndMaskProviderTokens(t *testing.T) {
	for want, token := range tokens() {
		text := "before " + token + " after"
		if got := FindProviderToken(text); got != want {
			t.Errorf("FindProviderToken(%s) = %q", want, got)
		}
		masked := MaskProviderTokens(text)
		if strings.Contains(masked, token) || !strings.Contains(masked, "[REDACTED") ||
			!strings.HasPrefix(masked, "before ") || !strings.HasSuffix(masked, " after") {
			t.Errorf("MaskProviderTokens(%s) = %q", want, masked)
		}
	}
}

// A hit refuses the write, so these must stay clean: a commit SHA, a UUID, and
// words that merely begin like a prefix.
func TestFindProviderTokenIgnoresLookalikes(t *testing.T) {
	for _, text := range []string{
		"commit 105a784e9d3f4c1a2b6d7e8f9a0b1c2d3e4f5a6b",
		"id 3cfd60c7-2050-4ec1-a61d-99ce294a0d05",
		"sk-short and risk-management-framework-review",
		"AKIA is the AWS prefix",
		"eyJ alone is not a JWT",
	} {
		if got := FindProviderToken(text); got != "" {
			t.Errorf("FindProviderToken(%q) = %q, want none", text, got)
		}
	}
}

func TestRedactNote(t *testing.T) {
	masked := []string{
		"API_TOKEN=abcdef123456",
		`password: "correct-horse-battery"`,
		"client_secret = 0123456789abcdef",
		"Authorization: Bearer abc",
	}
	for _, text := range masked {
		if got := RedactNote(text); !strings.Contains(got, "[REDACTED]") {
			t.Errorf("RedactNote(%q) = %q, want masked", text, got)
		}
	}
	kept := []string{
		"токен: строка в заголовке",
		"token: string",
		"API_TOKEN=${API_TOKEN}",
		"password: <your password>",
		"secret: {{ .Values.secret }}",
		"the token expired after rotation",
	}
	for _, text := range kept {
		if got := RedactNote(text); got != text {
			t.Errorf("RedactNote(%q) = %q, want unchanged", text, got)
		}
	}
}

func TestRedactDocumentKeepsPriorBehaviour(t *testing.T) {
	got := RedactDocument("password: hunter2\nAuthorization: Bearer abc123\n" + tokens()["private key"])
	for _, leak := range []string{"hunter2", "abc123", "body"} {
		if strings.Contains(got, leak) {
			t.Errorf("RedactDocument leaked %q: %q", leak, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "[REDACTED PRIVATE KEY]") {
		t.Errorf("RedactDocument = %q", got)
	}
}
