package skillengine_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

// The Russian schema is a translation, and a translation drifts: someone adds a
// field to the embedded schema and does not touch the other file, and from then
// on the two disagree about what the format is. Descriptions are allowed to
// differ — that is the point of a translation; the STRUCTURE is not.
//
// This is the same class the constitution calls out: a document that duplicates
// the source of truth is a defect unless something keeps them equal.
func TestRussianSchemaMatchesStructure(t *testing.T) {
	var en, ru any
	require.NoError(t, yaml.Unmarshal([]byte(se.SchemaYAML), &en), "the embedded schema does not parse")
	require.NoError(t, yaml.Unmarshal([]byte(se.SchemaRU), &ru), "the translation does not parse")

	assert.Equal(t, skeleton(en), skeleton(ru),
		"the schema and its translation diverge structurally — a field was added to one of them only")
}

// skeleton keeps everything that defines the format and drops everything that
// is prose. `description` is the translation's whole reason to exist;
// `examples` hold sample values that read better localised.
func skeleton(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, nested := range t {
			if k == "description" || k == "examples" {
				continue
			}
			out[k] = skeleton(nested)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, nested := range t {
			out[i] = skeleton(nested)
		}
		return out
	default:
		return v
	}
}

// Both files must stay valid YAML with the same set of defined types: a
// $defs entry present in one and missing from the other means the translation
// documents a format that does not exist.
func TestRussianSchemaCoversSameDefs(t *testing.T) {
	defsOf := func(t *testing.T, src string) []string {
		t.Helper()
		var doc struct {
			Defs map[string]any `yaml:"$defs"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
		names := make([]string, 0, len(doc.Defs))
		for name := range doc.Defs {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	assert.Equal(t, defsOf(t, se.SchemaYAML), defsOf(t, se.SchemaRU),
		"the set of $defs differs between skill.schema.yaml and its translation")
}

// The translation must be EMBEDDED, not merely present in the repository: an
// embedder gets the package through `go mod vendor`, which copies only what the
// build references. A schema shown to a skill author who does not read English
// is useless if it never arrives.
func TestRussianSchemaIsEmbedded(t *testing.T) {
	require.NotEmpty(t, se.SchemaRU, "the Russian schema is not embedded — vendoring will not carry it")

	summary := se.SchemaSummaryRU(yaml.Unmarshal)
	require.NotEmpty(t, summary)
	assert.Contains(t, summary, "ФОРМАТ СКИЛЛА", "the summary's own headings stayed English")
	assert.Contains(t, summary, "playbook", "the field list is missing")

	assert.NotEqual(t, se.SchemaSummary(yaml.Unmarshal), summary,
		"both summaries came out identical — one of them reads the wrong schema")
}
