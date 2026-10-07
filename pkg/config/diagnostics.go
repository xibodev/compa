package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

func decodeJSONWithDiagnostics(data []byte, target any, label string) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return wrapJSONError(data, err, label)
	}

	unknownFields := collectUnknownJSONFields(raw, reflect.TypeOf(target), "")
	if len(unknownFields) > 0 {
		sort.Strings(unknownFields)
		return fmt.Errorf(
			"%s contains unknown field(s): %s",
			label,
			strings.Join(unknownFields, ", "),
		)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return wrapJSONError(data, err, label)
	}
	return nil
}

func DiagnosticSummary(err error) string {
	if err == nil {
		return ""
	}
	summary, _ := splitDiagnosticError(err.Error())
	return stripANSISequences(summary)
}

func formatDiagnosticLogMessage(prefix string, err error) string {
	if err == nil {
		return prefix
	}

	summary, preview := splitDiagnosticError(err.Error())
	summary = stripANSISequences(summary)
	if preview == "" {
		if summary == "" {
			return prefix
		}
		return prefix + ": " + summary
	}
	if summary == "" {
		return prefix + "\n" + preview
	}
	return prefix + ": " + summary + "\n" + preview
}

func wrapJSONError(data []byte, err error, label string) error {
	switch e := err.(type) {
	case *json.SyntaxError:
		line, column := lineAndColumnForOffset(data, e.Offset)
		preview := diagnosticPreviewForOffset(data, e.Offset)
		if preview != "" {
			return fmt.Errorf(
				"%s syntax error at line %d, column %d: %w\n%s",
				label,
				line,
				column,
				err,
				preview,
			)
		}
		return fmt.Errorf("%s syntax error at line %d, column %d: %w", label, line, column, err)
	case *json.UnmarshalTypeError:
		line, column := lineAndColumnForOffset(data, e.Offset)
		preview := diagnosticPreviewForOffset(data, e.Offset)
		field := strings.TrimSpace(e.Field)
		value := e.Value
		if isSecretLookingKey(field) {
			// "number 12345" would print the secret; keep only its kind.
			value, _, _ = strings.Cut(value, " ")
		}
		if field != "" {
			if preview != "" {
				return fmt.Errorf(
					"%s type error at line %d, column %d for field %q: expected %s but got %s\n%s",
					label,
					line,
					column,
					field,
					e.Type.String(),
					value,
					preview,
				)
			}
			return fmt.Errorf(
				"%s type error at line %d, column %d for field %q: expected %s but got %s",
				label,
				line,
				column,
				field,
				e.Type.String(),
				value,
			)
		}
		if preview != "" {
			return fmt.Errorf(
				"%s type error at line %d, column %d: expected %s but got %s\n%s",
				label,
				line,
				column,
				e.Type.String(),
				e.Value,
				preview,
			)
		}
		return fmt.Errorf(
			"%s type error at line %d, column %d: expected %s but got %s",
			label,
			line,
			column,
			e.Type.String(),
			e.Value,
		)
	default:
		return fmt.Errorf("failed to parse %s: %w", label, err)
	}
}

func splitDiagnosticError(message string) (string, string) {
	if idx := strings.IndexByte(message, '\n'); idx >= 0 {
		return message[:idx], message[idx+1:]
	}
	return message, ""
}

func stripANSISequences(s string) string {
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		i += 2
		for i < len(s) {
			c := s[i]
			if c >= '@' && c <= '~' {
				break
			}
			i++
		}
	}

	return b.String()
}

