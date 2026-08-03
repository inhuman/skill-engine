package skillengine

// Branch conditions: `var == value`, `var != value`, `var is [not] empty`,
// `var contains a | b | c`.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var condRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*(==|!=)\s*(.*?)\s*$`)

// emptyCondRe — the "step produced nothing" condition: `var is empty` /
// `var is not empty`.
//
// Why a dedicated form instead of comparing against "". A step that failed
// stores a marked failure (ERROR:/DENIED:), not an empty string — the POLICY
// needs to tell those apart, but to a "nothing found, try another way" branch
// they mean the same. This pattern showed up three times in a single live
// skill, and a meaning repeated in three places belongs to the engine rather
// than to the skill (same reasoning as ErrorPolicy).
var emptyCondRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s+is\s+(not\s+)?empty\s*$`)

// containsCondRe — the "the text names one of these" condition:
// `var contains a | b | c` / `var not contains a | b`.
//
// Why the format grew a third comparison. A classifier step whose whole job is
// "which of these words did the request use" carries the mapping IN ITS OWN
// TEXT — the decision is already deterministic, and the model is there only to
// apply it. Measured on ten live requests: the model at temperature 0 got 5 of
// 10, the same dictionary applied in code got 10 of 10, and rewording the
// instruction three ways did not move the ceiling. The misses were one kind:
// the model fell back to a default and dropped what the request had NAMED.
//
// Alternatives are separated by `|` because that is how "or" reads, and because
// an alternative may contain spaces («что мы знаем»). One condition per value,
// not one per synonym: with two to six synonyms apiece, six nested `if`s would
// be worse than the model call being replaced.
//
// The list may come out empty here on purpose: `input contains` with nothing
// after it is recognised as this form so the error can say what is missing,
// rather than falling through to "the condition does not parse".
var containsCondRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s+(not\s+)?contains\b\s*(.*?)\s*$`)

// isBlank — "the step produced nothing useful": empty or a failure marker.
func isBlank(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || strings.HasPrefix(t, "ERROR:") || strings.HasPrefix(t, "DENIED:")
}

// parseCond parses a branch condition. For `contains` the returned want is the
// raw alternative list, split by condAlternatives.
//
// The value of an equality may be empty (`var == `) — that tests for an
// unfilled variable. The alternative list of a `contains` may NOT: a condition
// with nothing to look for can never fire, and a branch that can never run is
// not a branch, it is a hole the author cannot see.
func parseCond(cond string) (name, op, want string, err error) {
	if m := emptyCondRe.FindStringSubmatch(cond); m != nil {
		op = "is empty"
		if strings.TrimSpace(m[2]) != "" {
			op = "is not empty"
		}
		return m[1], op, "", nil
	}
	if m := containsCondRe.FindStringSubmatch(cond); m != nil {
		op = "contains"
		if strings.TrimSpace(m[2]) != "" {
			op = "not contains"
		}
		if len(condAlternatives(m[3])) == 0 {
			return "", "", "", fmt.Errorf("condition %q: `contains` without anything to look for", cond)
		}
		return m[1], op, m[3], nil
	}
	m := condRe.FindStringSubmatch(cond)
	if m == nil {
		return "", "", "", fmt.Errorf("condition %q: want `var == value`, `var != value`, "+
			"`var is [not] empty` or `var contains a | b`", cond)
	}
	return m[1], m[2], strings.Trim(m[3], `"'`), nil
}

