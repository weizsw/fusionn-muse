package logger

import (
	"strings"
	"testing"
)

// Shared cross-language redaction vectors. Go/Python must print identical
// output for every row. Values use plain fake literals (never real-shaped
// keys) so the vectors stay stable.
func TestRedact(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"url userinfo with password", `https://user:FAKEPASS123@example.test/v1`, `https://***@example.test/v1`},
		{"url userinfo without password", `https://FAKEUSER123@example.test/v1`, `https://***@example.test/v1`},
		{"bearer header", `Authorization: Bearer FAKEBEARER123`, `Authorization: ***`},
		{"basic header", `Authorization: Basic FAKEBASIC123`, `Authorization: ***`},
		{"plain authorization header", `Authorization: FAKEAUTH123`, `Authorization: ***`},
		{"query keeps trailing quote and colon", `Get "https://service.invalid/?key=FAKEQKEY123": timeout`, `Get "https://service.invalid/?key=***": timeout`},
		{"query api key", `GET https://h/x?api_key=FAKEAPI123&lang=en`, `GET https://h/x?api_key=***&lang=en`},
		{"access key kv", `access_key=FAKEACCESS123`, `access_key=***`},
		{"api key kv", `api_key=FAKEAPI123`, `api_key=***`},
		{"client secret kv", `client_secret=FAKECLIENT123`, `client_secret=***`},
		{"kv colon quoted", `password: "FAKEPASS123"`, `password: ***`},
		{"kv in error text", `dial tcp: token=FAKETOKEN123 failed`, `dial tcp: token=*** failed`},
		{"cli flag space", `--api-key FAKECLI123 --lang en`, `--api-key *** --lang en`},
		{"cli flag equals", `--token=FAKETOKEN123`, `--token=***`},
		{"cli flag quoted with space", `--api-key "FAKECLI123 more"`, `--api-key ***`},
		{"json quoted key", `{"api_key":"FAKEAPI123"}`, `{"api_key":***}`},
		{"json quoted key space", `{ "token": "FAKETOKEN123" }`, `{ "token": *** }`},
		// Credential names shared with the field-name masker: exact names,
		// suffix endings and hyphen/underscore equivalence.
		{"api_secret kv", `api_secret=REVIEW_ONLY_SECRET`, `api_secret=***`},
		{"suffix password kv", `db_password=REVIEW_ONLY_SECRET`, `db_password=***`},
		{"suffix secret kv", `signing_secret=REVIEW_ONLY_SECRET`, `signing_secret=***`},
		{"suffix authorization kv", `proxy_authorization=REVIEW_ONLY_SECRET`, `proxy_authorization=***`},
		{"hyphen api key kv", `api-key=REVIEW_ONLY_SECRET`, `api-key=***`},
		{"hyphen client secret kv", `client-secret: "REVIEW_ONLY_SECRET"`, `client-secret: ***`},
		{"hyphen refresh token kv", `refresh-token=REVIEW_ONLY_SECRET`, `refresh-token=***`},
		{"json hyphen key", `{"api-secret":"REVIEW_ONLY_SECRET"}`, `{"api-secret":***}`},
		{"prefixed api key", `openrouter_api_key=REVIEW_ONLY_SECRET`, `openrouter_api_key=***`},
		// A hyphenated spelling of a prefixed name is the same key: the name
		// part must accept hyphens, not only word characters.
		{"hyphen prefixed api key kv", `openrouter-api-key=REVIEW_ONLY_SECRET`, `openrouter-api-key=***`},
		{"cli hyphen prefixed api key", `--openrouter-api-key REVIEW_ONLY_SECRET`, `--openrouter-api-key ***`},
		{"cli hyphen prefixed api key equals", `--openrouter_api_key=REVIEW_ONLY_SECRET`, `--openrouter_api_key=***`},
		{"cli hyphen db password", `--my-db-password REVIEW_ONLY_SECRET`, `--my-db-password ***`},
		{"cli underscore db password", `--my_db_password=REVIEW_ONLY_SECRET`, `--my_db_password=***`},
		{"cli hyphen refresh token", `--svc-refresh-token REVIEW_ONLY_SECRET`, `--svc-refresh-token ***`},
		{"cli access token", `--access-token REVIEW_ONLY_SECRET`, `--access-token ***`},
		{"cli hyphen db password", `--db-password=REVIEW_ONLY_SECRET`, `--db-password=***`},
		{"cli client secret", `--client-secret "REVIEW_ONLY_SECRET more"`, `--client-secret ***`},
		// Over-masking guards: names without a credential separator survive.
		{"prose provider timeout", `provider timeout after 30s`, `provider timeout after 30s`},
		{"prose tokenization", `tokenization finished`, `tokenization finished`},
		{"prose keyboard", `keyboard layout loaded`, `keyboard layout loaded`},
		{"prose api gateway", `api gateway responded`, `api gateway responded`},
		{"prose refresh completed", `refresh completed`, `refresh completed`},
		{"prose credentials policy", `credentials accepted by policy docs`, `credentials accepted by policy docs`},
		{"cli non-credential flag", `--token-count 5`, `--token-count 5`},
		{"cli non-credential access flag", `--access-token-count 5`, `--access-token-count 5`},
		{"openrouter shape", "key is sk-" + strings.Repeat("a", 18) + " ok", `key is sk-*** ok`},
		{"gemini shape", "key is AIza" + strings.Repeat("b", 30), `key is ***`},
		// Over-masking guards: prose without a separator must survive.
		{"prose token", `token expired, retrying`, `token expired, retrying`},
		{"prose password", `password required`, `password required`},
		{"plain", `stage completed in 2s`, `stage completed in 2s`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.input); got != tt.want {
				t.Errorf("Redact(%q)\n got: %q\nwant: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRedactIdempotent(t *testing.T) {
	s := `Authorization: *** api_key=*** access_key=*** https://***@h sk-*** "https://h/x?key=***": ok`
	if got := Redact(s); got != s {
		t.Errorf("Redact not idempotent: %q -> %q", s, got)
	}
}