func diagnosticPreviewForOffset(data []byte, offset int64) string {
	if len(data) == 0 {
		return ""
	}

	start, end := lineBoundsForOffset(data, offset)
	if start >= end {
		return ""
	}

	lineNumber, column := lineAndColumnForOffset(data, offset)
	line := strings.TrimRight(string(data[start:end]), "\r\n")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	// Previews reach logs and the UI, so secret values never appear in them.
	line = maskSecretJSONLine(line, insideSecretValue(data[:start]))

	trimmedLine, trimOffset := trimDiagnosticLine(line, column)
	if trimmedLine == "" {
		return ""
	}

	prefix := fmt.Sprintf("%4d | ", lineNumber)
	caretColumn := column - trimOffset
	if caretColumn < 1 {
		caretColumn = 1
	}

	if diagnosticsUseColor() {
		linePrefix := "\x1b[2m" + prefix + "\x1b[0m"
		caretPrefix := "\x1b[2m" + strings.Repeat(" ", len(fmt.Sprintf("%4d", lineNumber))) + " | " + "\x1b[0m"
		highlighted := highlightDiagnosticColumn(trimmedLine, caretColumn)
		caretPad := strings.Repeat(" ", maxRuneCount(trimmedLine, caretColumn-1))
		return fmt.Sprintf(
			"  %s%s\n  %s%s\x1b[1;31m^\x1b[0m",
			linePrefix,
			highlighted,
			caretPrefix,
			caretPad,
		)
	}

	caretPrefix := strings.Repeat(" ", len(prefix))
	caretPad := strings.Repeat(" ", maxRuneCount(trimmedLine, caretColumn-1))
	return fmt.Sprintf(
		"  %s%s\n  %s%s^",
		prefix,
		trimmedLine,
		caretPrefix,
		caretPad,
	)
}

// secretKeyWords are the last words of the names of secrets, such as
// "api_key", "api_keys", "bot_token" or "crypto_passphrase". A name that
// ends in another word, such as "max_tokens", is not a secret.
var secretKeyWords = map[string]bool{
	"key": true, "keys": true, "token": true, "secret": true, "password": true, "passphrase": true,
}

// isSecretLookingKey reports whether a JSON key, or the last segment of a
// dotted field path, names something secret: its last word is one of
// secretKeyWords.
func isSecretLookingKey(key string) bool {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		key = key[i+1:]
	}
	return secretKeyWords[strings.ToLower(lastWord(key))]
}

// lastWord returns the last word of a snake_case, kebab-case or camelCase
// name, such as "TOKEN" of "GITHUB_TOKEN" and "Key" of "X-Api-Key".
func lastWord(name string) string {
	name = strings.TrimRight(name, "_- ")
	start := 0
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || c == '-' || c == ' ':
			start = i + 1
		case i > 0 && 'A' <= c && c <= 'Z' && 'a' <= name[i-1] && name[i-1] <= 'z':
			start = i
		}
	}
	return name[start:]
}

