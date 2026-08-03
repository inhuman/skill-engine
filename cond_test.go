package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dictionary a live classifier step carries in its own text. Kept as test
// data because that is what the condition replaces: the mapping was always
// deterministic, the model was only applying it.
var sourceWords = map[string]string{
	"wiki":    "вики | wiki | confluence",
	"tickets": "тикет | задач | jira | жир",
	"cluster": "кластер | под | k8s | куб",
	"repo":    "репозитор | код | merge request | MR",
	"metrics": "метрик | latency | rps",
}

// The words a request names are what the branch must follow. A model at
// temperature 0 got 5 of 10 live requests right; the same dictionary applied
// here gets them all, because there is nothing to get wrong.
func TestContainsFindsWhatTheRequestNamed(t *testing.T) {
	for _, c := range []struct {
		request string
		want    []string
	}{
		{"поищи про релиз в вики и в жире", []string{"wiki", "tickets"}},
		{"собери всё про релиз: тикеты, мержи, вики", []string{"wiki", "tickets"}},
		{"что сломалось: логи подов, метрики и тикеты", []string{"tickets", "cluster", "metrics"}},
		{"look it up in Confluence", []string{"wiki"}},
		{"покажи latency за сутки", []string{"metrics"}},
	} {
		t.Run(c.request, func(t *testing.T) {
			var got []string
			for _, name := range []string{"wiki", "tickets", "cluster", "repo", "metrics"} {
				if containsAny(c.request, condAlternatives(sourceWords[name])) {
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
	assert.False(t, ContainsWord("сходи в репозиторий", "поз"),
		"a root found in the middle of a word")
	assert.False(t, ContainsWord("research the topic", "search"),
		"`search` found inside `research` — the same class in ASCII")
}

// A dictionary of ROOTS is what makes stemming unnecessary: the author picks
// the root, and the engine does not guess at a language it does not know. So a
// match is anchored at the word's START and free at its end.
func TestContainsFindsRoots(t *testing.T) {
	for _, form := range []string{"жиру", "жире", "жира", "жир"} {
		assert.Truef(t, ContainsWord("посмотри в "+form, "жир"), "the root did not find %q", form)
	}
	assert.True(t, ContainsWord("несколько тикетов", "тикет"))
	assert.True(t, ContainsWord("по метрикам за час", "метрик"))
}

// Requests are written by people, in whatever case and whatever script.
func TestContainsIsCaseInsensitiveBeyondASCII(t *testing.T) {
	assert.True(t, ContainsWord("СМОТРИ В ВИКИ", "вики"))
	assert.True(t, ContainsWord("смотри в вики", "ВИКИ"))
	assert.True(t, ContainsWord("open Jira please", "jira"))
	assert.True(t, ContainsWord("Ünicode Ärger", "ärger"))
}

// An alternative may be several words: «что мы знаем» is one entry of a live
// dictionary, not three.
func TestContainsHandlesMultiWordAlternatives(t *testing.T) {
	assert.True(t, ContainsWord("напомни, что мы знаем про сервис", "что мы знаем"))
	assert.False(t, ContainsWord("что мы знали", "что мы знаем"))
}

// An alternative that does not itself begin with a letter has no word start to
// anchor to, and must stay findable.
func TestContainsAnchorsOnlyWhereThereIsAWordStart(t *testing.T) {
	assert.True(t, ContainsWord("run it with -v please", "-v"))
	assert.True(t, ContainsWord("release 2.1.0 is out", "2.1.0"))
}

func TestConditionAlternatives(t *testing.T) {
	assert.Equal(t, []string{"вики", "wiki", "confluence"}, condAlternatives(" вики | wiki |confluence "))
	assert.Equal(t, []string{"что мы знаем", "граф"}, condAlternatives("что мы знаем | граф"))
	assert.Equal(t, []string{"a", "b"}, condAlternatives(`"a" | 'b'`), "quotes are stripped as in an equality")
	assert.Equal(t, []string{"a", "b"}, condAlternatives("a | | b"), "an empty piece is a slip, not a match-all")
	assert.Empty(t, condAlternatives("  |  "))
}

func TestParseContainsCondition(t *testing.T) {
	name, op, want, err := parseCond("input contains вики | wiki")
	require.NoError(t, err)
	assert.Equal(t, "input", name)
	assert.Equal(t, "contains", op)
	assert.Equal(t, []string{"вики", "wiki"}, condAlternatives(want))

	name, op, _, err = parseCond("req.text not contains жир")
	require.NoError(t, err)
	assert.Equal(t, "req.text", name)
	assert.Equal(t, "not contains", op)

	got, ok := CondVar("input contains вики | wiki")
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
      cond: "input contains вики | wiki | confluence"
      then:
        - set: {var: sources, value: "wiki"}
      else:
        - set: {var: sources, value: "none"}
  - name: answer
    when: "input contains тикет | жир | jira"
    set: {var: also, value: "tickets"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"input": "поищи про релиз в Вики и в жире"})
	require.NoError(t, err)
	assert.Equal(t, "wiki", vars["sources"])
	assert.Equal(t, "tickets", vars["also"], "`when` reads the same grammar")
}

func TestNotContainsBranches(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "input not contains вики | wiki"
      then:
        - set: {var: route, value: "ask"}
      else:
        - set: {var: route, value: "wiki"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"input": "что там по метрикам"})
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
      cond: "report contains кластер"
      then:
        - set: {var: found, value: "yes"}
      else:
        - set: {var: found, value: "no"}
`)
	vars, _, err := ExecuteWith(context.Background(), f,
		Deps{Memory: fakeMemory{"res-1": "первая строка\nупал кластер staging\nхвост"}},
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
