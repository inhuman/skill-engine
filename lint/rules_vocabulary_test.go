package lint_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/inhuman/skill-engine/lint"
)

// The library ships no words. A rule that needs them and was not given any must
// SAY so — an unconfigured rule that quietly reports nothing is the same as a
// clean skill to anyone reading the findings.
func TestWordRulesSkipLoudlyWhenUnconfigured(t *testing.T) {
	opts := testOptions()
	opts.EmptyWords = nil
	opts.FreeTextFields = nil

	rep, err := lint.Lint([]byte(wf(`  tools: ["wiki"]
  steps:
    - name: understand
      instruction: |-
        namespace — the namespace. Not named — an empty string.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [namespace]
        properties:
          namespace: {type: string}
          summary: {type: string}
`)), testFacts(), opts)
	require.NoError(t, err)

	requireQuiet(t, rep, "W16")
	skipped := ""
	for _, s := range rep.Skipped {
		skipped += s + " "
	}
	assert.Contains(t, skipped, "W16")
	assert.Contains(t, skipped, "W13")
	assert.NotNil(t, find(rep, lint.SkipRule), "a caller reading findings alone must see the gap")
}

// The half of W13 that needs no vocabulary keeps working: a string inside an
// ARRAY can run away whatever it is called, in any language.
func TestFreeTextInsideArrayNeedsNoVocabulary(t *testing.T) {
	opts := testOptions()
	opts.FreeTextFields = nil

	rep, err := lint.Lint([]byte(wf(`  tools: ["wiki"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [findings]
        properties:
          findings:
            type: array
            items:
              type: object
              required: [raison]
              properties:
                raison: {type: string}
`)), testFacts(), opts)
	require.NoError(t, err)
	f := requireFinding(t, rep, "W13", lint.SeverityWarn)
	assert.Contains(t, f.Message, "raison", "a field named in another language is still inside an array")
}

// The words are the embedder's, so they work in whatever language they are
// given — the match is anchored at a word start by the same code the `contains`
// condition uses, so there is one definition of "starts a word", not two.
func TestEmptyWordsInAnyLanguage(t *testing.T) {
	opts := testOptions()
	opts.EmptyWords = []string{"vide"}

	rep, err := lint.Lint([]byte(wf(`  tools: ["wiki"]
  steps:
    - name: understand
      instruction: |-
        espace — le namespace. Non nommé — une chaîne vide.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [espace]
        properties:
          espace: {type: string}
`)), testFacts(), opts)
	require.NoError(t, err)
	requireFinding(t, rep, "W16", lint.SeverityError)

	// And the word-start rule still holds: a stem inside another word is not a
	// match, which is the failure the anchoring exists for.
	opts.EmptyWords = []string{"пуст"}
	rep, err = lint.Lint([]byte(wf(`  tools: ["wiki"]
  steps:
    - name: understand
      instruction: |-
        mode — «exec», если просят перезапустить сервис.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [mode]
        properties:
          mode: {type: string}
`)), testFacts(), opts)
	require.NoError(t, err)
	requireQuiet(t, rep, "W16")
}
