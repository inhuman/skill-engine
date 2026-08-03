package skillengine

import (
	"context"
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

	got, ok := CondVar("input contains десерт | сладк")
	require.True(t, ok, "a reader of the description must get the variable from the engine")
	assert.Equal(t, "input", got)
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
