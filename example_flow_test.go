package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A whole live skill (the structure of a real one, names made generic — the
// package is designed to be detachable). What is checked is that the form
// EXPRESSES the skill: four guards that would be requests in words are
// properties of steps here.
const exampleFlow = `
tools: ["code_search", "read_file", "list_tree"]
steps:
  - name: classify
    instruction: "Determine the resource owner. Answer in one word: internal or foreign."
    tools: []
    save_as: owner

  - name: extract
    instruction: "Return the resource name or an empty string."
    tools: []
    save_as: resource

  - set:
      var: doc_path
      value: "docs/resources/{{resource}}.md"

  - switch:
      var: owner
      cases:
        foreign:
          - name: answer_from_knowledge
            instruction: "Answer from general knowledge."
            tools: []
            save_as: answer
        internal:
          - name: search_schema
            instruction: "Find the schema of {{resource}}. Empty or an error — return MISS."
            tools: ["code_search"]
            max_calls: 1
            save_as: hit
            on_error: continue
          - if:
              cond: "hit == MISS"
              then:
                - name: read_doc
                  instruction: "Read {{doc_path}}. Not found — return MISS."
                  tools: ["read_file"]
                  max_calls: 1
                  save_as: hit
                  on_error: continue
          - if:
              cond: "hit == MISS"
              then:
                - name: walk_tree
                  instruction: "Walk the tree and find the schema file."
                  tools: ["list_tree"]
                  max_calls: 8
                  save_as: hit
                  on_error: continue
          - name: answer_from_schema
            instruction: "Answer from what was found ({{hit}}). Not a single claim without a line from the results."
            tools: []
            save_as: answer
      default:
        - name: ask_again
          instruction: "Ask for the resource name again."
          tools: []
          save_as: answer
`

func TestExampleFlow_ForeignBranchGetsNoTools(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))
	require.NoError(t, f.Validate())

	r := &fakeRunner{answer: map[string]string{
		"classify": "foreign", "extract": "aws_instance",
		"answer_from_knowledge": "an answer from general knowledge",
	}}
	vars, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "an answer from general knowledge", vars["answer"])

	// The main check: for a foreign resource the repository was PHYSICALLY not
	// visited. Otherwise this would read as "do not go to the repo for this,
	// you will waste calls".
	for _, s := range r.seen {
		assert.Empty(t, s.Tools, "step %q got tools even though the branch hands out none", s.Name)
	}
	assert.Len(t, r.seen, 3, "classify, extract, answer — and not a single trip for data")
}

func TestExampleFlow_InternalFallbackCascade(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))

	r := &fakeRunner{answer: map[string]string{
		"classify": "internal", "extract": "vpc_vip",
		"search_schema": "MISS", "read_doc": "MISS",
		"walk_tree":          "found internal/schema.go: l2_enabled (bool, optional)",
		"answer_from_schema": "l2_enabled is a boolean, optional",
	}}
	vars, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "l2_enabled is a boolean, optional", vars["answer"])

	byName := map[string]StepRequest{}
	for _, s := range r.seen {
		byName[s.Name] = s
	}
	// The limits DIFFER per step — the very reason a dedicated field was
	// needed: one attempt for a code search, up to eight for walking a tree.
	assert.Equal(t, 1, byName["search_schema"].MaxCalls)
	assert.Equal(t, 8, byName["walk_tree"].MaxCalls)

	// The documentation path was computed by code, not "derived" by the model.
	assert.Contains(t, byName["read_doc"].Instruction, "docs/resources/vpc_vip.md")

	// Every step saw exactly its own tool.
	assert.Equal(t, []string{"code_search"}, byName["search_schema"].Tools)
	assert.Equal(t, []string{"read_file"}, byName["read_doc"].Tools)
	assert.Equal(t, []string{"list_tree"}, byName["walk_tree"].Tools)
	assert.Empty(t, byName["answer_from_schema"].Tools, "the final answer comes without tools")
}

func TestExampleFlow_FirstHitSkipsFallbacks(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))

	r := &fakeRunner{answer: map[string]string{
		"classify": "internal", "extract": "vpc_vip",
		"search_schema":      "l2_enabled bool optional",
		"answer_from_schema": "done",
	}}
	_, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	for _, s := range r.seen {
		assert.NotEqual(t, "read_doc", s.Name, "found it right away — the fallbacks are left alone")
		assert.NotEqual(t, "walk_tree", s.Name)
	}
	assert.Len(t, r.seen, 4, "classify, extract, search, answer — no fallbacks")
}
