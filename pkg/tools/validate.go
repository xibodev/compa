package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/xibodev/compa/v3/pkg/logger"
)

// validateToolArgs validates args against a tool's JSON Schema, with the full
// semantics of JSON Schema: an absent additionalProperties allows other
// properties, and keywords such as minimum, pattern, anyOf and $ref apply.
//
// A schema that cannot be compiled is not enforced. Validation is there to
// let the model correct a call; an MCP server's unusable schema must not make
// its tool unusable.
func validateToolArgs(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	resolved, err := resolveToolSchema(schema)
	if err != nil {
		logger.DebugCF("tool", "Tool schema cannot be compiled; arguments are not validated",
			map[string]any{"error": err.Error()})
		return nil
	}

	if args == nil {
		args = map[string]any{}
	}
	// Validate the arguments as the JSON they came as: Go values such as int
	// or []string, which callers inside Compa pass, become JSON values first.
	data, err := json.Marshal(args)
	if err != nil {
		logger.DebugCF("tool", "Tool arguments cannot be encoded; arguments are not validated",
			map[string]any{"error": err.Error()})
		return nil
	}
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return nil
	}

	if err := resolved.Validate(instance); err != nil {
		return describeSchemaError(err)
	}
	return nil
}

// resolvedSchemas caches compiled schemas by their JSON text. Tools rebuild
// their parameter maps on every call, but the text rarely changes.
var (
	resolvedSchemasMu sync.Mutex
	resolvedSchemas   = map[string]*jsonschema.Resolved{}
)

// maxResolvedSchemas bounds the cache; past it, the cache starts over.
const maxResolvedSchemas = 512

func resolveToolSchema(schemaMap map[string]any) (*jsonschema.Resolved, error) {
	data, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, err
	}
	key := string(data)

	resolvedSchemasMu.Lock()
	resolved, ok := resolvedSchemas[key]
	resolvedSchemasMu.Unlock()
	if ok {
		return resolved, nil
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	// Servers declare all sorts of drafts; validate every schema with the
	// current rules rather than refuse the drafts the validator does not
	// name.
	schema.Schema = ""
	resolved, err = schema.Resolve(nil)
	if err != nil {
		return nil, err
	}

	resolvedSchemasMu.Lock()
	if len(resolvedSchemas) >= maxResolvedSchemas {
		resolvedSchemas = map[string]*jsonschema.Resolved{}
	}
	resolvedSchemas[key] = resolved
	resolvedSchemasMu.Unlock()
	return resolved, nil
}

var (
	reSchemaQuoted   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	reSchemaTypeLeaf = regexp.MustCompile(`has type "([^"]*)", want (?:one of )?"([^"]*)"`)
)

// maxSchemaReasonLen bounds a validation reason taken over verbatim, which
// may quote whole subschemas.
const maxSchemaReasonLen = 300

// describeSchemaError rewrites a jsonschema validation error in the style
// tool errors use: `missing required property "x"`, `property "x": expected
// string, got number`, `property "x": value y is not in enum`, `unexpected
// property "x"`. Other reasons keep the validator's wording, under the same
// property prefix.
func describeSchemaError(err error) error {
	msg := err.Error()
	path := ""
	for strings.HasPrefix(msg, "validating ") {
		rest := strings.TrimPrefix(msg, "validating ")
		idx := strings.Index(rest, ": ")
		if idx < 0 {
			break
		}
		if p := schemaPathProperty(rest[:idx]); p != "" {
			path = p
		}
		msg = rest[idx+2:]
	}
	if line, _, found := strings.Cut(msg, "\n"); found {
		msg = strings.TrimSuffix(line, ":")
	}

	reason := schemaErrorReason(msg)
	if path == "" {
		return fmt.Errorf("%s", reason)
	}
	return fmt.Errorf("property %q: %s", path, reason)
}

func schemaErrorReason(msg string) string {
	switch {
	case strings.HasPrefix(msg, "required: missing properties: "):
		names := quotedNames(msg)
		if len(names) == 1 {
			return fmt.Sprintf("missing required property %q", names[0])
		}
		if len(names) > 1 {
			return "missing required properties " + quoteJoin(names)
		}
	case strings.HasPrefix(msg, "unexpected additional properties "):
		names := quotedNames(msg)
		if len(names) == 1 {
			return fmt.Sprintf("unexpected property %q", names[0])
		}
		if len(names) > 1 {
			return "unexpected properties " + quoteJoin(names)
		}
	case strings.HasPrefix(msg, "type: "):
		if m := reSchemaTypeLeaf.FindStringSubmatch(msg); m != nil {
			got, want := m[1], strings.ReplaceAll(m[2], ", ", " or ")
			if got == "number" && want == "integer" {
				return "expected integer, got number with fractional part"
			}
			return fmt.Sprintf("expected %s, got %s", want, got)
		}
	case strings.HasPrefix(msg, "enum: "):
		value, _, found := strings.Cut(strings.TrimPrefix(msg, "enum: "), " does not equal any of")
		if found {
			return fmt.Sprintf("value %s is not in enum", value)
		}
	}
	if len(msg) > maxSchemaReasonLen {
		cut, _ := runePrefix(msg, maxSchemaReasonLen)
		msg = cut + "..."
	}
	return msg
}

// schemaPathProperty turns the location of a subschema, such as
// "/properties/address/properties/city" or "/properties/tags/items", into
// the argument it describes ("address.city", "tags[]"). Locations that are
// not under properties give "".
func schemaPathProperty(location string) string {
	if !strings.HasPrefix(location, "/") {
		return ""
	}
	tokens := strings.Split(location[1:], "/")
	var b strings.Builder
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "properties":
			if i+1 >= len(tokens) {
				return ""
			}
			i++
			name := strings.NewReplacer("~1", "/", "~0", "~").Replace(tokens[i])
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(name)
		case "items", "additionalItems":
			b.WriteString("[]")
		case "prefixItems":
			if i+1 < len(tokens) {
				i++
				b.WriteString("[" + tokens[i] + "]")
			}
		case "additionalProperties", "patternProperties":
			if tokens[i] == "patternProperties" && i+1 < len(tokens) {
				i++
			}
			b.WriteString(".*")
		default:
			// anyOf/0, allOf/1, $defs/x...: not an argument name.
			if b.Len() == 0 {
				return ""
			}
		}
	}
	return b.String()
}

func quotedNames(msg string) []string {
	var names []string
	for _, m := range reSchemaQuoted.FindAllStringSubmatch(msg, -1) {
		names = append(names, strings.ReplaceAll(m[1], `\"`, `"`))
	}
	return names
}

func quoteJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, ", ")
}