// condAlternatives splits the right-hand side of a `contains` into the words it
// looks for. Empty pieces are dropped: `a | | b` is a slip, not a request to
// match everything.
func condAlternatives(list string) []string {
	var out []string
	for _, part := range strings.Split(list, "|") {
		if w := strings.TrimSpace(strings.Trim(strings.TrimSpace(part), `"'`)); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// ContainsWord reports whether text names word — case-insensitively, and only
// where a WORD STARTS.
//
// Two decisions, both paid for by live failures.
//
// Case folding is Unicode-wide, not ASCII: the requests these conditions read
// are written by people, in whatever case and whatever script they use.
//
// The match must begin at a word start, and this is the DEFAULT rather than an
// option — the author of a skill will not think about it, while a false match
// is nearly impossible to debug because the condition looks right. Two live
// burns, both of them findings that looked genuine: «пуст» matched inside
// «перезапустить», and a rule about emptiness fired on a step about restarting.
// Note that Go's `\b` is ASCII-only and does not work on Cyrillic at all, which
// is why the check is written by hand.
//
// The END of the match is deliberately NOT anchored: an alternative matches a
// word that STARTS with it. That is what makes a dictionary of ROOTS work —
// «жир» finds «жиру», «жире», «жира» — and a dictionary of roots is why this
// engine needs no stemming, which would be a guess about a language it does not
// know. The cost is that a too-short root collides: «ком» finds «компонентах».
// That is a dictionary problem with a dictionary fix — a longer root — and the
// linter warns about an alternative made redundant by a shorter one.
func ContainsWord(text, word string) bool {
	hay, needle := strings.ToLower(text), strings.ToLower(strings.TrimSpace(word))
	if needle == "" {
		return false
	}
	// A needle that does not itself begin with a word character has no word
	// start to anchor to: «-v» must be findable after a space or a bracket.
	first, _ := utf8.DecodeRuneInString(needle)
	anchored := isWordRune(first)

	for from := 0; from <= len(hay)-len(needle); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		if !anchored || startsWord(hay, i) {
			return true
		}
		_, size := utf8.DecodeRuneInString(hay[i:])
		from = i + size
	}
	return false
}

// containsAny — the condition itself: any one of the alternatives is enough.
func containsAny(text string, alternatives []string) bool {
	for _, w := range alternatives {
		if ContainsWord(text, w) {
			return true
		}
	}
	return false
}

// startsWord reports whether position i in text begins a word: the rune before
// it is not part of one.
func startsWord(text string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return !isWordRune(r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// CondContains returns the words a `contains` condition looks for, and whether
// the condition is of that form at all.
//
// Exported for the same reason as CondVar — whoever reads a description asks
// the engine what a condition means instead of re-deriving the syntax. A second
// parser of it drifts on the first change, and here it would drift into
// reporting on conditions that do not exist.
func CondContains(cond string) ([]string, bool) {
	_, op, want, err := parseCond(cond)
	if err != nil || (op != "contains" && op != "not contains") {
		return nil, false
	}
	return condAlternatives(want), true
}

// CondVar returns the variable a branch condition tests, and whether the
// condition parses at all.
//
// Exported so that whoever reads a description — a linter, an editor, a
// visualiser — asks the engine which name a condition depends on instead of
// re-deriving the grammar from the docs. A second parser of the same syntax
// drifts on the first change: the left operand is a NAME while the right one is
// a literal, and a reader that misses the difference reports the value as a
// missing variable.
func CondVar(cond string) (string, bool) {
	name, _, _, err := parseCond(cond)
	if err != nil {
		return "", false
	}
	return name, true
}

func (s *state) eval(cond string) (bool, error) {
	name, op, want, err := parseCond(cond)
	if err != nil {
		return false, err
	}
	// payload, not lookup: a condition is DATA read by the flow, not text shown
	// to the model, so it needs the whole value with the host's note stripped.
	// A large result lives in a variable as a preview plus a handle, and a
	// condition reading the preview would answer about the first few hundred
	// bytes while looking exactly as if it had answered about the value.
	got := s.payload(name)
	switch op {
	case "is empty":
		return isBlank(got), nil
	case "is not empty":
		return !isBlank(got), nil
	case "contains":
		return containsAny(got, condAlternatives(want)), nil
	case "not contains":
		return !containsAny(got, condAlternatives(want)), nil
	}
	got = strings.TrimSpace(got)
	if op == "==" {
		return got == want, nil
	}
	return got != want, nil
}
