package skillengine_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

// The schema is a SECOND copy of what the format is; the structs are the first.
// Nothing kept them equal, and they drifted: `max_tokens` is read by the engine
// on every step and was described only inside `sampling`, so a host that repairs
// a description against the schema removed a legal field — quietly, because a
// dropped ceiling looks exactly like a description that never asked for one.
//
// That is what this test is for. It compares the yaml tags of the structs with
// the schema's properties, so the next field added to one of them fails here
// instead of in somebody's skill.
func TestSchemaCoversStructFields(t *testing.T) {
	var doc struct {
		Defs map[string]struct {
			Properties map[string]any `yaml:"properties"`
		} `yaml:"$defs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(se.SchemaYAML), &doc))

	// Type → the definition that describes it. Anything the schema does not
	// describe as an object (Sampling's numbers, say) needs no entry.
	cases := []struct {
		def string
		typ any
	}{
		{"Step", se.Step{}},
		{"Call", se.Call{}},
		{"Set", se.Set{}},
		{"If", se.If{}},
		{"Switch", se.Switch{}},
		{"ForEach", se.ForEach{}},
		{"Parallel", se.Parallel{}},
		{"Delegate", se.Delegate{}},
		{"Exit", se.Exit{}},
		{"Asset", se.Asset{}},
		{"Profile", se.Profile{}},
		{"Sampling", se.Sampling{}},
	}

	for _, c := range cases {
		t.Run(c.def, func(t *testing.T) {
			def, ok := doc.Defs[c.def]
			require.True(t, ok, "the schema has no definition %q", c.def)

			var missing []string
			for _, field := range yamlFields(reflect.TypeOf(c.typ)) {
				if _, has := def.Properties[field]; !has {
					missing = append(missing, field)
				}
			}
			sort.Strings(missing)
			assert.Empty(t, missing,
				"the engine reads these fields of %s, and the schema does not describe them: %s",
				c.def, strings.Join(missing, ", "))
		})
	}
}

// yamlFields lists the field names a type accepts in YAML, following inline
// embeds — `Run` is inline in `Step`, so its fields are written ON the step and
// belong to the step's set of keys.
//
// A field tagged `schema:"-"` is skipped: the parser reads it in order to
// REFUSE it, not because the format has it. Today that is a generation
// parameter written on a step instead of inside `sampling` — describing it in
// the schema would state the opposite of what Validate does with it.
func yamlFields(t reflect.Type) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Tag.Get("schema") == "-" {
			continue
		}
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			out = append(out, yamlFields(f.Type)...)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out = append(out, name)
	}
	return out
}
