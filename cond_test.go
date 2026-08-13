package skillengine

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dictionary of the kind a classifier step carries in its own text. The
// domain is deliberately not this library's: the words belong to whoever writes
// the skill, and an agent about a kitchen, a car fleet or a warehouse will
// share none of them. What is tested is the mechanism.
var courseWords = map[string]string{
	"dessert": "десерт | сладк | пирог | dessert",
	"main":    "горяч | второе | main course",
	"drink":   "напит | коктейл | чай | чаю | чая | кофе | drink",
	"starter": "закус | салат | starter",
}

// The words a request names are what the branch must follow. This is the whole
// value of the construct: a step that used to ask a model to apply a mapping it
// already carried now applies it itself, and cannot get it wrong.
func TestContainsFindsWhatTheRequestNamed(t *testing.T) {
	for _, c := range []struct {
		request string
		want    []string
	}{
		{"подбери десерт и напиток", []string{"dessert", "drink"}},
		{"что на второе, и салат бы", []string{"main", "starter"}},
		{"хочу что-нибудь сладкое к чаю", []string{"dessert", "drink"}},
		{"посоветуй что приготовить", nil},
		{"a dessert, please", []string{"dessert"}},
	} {
		t.Run(c.request, func(t *testing.T) {
			var got []string
			for _, name := range []string{"dessert", "main", "drink", "starter"} {
				if containsAny(c.request, condAlternatives(courseWords[name])) {
					got = append(got, name)
				}
			}
			assert.ElementsMatch(t, c.want, got)
		})
	}
}

// The failure this rule exists to prevent, twice over. Both were live findings
// that looked genuine: a word found INSIDE another word.
func TestContainsDoesNotMatchInsideAWord(t *testing.T) {
	assert.False(t, ContainsWord("просят перезапустить сервис", "пуст"),
		"«пуст» found inside «перезапустить» — the burn this default exists for")
	assert.False(t, ContainsWord("подай десерт", "дай"),
		"a root found in the middle of a word")
	assert.False(t, ContainsWord("research the topic", "search"),
		"`search` found inside `research` — the same class in ASCII")
}

// A dictionary of ROOTS is what makes stemming unnecessary: the author picks
// the root, and the engine does not guess at a language it does not know. So a
// match is anchored at the word's START and free at its end.
func TestContainsFindsRoots(t *testing.T) {
	for _, form := range []string{"заказ", "заказы", "заказа", "заказу", "заказами"} {
		assert.Truef(t, ContainsWord("посмотри "+form, "заказ"), "the root did not find %q", form)
	}
	assert.True(t, ContainsWord("несколько десертов", "десерт"))
	assert.True(t, ContainsWord("два коктейля", "коктейл"))
}

// Requests are written by people, in whatever case and whatever script.
func TestContainsIsCaseInsensitiveBeyondASCII(t *testing.T) {
	assert.True(t, ContainsWord("ХОЧУ ДЕСЕРТ", "десерт"))
	assert.True(t, ContainsWord("хочу десерт", "ДЕСЕРТ"))
	assert.True(t, ContainsWord("a Dessert please", "dessert"))
	assert.True(t, ContainsWord("Ünicode Ärger", "ärger"))
}

// An alternative may be several words: «что мы знаем» is one entry of a live
// dictionary, not three.
func TestContainsHandlesMultiWordAlternatives(t *testing.T) {
	assert.True(t, ContainsWord("напомни, что мы брали в прошлый раз", "что мы брали"))
	assert.False(t, ContainsWord("что мы брали бы", "что мы брали в"))
}

// An alternative that does not itself begin with a letter has no word start to
// anchor to, and must stay findable.
func TestContainsAnchorsOnlyWhereThereIsAWordStart(t *testing.T) {
	assert.True(t, ContainsWord("run it with -v please", "-v"))
	assert.True(t, ContainsWord("release 2.1.0 is out", "2.1.0"))
}

