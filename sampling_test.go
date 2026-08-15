package skillengine

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samplingCases — one YAML line per field of Sampling, derived from the TYPE.
//
// The enumeration is reflective on purpose, and it is the whole point of the
// test rather than a convenience: the defect being fixed is that a parameter
// the engine does not read disappears in silence, and a hand-written list of
// seven names would leave the EIGHTH to disappear exactly the same way. The
// list has to be remembered; the type cannot be forgotten.
//
// A value is invented per kind, because the check is about the key being seen
// at all — what the number means is the executor's business.
func samplingCases(t *testing.T) map[string]string {
	t.Helper()
	typ := reflect.TypeOf(Sampling{})
	out := map[string]string{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		require.NotEmptyf(t, name, "field %s of Sampling has no yaml name", f.Name)

		k := f.Type.Kind()
		if k == reflect.Ptr {
			k = f.Type.Elem().Kind()
		}
		switch k {
		case reflect.Float32, reflect.Float64:
			out[name] = "0.5"
		case reflect.Int, reflect.Int64:
			out[name] = "8000"
		case reflect.String:
			out[name] = "low"
		case reflect.Bool:
			out[name] = "true"
		default:
			t.Fatalf("field %s of Sampling is a %s, and this test has no value for that kind", f.Name, k)
		}
	}
	require.NotEmpty(t, out)
	return out
}

// EVERY generation parameter written on a step is refused, and the refusal
// names both the parameter and where it goes.
//
// The silence this replaces was measured: in a live catalogue of 29 skills, of
// ten declarations of a token ceiling NINE were written on the step and had
// never worked a single day. Not one author's slip — the files were written by
// hand by someone who knew the format, and a skill-writing model produces the
// same shape.
func TestSamplingFieldOnAStepIsRefused(t *testing.T) {
	for name, value := range samplingCases(t) {
		t.Run(name, func(t *testing.T) {
			f := parseFlow(t, fmt.Sprintf(`
steps:
  - name: write_fix
    instruction: fix it
    tools: []
    %s: %s
`, name, value))
			err := f.Validate()
			require.Errorf(t, err, "`%s` on a step was accepted and would have been dropped in silence", name)
			assert.Contains(t, err.Error(), name, "the refusal must name the parameter")
			assert.Contains(t, err.Error(), "sampling: {"+name+": ",
				"the refusal must show where it goes, with the author's own value")
			assert.Contains(t, err.Error(), "write_fix", "the refusal must name the step")
		})
	}
}

// The same one level up: a profile keeps its parameters in `sampling` too.
func TestSamplingFieldOnAProfileIsRefused(t *testing.T) {
	for name, value := range samplingCases(t) {
		t.Run(name, func(t *testing.T) {
			f := parseFlow(t, fmt.Sprintf(`
profiles:
  classifier:
    model: small/model
    %s: %s
steps:
  - name: understand
    profile: classifier
    instruction: understand it
`, name, value))
			err := f.Validate()
			require.Errorf(t, err, "`%s` in a profile was accepted", name)
			assert.Contains(t, err.Error(), "classifier")
			assert.Contains(t, err.Error(), "sampling: {"+name+": ")
		})
	}
}

// The place it is supposed to be written keeps working — otherwise the check
// would be refusing the format instead of a mistake in it.
func TestSamplingInsideItsBlockIsFine(t *testing.T) {
	for name, value := range samplingCases(t) {
		t.Run(name, func(t *testing.T) {
			f := parseFlow(t, fmt.Sprintf(`
steps:
  - name: write_fix
    instruction: fix it
    tools: []
    sampling: {%s: %s}
`, name, value))
			require.NoError(t, f.Validate())
			require.NotNil(t, f.Steps[0].Run.Sampling)
			assert.Nil(t, f.Steps[0].Run.Misplaced, "the block's own contents leaked into the trap")
		})
	}
}

// A step carrying nothing but a stray parameter gets the diagnosis it needs.
// Without the check running first it would be refused as "does nothing" —
// true, and no help to an author who wrote a ceiling.
func TestAStepThatIsOnlyAStrayParameterSaysWhy(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: lonely
    max_tokens: 8000
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens")
	assert.NotContains(t, err.Error(), "does nothing")
}

// Several at once are one refusal, not the first of several rounds: an author
// who moves one parameter and is sent back for the next learns the format one
// error per attempt.
func TestSeveralStrayParametersAreOneRefusal(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: write_fix
    instruction: fix it
    tools: []
    temperature: 0
    max_tokens: 8000
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temperature, max_tokens", "declaration order, so the message is stable")
	assert.Contains(t, err.Error(), "sampling: {temperature: 0, max_tokens: 8000}")
	assert.Contains(t, err.Error(), "are generation parameters")
}

// Zero is a SETTING, not an absence — which is why these fields are pointers.
// A `temperature: 0` that read as unset would be dropped in the silence this
// whole check exists to end, and dropped for the value a classifier step wants
// most.
func TestAZeroValueIsStillRefused(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: classify
    instruction: classify it
    tools: []
    temperature: 0
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sampling: {temperature: 0}")
}

// The trap must not swallow a key of the format itself: `model` sits beside
// `sampling` legitimately, and the two are easy to confuse from the outside.
func TestTheTrapCatchesOnlyGenerationParameters(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: write_fix
    instruction: fix it
    tools: []
    model: small/model
    max_calls: 3
    sampling: {temperature: 0}
`)
	require.NoError(t, f.Validate())
}

// Refused where the step is, not only at the top level: a description is
// nested, and a check that stops at the first level leaves the branches to the
// silence it was written against.
func TestStrayParametersAreRefusedInsideBranches(t *testing.T) {
	for _, src := range []string{`
steps:
  - if:
      cond: "mode == fast"
      then:
        - name: inner
          instruction: do it
          tools: []
          max_tokens: 8000
`, `
steps:
  - for_each:
      in: items
      as: item
      steps:
        - name: inner
          instruction: do it
          tools: []
          max_tokens: 8000
`} {
		err := parseFlow(t, src).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inner")
		assert.Contains(t, err.Error(), "sampling: {max_tokens: 8000}")
	}
}
