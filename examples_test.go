package skillengine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

// readWorkflow pulls the flow description out of an example file.
//
// The node is taken BY VALUE, not by pointer: yaml.v3 does not fill in a
// *yaml.Node when decoding into a struct — the field stays non-nil but empty,
// and the description is silently lost.
func readWorkflow(t *testing.T, path string) (se.Flow, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc struct {
		Workflow yaml.Node `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.Workflow.Kind == 0 {
		return se.Flow{}, false // a vocabulary of values, not a skill
	}
	var f se.Flow
	require.NoError(t, doc.Workflow.Decode(&f), "the description does not parse")
	return f, true
}

// The examples are part of the contract: people read the format from them. An
// example that stopped parsing is worse than a missing one — it teaches the
// wrong thing.
func TestExamplesParseAndValidate(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "the examples are gone")

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, ok := readWorkflow(t, path)
			if !ok {
				return
			}
			require.NoError(t, f.Validate(), "the description does not pass validation")
			assert.NotEmpty(t, f.Steps)
		})
	}
}

// Every example must declare the format version the engine actually speaks:
// a skill of another major is refused, so an example carrying a stale version
// teaches a file that will not run.
func TestExamplesDeclareCurrentFormat(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("examples", "*.yaml"))
	require.NoError(t, err)

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var doc struct {
				Name    string `yaml:"name"`
				Version string `yaml:"skill_engine_version"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &doc))
			if doc.Name == "" {
				return // a vocabulary of values, not a skill
			}
			require.NoError(t, se.CheckEngineVersion(doc.Version))
		})
	}
}

// An example must WORK, not merely parse: steps reach the executors, the server
// is computed, the answer arrives.
func TestExamplePodsRuns(t *testing.T) {
	f, ok := readWorkflow(t, filepath.Join("examples", "pods.yaml"))
	require.True(t, ok)

	answers := map[string]string{
		"understand": `{"cluster":"staging","namespace":"backend"}`,
		"report":     "api-7f9 → Running",
	}
	var asked [][]string
	runner := se.RunnerFunc(func(_ context.Context, req se.StepRequest) (se.Result, error) {
		asked = append(asked, req.Tools)
		return se.Result{Text: answers[req.Name]}, nil
	})
	var calledOn string
	caller := se.ToolCallerFunc(func(_ context.Context, server, _ string, _ map[string]any) (string, error) {
		calledOn = server
		return "api-7f9 Running", nil
	})

	vars, _, err := se.ExecuteWith(t.Context(), &f, se.Deps{Runner: runner, Caller: caller},
		map[string]string{"input": "show me the pods"})
	require.NoError(t, err)

	assert.Equal(t, "staging", calledOn, "the server was computed from parsing the request")
	assert.Equal(t, "api-7f9 → Running", vars[se.AnswerVar], "the turn's answer comes from the last step")
	for _, tools := range asked {
		assert.Empty(t, tools, "the parse and answer steps were handed no tools")
	}
}