func TestConditionAlternatives(t *testing.T) {
	assert.Equal(t, []string{"десерт", "сладкое", "dessert"}, condAlternatives(" десерт | сладкое |dessert "))
	assert.Equal(t, []string{"что мы брали", "заказ"}, condAlternatives("что мы брали | заказ"))
	assert.Equal(t, []string{"a", "b"}, condAlternatives(`"a" | 'b'`), "quotes are stripped as in an equality")
	assert.Equal(t, []string{"a", "b"}, condAlternatives("a | | b"), "an empty piece is a slip, not a match-all")
	assert.Empty(t, condAlternatives("  |  "))
}

func TestParseContainsCondition(t *testing.T) {
	name, op, want, err := parseCond("input contains десерт | сладк")
	require.NoError(t, err)
	assert.Equal(t, "input", name)
	assert.Equal(t, "contains", op)
	assert.Equal(t, []string{"десерт", "сладк"}, condAlternatives(want))

	name, op, _, err = parseCond("req.text not contains заказ")
	require.NoError(t, err)
	assert.Equal(t, "req.text", name)
	assert.Equal(t, "not contains", op)

	got, ok := CondVars("input contains десерт | сладк")
	require.True(t, ok, "a reader of the description must get the variable from the engine")
	assert.Equal(t, []string{"input"}, got)
}

// A condition with nothing to look for can never fire, and a branch that can
// never run is not a branch — it is a hole the author cannot see. Refused at
// parse time, so Validate refuses the skill.
func TestParseCondRefusesEmptyAlternatives(t *testing.T) {
	for _, cond := range []string{"input contains", "input contains   ", "input contains | |"} {
		_, _, _, err := parseCond(cond)
		require.Errorf(t, err, "%q was accepted", cond)
		assert.Contains(t, err.Error(), "without anything to look for")
	}
}

// An equality whose value happens to be the word `contains` is still an
// equality: the new form must not swallow the old one.
func TestContainsDoesNotSwallowEquality(t *testing.T) {
	_, op, want, err := parseCond("mode == contains")
	require.NoError(t, err)
	assert.Equal(t, "==", op)
	assert.Equal(t, "contains", want)
}

// The point of the whole thing: a step that used to be a model call becomes an
// assignment, and the branch follows the words the request actually used.
func TestContainsBranchesInAFlow(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "input contains десерт | сладк"
      then:
        - set: {var: course, value: "dessert"}
      else:
        - set: {var: course, value: "none"}
  - name: answer
    when: "input contains напит | чай | кофе"
    set: {var: also, value: "drink"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"input": "хочу Десерт и чай"})
	require.NoError(t, err)
	assert.Equal(t, "dessert", vars["course"])
	assert.Equal(t, "drink", vars["also"], "`when` reads the same grammar")
}

func TestNotContainsBranches(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "input not contains десерт | сладк"
      then:
        - set: {var: route, value: "ask"}
      else:
        - set: {var: route, value: "dessert"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"input": "что на второе"})
	require.NoError(t, err)
	assert.Equal(t, "ask", vars["route"])
}

// A condition is DATA the flow reads, not text shown to the model, so it reads
// the whole value. A large result lives in a variable as a preview plus a
// handle: a condition looking at the preview would answer about the first few
// hundred bytes and look exactly as if it had answered about the value.
func TestConditionReadsTheWholeValue(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "report contains пригорел"
      then:
        - set: {var: found, value: "yes"}
      else:
        - set: {var: found, value: "no"}
`)
	vars, _, err := ExecuteWith(context.Background(), f,
		Deps{Memory: fakeMemory{"res-1": "первая строка\nсоус пригорел\nхвост"}},
		map[string]string{"report": "первая строка\n[mem:res-1]"})
	require.NoError(t, err)
	assert.Equal(t, "yes", vars["found"], "the condition judged the preview instead of the value")
}

// The live case, in the shape it arrived in: a threshold inside a loop. Both
// refusals that produced this form were a condition in the body of a for_each —
// "keep the ones over the threshold" is written as a loop with a branch, which
// is why a loop plus a comparison covers it and a collection filter is not
// needed.
func TestNumericConditionPicksItemsOverAThreshold(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: process_pods
    for_each:
      in: pods
      as: pod
      collect: hot
      steps:
        - name: check_restart
          if:
            cond: "pod.restartCount > 5"
            then:
              - set: {var: hot, value: "{{pod.name}}"}
`)
	require.NoError(t, f.Validate())
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"pods": `[{"name":"api","restartCount":12},{"name":"web","restartCount":1},{"name":"db","restartCount":6}]`,
	})
	require.NoError(t, err)
	assert.Equal(t, "api\n\ndb", vars["hot"])
}

