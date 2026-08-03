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
// markers — the decision markers of an IMAGINARY embedding application. The
// engine ships none: these are what a host passes in through
// Vocabulary.DecisionMarkers, and the tests below are about the mechanism, not
// about this particular list.
var markers = []string{"result:", "answer:", "summary:", "verdict:"}

func TestNormalizeOneOfOnLiveWordings(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, c := range []struct{ text, want, why string }{
		{"foreign", "foreign", "clean answer"},
		{"  t1\n", "t1", "with whitespace"},
		{"Summary: determined by prefix. Result: foreign", "foreign", "value after a marker"},
		{"Answer: t1", "t1", "another marker of the same list"},
		{"Not foreign, but t1", "", "both mentioned, no marker → ambiguous"},
		{"This is definitely t1, not foreign", "", "TRAP: the last occurrence would give foreign"},
		{"Choosing between t1 and foreign", "", "enumeration without a decision"},
		{"this is an internal t1 resource, check against the repo", "t1", "one value mentioned"},
		{"did not understand a thing", "", "no value at all"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed, markers), c.text)
		})
	}
}

// The markers belong to whoever embeds the engine, so they may be in any
// script — a model prompted in one language answers with that language's
// markers. What is tested here is that the mechanism does not care which.
func TestNormalizeOneOfMarkersInAnotherScript(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	markers := []string{"результат:", "ответ:"} // another application, another script
	for _, c := range []struct{ text, want, why string }{
		{"Итог: определил по префиксу. Результат: foreign", "foreign", "value after a marker"},
		{"Ответ: t1", "t1", "the `ответ` marker"},
		{"Это точно t1, а не foreign", "", "TRAP: trailing negation"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed, markers), c.text)
		})
	}
}

// "Not foreign, but t1" is the ambiguous case: both values are named and there
// is no marker. An empty result is more honest than a guess.
func TestNormalizeOneOfAmbiguousReturnsEmpty(t *testing.T) {
	assert.Empty(t, normalizeOneOf("Not foreign, but t1", []string{"t1", "foreign"}, markers))
}

func TestNormalizeOneOfNoConstraint(t *testing.T) {
	assert.Equal(t, "any text", normalizeOneOf("any text", nil, markers))
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
		if got := normalizeOneOf(c.in, allowed, markers); got != c.want {
			t.Errorf("normalizeOneOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Quotes around the answer: a model without a grammar answers with a JSON string.
func TestNormalizeOneOf_Quoted(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, in := range []string{`"foreign"`, "'foreign'", "`foreign`", ` "foreign" `} {
		if got := normalizeOneOf(in, allowed, markers); got != "foreign" {
			t.Errorf("normalizeOneOf(%q) = %q", in, got)
		}
	}
}

// Without markers the engine loses ONE of five ways to normalise an answer, and
// a narrow one: an exact match, a single value mentioned, and a value mentioned
// strictly more often all work in any language. Markers decide only a TIE —
// where the allowed values appear equally often and the answer is prose. There
// the result becomes EMPTY, never a guess, which is what the format does with
// "the model did not decide".
func TestNormalizeOneOfWithoutMarkers(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, c := range []struct{ text, want, why string }{
		{"t1", "t1", "an exact answer needs no words at all"},
		{`"foreign"`, "foreign", "quoted, still exact"},
		{"this is an internal t1 resource", "t1", "one value mentioned"},
		{"t1, definitely t1, not the other one", "t1", "one value mentioned more often"},
		{"Summary: chose by prefix. Result: foreign", "foreign", "prose naming ONE value still resolves"},
		{"Result: t1, not foreign", "", "a TIE is where markers would have decided — empty, never a guess"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed, nil), c.text)
		})
	}
}