var jsonKeyPattern = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*:\s*`)

// maskSecretJSONLine replaces the values of secret-looking keys in one line of
// JSON with asterisks, byte for byte so the caret under the line still points
// at the same place. With whole, the line is inside a secret value that began
// earlier, and everything but its structure is masked.
func maskSecretJSONLine(line string, whole bool) string {
	b := []byte(line)
	if whole {
		maskSpan(b, 0, len(b))
		return string(b)
	}
	for _, m := range jsonKeyPattern.FindAllStringSubmatchIndex(line, -1) {
		if !isSecretLookingKey(line[m[2]:m[3]]) {
			continue
		}
		start := m[1]
		if start < len(b) && b[start] == '"' {
			end := start + 1
			for end < len(b) && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			maskSpan(b, start+1, min(end, len(b)))
			continue
		}
		end, depth := start, 0
		for ; end < len(b); end++ {
			c := b[end]
			if c == '[' || c == '{' {
				depth++
			} else if c == ']' || c == '}' {
				if depth == 0 {
					break
				}
				depth--
			} else if c == ',' && depth == 0 {
				break
			}
		}
		maskSpan(b, start, end)
	}
	return string(b)
}

// maskSpan masks b[start:end] except whitespace and JSON punctuation.
func maskSpan(b []byte, start, end int) {
	for i := start; i < end; i++ {
		switch b[i] {
		case ' ', '\t', '"', '[', ']', '{', '}', ',', ':':
		default:
			b[i] = '*'
		}
	}
}

// insideSecretValue reports whether the JSON that follows prefix is still
// part of the value of a secret-looking key: a container such as
// "api_keys": [ opened on an earlier line, or a key whose value starts on
// the next line.
func insideSecretValue(prefix []byte) bool {
	var stack []string
	var str []byte
	var lastString, key string
	inString, escaped := false, false
	for _, c := range prefix {
		if inString {
			switch {
			case escaped:
				escaped = false
				str = append(str, c)
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				lastString = string(str)
			default:
				str = append(str, c)
			}
			continue
		}
		switch c {
		case '"':
			inString, str = true, str[:0]
		case ':':
			key, lastString = lastString, ""
		case '{', '[':
			stack = append(stack, key)
			key = ""
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			key = ""
		case ',':
			key = ""
		}
	}
	if isSecretLookingKey(key) {
		return true
	}
	for _, k := range stack {
		if isSecretLookingKey(k) {
			return true
		}
	}
	return false
}

func lineAndColumnForOffset(data []byte, offset int64) (int, int) {
	if offset <= 0 {
		return 1, 1
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}

	line := 1
	column := 1
	for i := int64(0); i < offset-1; i++ {
		if data[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column
}

func lineBoundsForOffset(data []byte, offset int64) (int, int) {
	if len(data) == 0 {
		return 0, 0
	}

	if offset <= 0 {
		offset = 1
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}

	index := int(offset - 1)
	if index < 0 {
		index = 0
	}
	if index >= len(data) {
		index = len(data) - 1
	}

	start := index
	for start > 0 && data[start-1] != '\n' {
		start--
	}

	end := index
	for end < len(data) && data[end] != '\n' {
		end++
	}

	return start, end
}

func trimDiagnosticLine(line string, column int) (string, int) {
	runes := []rune(line)
	if len(runes) == 0 {
		return "", 0
	}

	if len(runes) <= 160 {
		return line, 0
	}

	const contextBefore = 60
	const maxWidth = 160

	start := column - 1 - contextBefore
	if start < 0 {
		start = 0
	}
	if start > len(runes)-maxWidth {
		start = len(runes) - maxWidth
	}
	if start < 0 {
		start = 0
	}

	end := start + maxWidth
	if end > len(runes) {
		end = len(runes)
	}

	trimmed := string(runes[start:end])
	trimOffset := start

	if start > 0 {
		trimmed = "..." + trimmed
		trimOffset -= 3
	}
	if end < len(runes) {
		trimmed += "..."
	}

	return trimmed, trimOffset
}

func diagnosticsUseColor() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func highlightDiagnosticColumn(line string, column int) string {
	runes := []rune(line)
	if column < 1 || column > len(runes) {
		return line
	}

	index := column - 1
	return string(runes[:index]) + "\x1b[31m" + string(runes[index]) + "\x1b[0m" + string(runes[index+1:])
}

func maxRuneCount(s string, count int) int {
	if count <= 0 {
		return 0
	}
	runes := []rune(s)
	if count > len(runes) {
		count = len(runes)
	}
	return utf8.RuneCountInString(string(runes[:count]))
}

func collectUnknownJSONFields(raw any, targetType reflect.Type, path string) []string {
	targetType = derefType(targetType)
	if targetType == nil {
		return nil
	}

	switch targetType.Kind() {
	case reflect.Struct:
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		fieldMap := jsonFieldTypeMap(targetType)
		var issues []string
		for key, value := range obj {
			fieldType, exists := fieldMap[key]
			fieldPath := appendJSONPath(path, key)
			if !exists {
				issues = append(issues, fieldPath)
				continue
			}
			issues = append(issues, collectUnknownJSONFields(value, fieldType, fieldPath)...)
		}
		return issues
	case reflect.Slice, reflect.Array:
		items, ok := raw.([]any)
		if !ok {
			return nil
		}
		var issues []string
		elemType := targetType.Elem()
		for i, item := range items {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			issues = append(issues, collectUnknownJSONFields(item, elemType, itemPath)...)
		}
		return issues
	case reflect.Map:
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		var issues []string
		elemType := targetType.Elem()
		for key, value := range obj {
			fieldPath := appendJSONPath(path, key)
			issues = append(issues, collectUnknownJSONFields(value, elemType, fieldPath)...)
		}
		return issues
	default:
		return nil
	}
}

func jsonFieldTypeMap(t reflect.Type) map[string]reflect.Type {
	result := make(map[string]reflect.Type)
	populateJSONFieldTypeMap(result, derefType(t))
	return result
}

func populateJSONFieldTypeMap(result map[string]reflect.Type, t reflect.Type) {
	if t == nil || t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}

		if field.Anonymous && name == "" {
			populateJSONFieldTypeMap(result, derefType(field.Type))
			continue
		}

		if name == "" {
			name = field.Name
		}
		result[name] = field.Type
	}
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func appendJSONPath(path, segment string) string {
	if path == "" {
		return segment
	}
	return path + "." + segment
}
