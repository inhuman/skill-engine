package skillengine

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// Unmarshal — parsing YAML into a struct. The engine does NOT pull in a yaml
// library itself: the skill description is read by the embedding application
// anyway, it already has a parser, and a second copy here would become its
// dependency — with its versions and its conflicts. Exactly one method is
// needed, so this is a function rather than an interface.
//
// `yaml.Unmarshal` from gopkg.in/yaml.v3 fits without a wrapper.
type Unmarshal func(data []byte, v any) error

// SchemaYAML — the skill format contract (JSON Schema written in YAML).
//
// It lives next to the engine, not only in the spec: skills are validated
// against it on write, and it is handed to the model when it writes a skill on
// a human's request. The spec is documentation; the source of truth for code
// is here.
//
//go:embed skill.schema.yaml
var SchemaYAML string

// SchemaSummary — a compact reference for the format: the fields and the first
// line of each description.
//
// The full schema is 56 KB. Handing that to a model means spending context on
// a reference book: the same kind of suffocation the format protects against
// by truncating tool results. A skill author needs the list of fields and what
// each means; the details are in the spec, for humans.
func SchemaSummary(unmarshal Unmarshal) string {
	if unmarshal == nil {
		return ""
	}
	var root struct {
		Properties map[string]struct {
			Description string   `yaml:"description"`
			Type        string   `yaml:"type"`
			Enum        []string `yaml:"enum"`
			Ref         string   `yaml:"$ref"`
		} `yaml:"properties"`
		Required []string `yaml:"required"`
		Defs     map[string]struct {
			Description string `yaml:"description"`
			Properties  map[string]struct {
				Description string   `yaml:"description"`
				Type        string   `yaml:"type"`
				Enum        []string `yaml:"enum"`
			} `yaml:"properties"`
		} `yaml:"$defs"`
	}
	if err := unmarshal([]byte(SchemaYAML), &root); err != nil {
		return "" // schema is broken — let the caller hand out the full one
	}

	req := map[string]bool{}
	for _, r := range root.Required {
		req[r] = true
	}

	var b strings.Builder
	b.WriteString("SKILL FORMAT (required fields marked *)\n\n")
	for _, name := range sortedKeys(root.Properties) {
		p := root.Properties[name]
		mark := " "
		if req[name] {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-22s %s\n", mark, name, firstLine(p.Description))
	}

	b.WriteString("\nSTEP (exactly one action: instruction | call | set | switch | if | exit | delegate | for_each | parallel)\n\n")
	if step, ok := root.Defs["Step"]; ok {
		for _, name := range sortedKeys(step.Properties) {
			f := step.Properties[name]
			desc := firstLine(f.Description)
			if desc == "" {
				// A reference field: the description lives on the type it
				// points at.
				if d, ok := root.Defs[capitalizeRef(name)]; ok {
					desc = firstLine(d.Description)
				}
			}
			fmt.Fprintf(&b, "  %-18s %s\n", name, desc)
		}
	}

	b.WriteString("\nOTHER CONSTRUCTS\n\n")
	for _, name := range sortedKeys(root.Defs) {
		if name == "Step" {
			continue
		}
		fmt.Fprintf(&b, "  %-18s %s\n", name, firstLine(root.Defs[name].Description))
	}
	return b.String()
}

// capitalizeRef turns a field name into a type name: for_each → ForEach,
// call → Call.
func capitalizeRef(field string) string {
	parts := strings.Split(field, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
