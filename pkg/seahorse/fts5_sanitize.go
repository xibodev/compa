package seahorse

import (
	"regexp"
	"strings"
)

// phraseRegex matches complete quoted phrases like "exact phrase".
// Compiled once at package level to avoid per-call overhead.
var phraseRegex = regexp.MustCompile(`"([^"]+)"`)

// SanitizeFTS5Query escapes user input for safe use in an FTS5 MATCH expression.
//
// FTS5 treats certain characters as operators:
//   - `-` (NOT), `+` (required), `*` (prefix), `^` (initial token)
//   - `OR`, `AND`, `NOT`, `NEAR` (boolean/proximity operators)
//   - `:` (column filter — e.g. `agent:foo` means "search column agent")
//   - `"` (phrase query), `(` `)` (grouping)
//
// Strategy: wrap each whitespace-delimited token in double quotes so FTS5
// treats it as a literal phrase token. User-quoted phrases ("...") are
// preserved as-is. Internal double quotes are stripped. Empty tokens are
// dropped. Tokens are joined with spaces (implicit AND).
//
// The uppercase words AND, OR and NOT, unquoted and between two search terms,
// stay boolean operators, as the short_grep tool documents. Anywhere else
// (first or last, or next to another operator) they are searched literally,
// so the expression is always valid.
//
// Returns empty string for blank input so callers can skip the MATCH query.
//
// Examples:
//
//	"sub-agent restrict"  →  `"sub-agent" "restrict"`
//	"lcm_expand OR crash" →  `"lcm_expand" OR "crash"`
//	"NOT excluded"        →  `"NOT" "excluded"`
//	`hello "world"`       →  `"hello" "world"`
func SanitizeFTS5Query(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	type token struct {
		text     string
		operator bool // bare AND/OR/NOT outside a quoted phrase
	}
	var tokens []token
	addWords := func(s string) {
		for _, t := range strings.Fields(s) {
			if t == "AND" || t == "OR" || t == "NOT" {
				tokens = append(tokens, token{text: t, operator: true})
				continue
			}
			t = strings.ReplaceAll(t, `"`, "")
			if t != "" {
				tokens = append(tokens, token{text: t})
			}
		}
	}

	// Preserve user-quoted phrases: extract "..." groups first, then tokenize the rest.
	lastIndex := 0
	for _, loc := range phraseRegex.FindAllStringIndex(raw, -1) {
		addWords(raw[lastIndex:loc[0]])
		// Preserve the phrase as-is (strip internal quotes for safety)
		phrase := strings.TrimSpace(strings.ReplaceAll(raw[loc[0]+1:loc[1]-1], `"`, ""))
		if phrase != "" {
			tokens = append(tokens, token{text: phrase})
		}
		lastIndex = loc[1]
	}
	addWords(raw[lastIndex:])

	parts := make([]string, 0, len(tokens))
	prevOperand := false
	for i, t := range tokens {
		if t.operator && prevOperand && i+1 < len(tokens) && !tokens[i+1].operator {
			parts = append(parts, t.text)
			prevOperand = false
			continue
		}
		parts = append(parts, `"`+t.text+`"`)
		prevOperand = true
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}
