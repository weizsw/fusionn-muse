package logger

import (
	"regexp"
	"strings"
)

// sensitiveKeyNames and sensitiveKeySuffixes are the single source of truth for
// credential-looking names: the field-name masker (isSensitiveKey) and the text
// redactor are both built from them, so the two cannot drift. Hyphen and
// underscore spellings are equivalent (api-secret == api_secret).
var (
	sensitiveKeyNames = []string{
		"api_key", "apikey", "api_secret", "access_key", "access_token", "auth",
		"authorization", "client_secret", "credential", "credentials", "token",
		"password", "passwd", "secret",
	}
	sensitiveKeySuffixes = []string{
		"_token", "_password", "_passwd", "_secret", "_authorization",
	}
)

// credentialKeyChars are the characters a credential name part may use. A
// \w class would stop at the hyphen, so an equivalent name such as
// openrouter-api-key would not match the underscore spelling of the same key.
const credentialKeyChars = `[A-Za-z0-9_-]`

// credentialKeyPattern matches a credential field name in text: one of the
// exact names, any key-part ending in a sensitive suffix, or any key-part
// containing the api_key name (the same rule isSensitiveKey applies). It is a
// single capturing group so the surrounding replacements keep the matched name.
func credentialKeyPattern() string {
	alternatives := make([]string, 0, len(sensitiveKeyNames)+len(sensitiveKeySuffixes)+1)
	for _, name := range sensitiveKeyNames {
		alternatives = append(alternatives, hyphenOrUnderscore(name))
	}
	for _, suffix := range sensitiveKeySuffixes {
		alternatives = append(alternatives, credentialKeyChars+`+`+hyphenOrUnderscore(suffix))
	}
	alternatives = append(alternatives, credentialKeyChars+`*`+hyphenOrUnderscore("api_key")+credentialKeyChars+`*`)
	return `(` + strings.Join(alternatives, "|") + `)`
}

func hyphenOrUnderscore(name string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[-_]")
}

// Redact masks credentials in text that ends up in a log value. It is a
// backstop, not a license to log raw payloads: call sites still prefer
// whitelisted safe fields and explicit known-secret replacement, and INFO
// never carries bodies, prompts or full third-party output. Fields whose
// *name* is sensitive never reach here at all — the renderer masks them
// unconditionally (see isSensitiveKey).
var redactPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// URL userinfo: scheme://user:pass@ -> scheme://***@
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s]+@`), `${1}***@`},
	// Authorization headers mask the whole credential, scheme included:
	// "Authorization: Bearer x" -> "Authorization: ***".
	{regexp.MustCompile(`(?i)\b(` + hyphenOrUnderscore("authorization") + `)(["']?)(\s*[:=]\s*)(?:(?:basic|bearer)\s+)?("[^"]*"|'[^']*'|[A-Za-z0-9._~+/=\-]+)`), `${1}${2}${3}***`},
	// Standalone Bearer/Basic tokens (no Authorization prefix).
	{regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=\-]+`), `${1}${2}***`},
	// key=value / key: value for sensitive keys, tolerating JSON-style
	// quoted keys and quoted values. Also covers credential query
	// parameters such as ?api_key=...
	{regexp.MustCompile(`(?i)(["']?)\b` + credentialKeyPattern() + `(["']?)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}\]]+)`), `${1}${2}${3}${4}***`},
	// Credential query parameters with generic names: ?key= / &sig=. The
	// value stops before a closing quote or colon so surrounding punctuation
	// survives.
	{regexp.MustCompile(`([?&](?:key|sig|signature)=)[^\s&#"':]+`), `${1}***`},
	// CLI flags: --api-key value / --access-token="some value".
	{regexp.MustCompile(`(?i)--` + credentialKeyPattern() + `(\s*[= ]\s*)("[^"]*"|'[^']*'|[^\s]+)`), `--${1}${2}***`},
	// Well-known API key shapes.
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{12,}\b`), `sk-***`},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{20,}\b`), `***`},
}

// Redact returns s with credentials masked.
func Redact(s string) string {
	for _, p := range redactPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// isSensitiveKey reports whether a field name itself implies the value is a
// credential; the renderer masks such values unconditionally.
func isSensitiveKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, name := range sensitiveKeyNames {
		if k == name {
			return true
		}
	}
	if strings.Contains(k, "api_key") {
		return true
	}
	for _, suffix := range sensitiveKeySuffixes {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}
