package skillengine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const wholeSkill = `skill_engine_version: "2.1.0"
skill_version: "1.2.0"
name: pods
description: Show the pods of a namespace.
trigger_examples: ["what is running in backend"]
servers: [staging, prod]
builtin_tools: [render_diagram]
role: reader
kind: leaf
temperature: 0.3
mode: workflow
meta:
  owner: platform
workflow:
  tools: [staging]
  steps:
    - name: list
      call: {tool: "staging:get_pods", save_as: pods}
playbook: |
  Ask which namespace, then list the pods.
`

func TestParseSkillReadsTheWholeFile(t *testing.T) {
	s, err := ParseSkill([]byte(wholeSkill), yaml.Unmarshal)
	require.NoError(t, err)

	assert.Equal(t, "2.1.0", s.EngineVersion)
	assert.Equal(t, "1.2.0", s.SkillVersion)
	assert.Equal(t, "pods", s.Name)
	assert.Equal(t, []string{"what is running in backend"}, s.TriggerExamples)
	assert.Equal(t, []string{"staging", "prod"}, s.Servers)
	assert.Equal(t, []string{"render_diagram"}, s.BuiltinTools)
	assert.Equal(t, "reader", s.Role)
	assert.Equal(t, "leaf", s.Kind)
	require.NotNil(t, s.Temperature)
	assert.InDelta(t, 0.3, *s.Temperature, 0.001)
	assert.Equal(t, "platform", s.Meta["owner"])

	require.True(t, s.HasWorkflow(), "the steps did not survive the parse")
	require.True(t, s.HasPlaybook())
	assert.Equal(t, "staging:get_pods", s.Workflow.Steps[0].Call.Tool)

	require.NoError(t, s.Validate())
	mode, err := s.ResolveMode()
	require.NoError(t, err)
	assert.Equal(t, ModeWorkflow, mode, "an explicit mode decides when both halves are there")
}

func TestParseSkillNeedsAnUnmarshalFunc(t *testing.T) {
	_, err := ParseSkill([]byte(wholeSkill), nil)
	require.Error(t, err, "parsing without a parser must say so rather than return an empty skill")
}

// A `workflow:` key with nothing under it is not a description: without this
// the mode table would read it as "there are steps" and run a turn that does
// nothing, which looks exactly like a turn that worked.
func TestEmptyWorkflowIsNoDescription(t *testing.T) {
	s, err := ParseSkill([]byte("name: x\ndescription: d\nworkflow:\n  tools: [a]\nplaybook: do it\n"), yaml.Unmarshal)
	require.NoError(t, err)
	assert.False(t, s.HasWorkflow())

	mode, err := s.ResolveMode()
	require.NoError(t, err)
	assert.Equal(t, ModePlaybook, mode)
}

// current — the version line every skill must carry: without it the file reads
// as 1.0.0, which this engine refuses outright.
const current = "skill_engine_version: \"2.1.0\"\n"

func TestSkillValidateRefuses(t *testing.T) {
	for _, c := range []struct {
		name, src, want string
	}{
		{"no name", current + "description: d\nplaybook: x\n", "without a name"},
		{"a name with capitals", current + "name: Pods\ndescription: d\nplaybook: x\n", "want lowercase"},
		{"a name ending in a dash", current + "name: pods-\ndescription: d\nplaybook: x\n", "want lowercase"},
		{"no description", current + "name: pods\nplaybook: x\n", "no description"},
		{"no version at all", "name: pods\ndescription: d\nplaybook: x\n", "different major"},
		{"a foreign major", "skill_engine_version: \"9.0.0\"\nname: pods\ndescription: d\nplaybook: x\n", "different major"},
		{"describes no turn", current + "name: pods\ndescription: d\n", "describes no turn"},
		{"a mode with the empty half", current + "name: pods\ndescription: d\nmode: workflow\nplaybook: x\n", "no steps"},
		{"a broken workflow", current + "name: pods\ndescription: d\nworkflow:\n  steps:\n    - name: s\n", "does nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := ParseSkill([]byte(c.src), yaml.Unmarshal)
			require.NoError(t, err, "the file itself is legal YAML — the defect is in what it says")
			err = s.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// A skill in playbook mode still has its steps checked. The half you cannot
// switch to is the failure `mode` exists to prevent: the author switches back,
// the run falls over, and nothing said so while the file was being saved.
func TestSkillValidatesTheWorkflowEvenInPlaybookMode(t *testing.T) {
	s, err := ParseSkill([]byte(current+`name: pods
description: d
mode: playbook
playbook: ask, then list
workflow:
  steps:
    - name: broken
      for_each: {in: "{{parts}}", as: p, steps: [{set: {var: a, value: b}}]}
`), yaml.Unmarshal)
	require.NoError(t, err)

	err = s.Validate()
	require.Error(t, err, "the description that is one edit away from running was not checked")
	assert.Contains(t, err.Error(), "variable NAME")
}

// The name rule is enforced in two places — here and in the schema an editor
// runs. Two spellings of one rule mean a name that passes in one place and
// fails in the other, and the author finds out from CI.
func TestSkillNamePatternMatchesTheSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Pattern string `yaml:"pattern"`
		} `yaml:"properties"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(SchemaYAML), &schema))

	want := schema.Properties["name"].Pattern
	require.NotEmpty(t, want, "the schema stopped constraining the name")
	assert.Equal(t, want, skillNameRE.String())
}

// A field the schema describes and the struct does not have is dropped on load
// without a word: the file declares it, an editor autocompletes it, the engine
// never sees it. The schema closes its top level (`additionalProperties:
// false`), so the two lists must match exactly, both ways.
func TestSkillCoversEverySchemaField(t *testing.T) {
	var schema struct {
		Properties map[string]yaml.Node `yaml:"properties"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(SchemaYAML), &schema))
	require.NotEmpty(t, schema.Properties)

	inStruct := map[string]bool{}
	typ := reflect.TypeOf(Skill{})
	for i := range typ.NumField() {
		tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		if tag != "" && tag != "-" {
			inStruct[tag] = true
		}
	}

	for field := range schema.Properties {
		assert.Truef(t, inStruct[field], "the schema describes `%s` and Skill has no field for it — "+
			"a skill declaring it would load with the field silently dropped", field)
	}
	for field := range inStruct {
		_, ok := schema.Properties[field]
		assert.Truef(t, ok, "Skill has `%s` and the schema does not describe it — "+
			"the schema closes its top level, so such a file would not validate", field)
	}
}

// The examples are read as whole skill FILES here, not just as flows: that is
// how an embedder loads them, and a header that stopped validating would go
// unnoticed by a test that only looks at the steps.
func TestExamplesLoadAsSkills(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var skills int
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw := mustRead(t, path)
			s, err := ParseSkill(raw, yaml.Unmarshal)
			require.NoError(t, err)
			if s.Name == "" {
				return // a vocabulary of values, not a skill
			}
			skills++
			require.NoError(t, s.Validate())
			_, err = s.ResolveMode()
			require.NoError(t, err)
		})
	}
	assert.NotZero(t, skills)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}
