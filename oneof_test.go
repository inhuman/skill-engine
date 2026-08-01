package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Normalising a classifier step's value on LIVE wordings.
//
// The previous version took the last occurrence and got "this is definitely t1,
// not foreign" wrong — a trailing negation is ordinary phrasing (and the default
// word order in Russian), so the guess produced the exact opposite answer. Now
// an ambiguous case returns empty: the switch falls through to default, where
// the skill can ask again instead of silently going the wrong way.
func TestNormalizeOneOfOnLiveWordings(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, c := range []struct{ text, want, why string }{
		{"foreign", "foreign", "clean answer"},
		{"  t1\n", "t1", "with whitespace"},
		{"Summary: determined by prefix. Result: foreign", "foreign", "value after a marker"},
		{"Answer: t1", "t1", "the `answer` marker"},
		{"Not foreign, but t1", "", "both mentioned, no marker → ambiguous"},
		{"This is definitely t1, not foreign", "", "TRAP: the last occurrence would give foreign"},
		{"Choosing between t1 and foreign", "", "enumeration without a decision"},
		{"this is an internal t1 resource, check against the repo", "t1", "one value mentioned"},
		{"did not understand a thing", "", "no value at all"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed), c.text)
		})
	}
}

// The decision markers are bilingual, and so is the coverage: a model prompted
// in Russian answers with Russian markers, and dropping them from the list would
// silently send every such step to default.
func TestNormalizeOneOfRussianMarkers(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, c := range []struct{ text, want, why string }{
		{"Итог: определил по префиксу. Результат: foreign", "foreign", "value after a marker"},
		{"Ответ: t1", "t1", "the `ответ` marker"},
		{"Это точно t1, а не foreign", "", "TRAP: trailing negation"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed), c.text)
		})
	}
}

// "Not foreign, but t1" is the ambiguous case: both values are named and there
// is no marker. An empty result is more honest than a guess.
func TestNormalizeOneOfAmbiguousReturnsEmpty(t *testing.T) {
	assert.Empty(t, normalizeOneOf("Not foreign, but t1", []string{"t1", "foreign"}))
}

func TestNormalizeOneOfNoConstraint(t *testing.T) {
	assert.Equal(t, "any text", normalizeOneOf("any text", nil))
}

// One allowed value is part of another (found ⊂ not_found). A plain occurrence
// count credited the answer to both, both layers saw a tie and returned nothing:
// the step silently ended up without a value. Live case: a search skill.
func TestNormalizeOneOf_OverlappingValues(t *testing.T) {
	allowed := []string{"found", "not_found"}
	for _, c := range []struct{ in, want string }{
		{"not_found", "not_found"},
		{`"not_found"`, "not_found"},
		{"found", "found"},
		{"Answer: not_found", "not_found"},
		{"no matching pages — not_found", "not_found"},
	} {
		if got := normalizeOneOf(c.in, allowed); got != c.want {
			t.Errorf("normalizeOneOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Quotes around the answer: a model without a grammar answers with a JSON string.
func TestNormalizeOneOf_Quoted(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, in := range []string{`"foreign"`, "'foreign'", "`foreign`", ` "foreign" `} {
		if got := normalizeOneOf(in, allowed); got != "foreign" {
			t.Errorf("normalizeOneOf(%q) = %q", in, got)
		}
	}
}