// A threshold is almost never a literal: it arrives from the step that parsed
// the request. Without variable-against-variable the live case does not close.
func TestNumericComparisonTakesAThresholdFromAVariable(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "stale_days >= req.threshold"
      then:
        - set: {var: verdict, value: "stale"}
      else:
        - set: {var: verdict, value: "fresh"}
`)
	for _, c := range []struct{ days, want string }{{"14", "stale"}, {"7", "stale"}, {"6", "fresh"}} {
		vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
			"stale_days": c.days,
			"req":        `{"threshold": 7}`,
		})
		require.NoError(t, err)
		assert.Equal(t, c.want, vars["verdict"], "stale_days = %s", c.days)
	}
}

// Compared as NUMBERS, not as the text they are written in: by text `9 > 10`.
func TestNumbersAreComparedAsNumbers(t *testing.T) {
	for _, c := range []struct {
		cond string
		vars map[string]string
		want bool
	}{
		{"count > limit", map[string]string{"count": "9", "limit": "10"}, false},
		{"count < limit", map[string]string{"count": "9", "limit": "10"}, true},
		{"count > 5", map[string]string{"count": " 12\n"}, true},
		{"share <= 0.5", map[string]string{"share": "0.25"}, true},
		{"delta > -1", map[string]string{"delta": "0"}, true},
		{"count >= 5", map[string]string{"count": "5"}, true},
	} {
		t.Run(c.cond, func(t *testing.T) {
			s := &state{vars: c.vars}
			got, err := s.eval(c.cond)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// An id is a number a flow compares, and a nineteen-digit one does not survive
// float64: two different ids come out equal, and the branch is confidently
// wrong with nothing to see.
func TestLargeIdsCompareExactly(t *testing.T) {
	s := &state{vars: map[string]string{"id": "9007199254740993"}}
	got, err := s.eval("id > 9007199254740992")
	require.NoError(t, err)
	assert.True(t, got, "the two ids differ by one and came out equal — float64 precision")
}

// An empty variable is NOT zero. A step that returned nothing leaves `restarts`
// empty, and answering the comparison would make "no data" indistinguishable
// from "few restarts" — the failure this whole grammar exists to make loud.
func TestEmptyIsNotZero(t *testing.T) {
	for _, v := range []string{"", "   ", "ERROR: the tool fell over"} {
		s := &state{vars: map[string]string{"restarts": v}}
		_, err := s.eval("restarts > 5")
		require.Errorf(t, err, "%q was compared as if it were a number", v)
		assert.Contains(t, err.Error(), "is NOT zero")
		assert.Contains(t, err.Error(), "restarts is not empty", "the error must say how to allow emptiness")
	}
	// A missing variable is the same case: the engine resolves an unknown name
	// to emptiness, and that is exactly where a silent `false` would hide.
	s := &state{vars: map[string]string{}}
	_, err := s.eval("restarts > 5")
	require.Error(t, err)
}

// A non-number is a loud refusal rather than `false`: a condition is the one
// place where a wrong answer is invisible — `restarts > 5` looks right whatever
// it returns.
func TestANonNumberStopsTheTurn(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "restarts > 5"
      then:
        - set: {var: verdict, value: "hot"}
      else:
        - set: {var: verdict, value: "calm"}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"restarts": "many, honestly"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a number")
	assert.Contains(t, err.Error(), `"many, honestly"`, "the error must show the value it choked on")
}

// A value large enough to be a whole tool result is CLIPPED in the message: an
// error that prints it in full buries the sentence saying what is wrong.
func TestTheRefusalDoesNotPrintTheWholeValue(t *testing.T) {
	s := &state{vars: map[string]string{"n": strings.Repeat("nope ", 200)}}
	_, err := s.eval("n > 5")
	require.Error(t, err)
	assert.Less(t, len(err.Error()), 200)
	assert.Contains(t, err.Error(), "…")
}

// A threshold that is neither a number nor a name can never work, so it is
// refused when the file is loaded rather than when the branch is reached.
func TestValidateRefusesANonNumericThreshold(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "count > пять"
      then:
        - set: {var: a, value: b}
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a number or the name of a variable")
}

// The form a model actually writes. Braces are refused — a field that holds a
// NAME holds one spelling of it — but the message must name the braces, not
// list the allowed shapes and leave the author to spot that their string
// differs from one of them by exactly two pairs of them.
func TestBracesInAConditionAreNamedInTheRefusal(t *testing.T) {
	_, _, _, err := parseCond("{{pod.restartCount}} > 5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without {{ }}")
	assert.Contains(t, err.Error(), "`pod.restartCount > 5`", "the refusal must print the condition that works")

	// Braces on both sides, and the older forms too: the diagnosis is about the
	// grammar, not about the new operators.
	_, _, _, err = parseCond("{{stale}} >= {{req.limit}}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`stale >= req.limit`")
	_, _, _, err = parseCond("{{mode}} == fast")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`mode == fast`")

	// Two mistakes at once: fixing the visible one would send the author round
	// again, so the refusal carries both.
	_, _, _, err = parseCond("{{count}} > пять")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without {{ }}")
	assert.Contains(t, err.Error(), "must be a number or the name of a variable")
}

// Equality stays TEXTUAL. Coercing it would silently change skills already
// written: `"5" == "5.0"` is false today, and the equalities in a live
// catalogue compare ids and sentinels.
func TestEqualityStaysTextual(t *testing.T) {
	s := &state{vars: map[string]string{"n": "5"}}
	got, err := s.eval("n == 5.0")
	require.NoError(t, err)
	assert.False(t, got)

	got, err = s.eval("n == 5")
	require.NoError(t, err)
	assert.True(t, got)
}

// A reader of a description — a linter, an editor, a visualiser — asks the
// engine which names a condition depends on. Only the left one would leave a
// typo in a threshold unchecked.
func TestCondVarsSeesBothSides(t *testing.T) {
	got, ok := CondVars("stale_days > req.threshold")
	require.True(t, ok)
	assert.Equal(t, []string{"stale_days", "req.threshold"}, got)

	got, ok = CondVars("stale_days > 7")
	require.True(t, ok)
	assert.Equal(t, []string{"stale_days"}, got, "a literal is not a variable")

	_, ok = CondVars("count > пять")
	assert.False(t, ok, "a condition that does not parse has no dependencies to report")
}

// `when` reads the same grammar, and the numeric forms are no exception.
func TestNumericConditionInWhen(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: warn
    when: "errors > 0"
    set: {var: note, value: "there are failures"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"errors": "0"})
	require.NoError(t, err)
	assert.Empty(t, vars["note"])

	vars, _, err = ExecuteWith(context.Background(), f, Deps{}, map[string]string{"errors": "3"})
	require.NoError(t, err)
	assert.Equal(t, "there are failures", vars["note"])
}

// The description must be refused BEFORE execution: a condition that does not
// parse is not a runtime surprise, it is a typo in a file.
func TestValidateRefusesABrokenContains(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "input contains"
      then:
        - set: {var: a, value: b}
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without anything to look for")
}
