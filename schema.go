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

// SchemaRU — the same contract in Russian.
//
// EMBEDDED, not merely present in the repository, and that is the whole point:
// an embedder shows the schema to whoever writes the skill, and a file that is
// not embedded does not travel — `go mod vendor` copies only what the build
// references, so a translation living beside the package would simply not
// arrive. A field the author cannot read is a field the author does not use.
//
// A test keeps this structurally identical to SchemaYAML; only the prose
// differs, and SchemaYAML stays the source of truth for validation.
//
//go:embed skill.schema.ru.yaml
var SchemaRU string

// SchemaSummary — a compact reference for the format in English: the fields and
// the first line of each description.
//
// The full schema is 56 KB. Handing that to a model means spending context on
// a reference book: the same kind of suffocation the format protects against
// by truncating tool results. A skill author needs the list of fields and what
// each means; the details are in the spec, for humans.
func SchemaSummary(unmarshal Unmarshal) string {
	return summarize(SchemaYAML, headingsEN, unmarshal)
}

// SchemaSummaryRU — the same reference in Russian, for an embedder whose skill
// authors (and whose skill-writing model) work in Russian.
//
// A separate function rather than SchemaSummaryOf(lang string): a language code
// is a string, and a string can be misspelled into a silent empty result. Two
// names cannot.
func SchemaSummaryRU(unmarshal Unmarshal) string {
	return summarize(SchemaRU, headingsRU, unmarshal)
}

// schemaHeadings — the summary's own words. They are not taken from the schema
// (it has no place for them), so a translated summary needs its own set:
// Russian field descriptions under English headings read as a bug.
type schemaHeadings struct{ format, step, other string }

var headingsEN = schemaHeadings{
	format: "SKILL FORMAT (required fields marked *)\n\n",
	step:   "\nSTEP (exactly one action: instruction | call | set | switch | if | exit | delegate | for_each | parallel)\n\n",
	other:  "\nOTHER CONSTRUCTS\n\n",
}

var headingsRU = schemaHeadings{
	format: "ФОРМАТ СКИЛЛА (обязательные помечены *)\n\n",
	step:   "\nШАГ (ровно одно действие: instruction | call | set | switch | if | exit | delegate | for_each | parallel)\n\n",
	other:  "\nОСТАЛЬНЫЕ КОНСТРУКЦИИ\n\n",
}

func summarize(schema string, h schemaHeadings, unmarshal Unmarshal) string {
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
	if err := unmarshal([]byte(schema), &root); err != nil {
		return "" // schema is broken — let the caller hand out the full one
	}

	req := map[string]bool{}
	for _, r := range root.Required {
		req[r] = true
	}

	var b strings.Builder
	b.WriteString(h.format)
	for _, name := range sortedKeys(root.Properties) {
		p := root.Properties[name]
		mark := " "
		if req[name] {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-22s %s\n", mark, name, firstLine(p.Description))
	}

	b.WriteString(h.step)
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

	b.WriteString(h.other)
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
