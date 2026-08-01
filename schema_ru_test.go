package skillengine_test

import (
	"fmt"
	"os"
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

	raw, err := os.ReadFile("skill.schema.ru.yaml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &ru), "the translation does not parse")

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
	defsOf := func(t *testing.T, src []byte) []string {
		t.Helper()
		var doc struct {
			Defs map[string]any `yaml:"$defs"`
		}
		require.NoError(t, yaml.Unmarshal(src, &doc))
		names := make([]string, 0, len(doc.Defs))
		for name := range doc.Defs {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	raw, err := os.ReadFile("skill.schema.ru.yaml")
	require.NoError(t, err)

	assert.Equal(t, defsOf(t, []byte(se.SchemaYAML)), defsOf(t, raw),
		fmt.Sprintf("the set of $defs differs between %s and its translation", "skill.schema.yaml"))
}
