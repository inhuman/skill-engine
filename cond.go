package skillengine

// Branch conditions: `var == value`, `var != value`, `var is [not] empty`,
// `var contains a | b | c`, `var > 5` (also `>=`, `<`, `<=`).

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
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

// numCondRe — the numeric comparisons: `var > 5`, `var >= req.limit`,
// `var < 0.5`, `var <= days`.
//
// Why the format grew them. Numbers were in the flow all along — counters, ids,
// thresholds parsed out of a request — and the only thing expressible about one
// was equality with a literal, which is why a live catalogue spells "no
// pipeline found" as `ci.id == 0`. Everything else went one of two ways: into
// the TEXT of a step, where a deterministic rule ends up applied by a model
// («порог простоя больше нуля — оставь только те, что не двигались дольше
// него»), or into an asset — a network call to compare two numbers.
//
// The refusal was not hypothetical. On the first day skills were being written
// by a model rather than by hand, it wrote `{{pod.restartCount}} > 5`, got a
// parse error, and wrote the same form again in another step of the same
// session — a form it returns to, not a slip. Both attempts were a condition in
// the body of a `for_each`: authors express "keep the ones over the threshold"
// as a loop with a branch inside, which is why a loop plus a comparison covers
// the case and a separate collection filter is not needed.
//
// The right side may be a literal OR another variable: a threshold is rarely a
// constant, it arrives from the step that parsed the request.
//
// Deliberately NOT here: arithmetic (`a + b > c`, `len(x) > 0`). Those are
// expressions, and expressions are the door to skills that cannot be read from
// top to bottom.
var numCondRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*(>=|<=|>|<)\s*(\S.*?)\s*$`)

// numberRe — what counts as a number on either side of a comparison.
//
// Narrower than strconv.ParseFloat on purpose: that also accepts `NaN`, `Inf`,
// hex floats and digit separators. A variable holding `NaN` would compare false
// against every threshold and look exactly like a number that is merely small —
// the silence these conditions are written to avoid.
var numberRe = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)

// nameRe — a variable name: the shape the left side of every condition takes,
// and the shape the right side of a comparison takes when it is not a literal.
var nameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// bracedLeftRe / bracedNameRe — a condition written the way substitution is
// written everywhere else: `{{pod.restartCount}} > 5`.
//
// The braces are refused rather than accepted, the same call `for_each.in`
// already makes: a field that holds a NAME holds exactly one spelling of it.
// Accepting them on the left would also teach a rule about the right side that
// is not true — `x == {{y}}` compares against those six characters, not against
// the value of y.
//
// What changed is the MESSAGE. The author of the two live refusals got a list
// of the allowed shapes and had to notice that their string differs from one of
// them by exactly two pairs of braces; now the error names the braces and
// prints the condition without them.
var (
	bracedLeftRe = regexp.MustCompile(`^\s*\{\{\s*[a-zA-Z_][a-zA-Z0-9_.]*\s*\}\}`)
	bracedNameRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)
)

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
	if bracedLeftRe.MatchString(cond) {
		bare := strings.TrimSpace(bracedNameRe.ReplaceAllString(cond, "$1"))
		// The suggestion is checked before it is offered: `{{a}} > пять` is two
		// mistakes, and a message that fixes the visible one sends the author
		// round again.
		if _, _, _, inner := parseCond(bare); inner != nil {
			return "", "", "", fmt.Errorf("condition %q: a condition takes a variable NAME, without {{ }} — "+
				"and without them it still does not parse: %w", cond, inner)
		}
		return "", "", "", fmt.Errorf("condition %q: a condition takes a variable NAME, without {{ }} — write `%s`",
			cond, bare)
	}
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
	if m := numCondRe.FindStringSubmatch(cond); m != nil {
		// Static, because it can never work: `count > пять` is wrong in the
		// file, not at the moment the branch is reached. Validate calls this
		// parser, so the skill is refused at load instead of mid-turn.
		if _, ok := parseNumber(m[3]); !ok && !nameRe.MatchString(m[3]) {
			return "", "", "", fmt.Errorf("condition %q: the right side of `%s` must be a number or the name of "+
				"a variable holding one, and %q is neither", cond, m[2], m[3])
		}
		return m[1], m[2], m[3], nil
	}
	m := condRe.FindStringSubmatch(cond)
	if m == nil {
		return "", "", "", fmt.Errorf("condition %q: want `var == value`, `var != value`, "+
			"`var is [not] empty`, `var contains a | b` or `var > 5` (also `>=`, `<`, `<=`)", cond)
	}
	return m[1], m[2], strings.Trim(m[3], `"'`), nil
}

// number — one side of a comparison, kept as an integer where it is one.
//
// Why not float64 for everything. The numbers in a flow include ids, and a
// nineteen-digit id does not survive float64: two different ones compare equal,
// and the branch is confidently wrong with nothing to see. Two integers are
// compared as integers; anything with a fractional part falls back to float,
// where the loss is inherent to the value rather than introduced by the engine.
type number struct {
	i     int64
	f     float64
	isInt bool
}

func parseNumber(text string) (number, bool) {
	t := strings.TrimSpace(text)
	if !numberRe.MatchString(t) {
		return number{}, false
	}
	if i, err := strconv.ParseInt(t, 10, 64); err == nil {
		return number{i: i, f: float64(i), isInt: true}, true
	}
	// Out of range (1e400) is refused rather than clamped to infinity: an
	// infinity compares against every threshold and explains nothing.
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return number{}, false
	}
	return number{f: f}, true
}

func compareNumbers(a, b number) int {
	if a.isInt && b.isInt {
		return cmp.Compare(a.i, b.i)
	}
	return cmp.Compare(a.f, b.f)
}

func isNumericOp(op string) bool {
	switch op {
	case ">", ">=", "<", "<=":
		return true
	}
	return false
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
// «заказ» finds «заказы», «заказа», «заказу» — and a dictionary of roots is why this
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

// CondVars returns every variable a branch condition depends on, and whether
// the condition parses at all.
//
// Exported so that whoever reads a description — a linter, an editor, a
// visualiser — asks the engine which names a condition depends on instead of
// re-deriving the grammar from the docs. A second parser of the same syntax
// drifts on the first change: the left operand is a NAME while the right one is
// a literal, and a reader that misses the difference reports the value as a
// missing variable.
//
// A LIST rather than the one name it used to be, because a numeric comparison
// may name a variable on both sides (`stale_days > req.threshold`). Returning
// only the left one would leave a typo in a threshold unchecked — which is the
// defect class the caller is usually looking for.
func CondVars(cond string) ([]string, bool) {
	name, op, want, err := parseCond(cond)
	if err != nil {
		return nil, false
	}
	if isNumericOp(op) {
		if _, isLiteral := parseNumber(want); !isLiteral {
			return []string{name, want}, true
		}
	}
	return []string{name}, true
}

func (s *state) eval(cond string) (bool, error) {
	name, op, want, err := parseCond(cond)
	if err != nil {
		return false, err
	}
	// Numbers are compared ONLY by these operators; `==` stays textual. Teaching
	// equality to coerce would change skills already written: `"5" == "5.0"` is
	// false today, and the equalities in a live catalogue compare ids and
	// sentinels, where "the same number written differently" is not a case that
	// arises and "the same string" is exactly what was meant.
	if isNumericOp(op) {
		left, err := s.operand(cond, name)
		if err != nil {
			return false, err
		}
		right, err := s.operand(cond, want)
		if err != nil {
			return false, err
		}
		switch c := compareNumbers(left, right); op {
		case ">":
			return c > 0, nil
		case ">=":
			return c >= 0, nil
		case "<":
			return c < 0, nil
		default:
			return c <= 0, nil
		}
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

// operand resolves one side of a numeric comparison: a literal as written, or
// the number a variable holds.
//
// Both ways of not being a number are LOUD — an error that stops the turn, not
// a `false` that picks the other branch. A condition is the one place where a
// wrong answer is invisible: `restarts > 5` looks right whatever it returns,
// and the engine already has a class of defects where an unknown name resolves
// quietly to emptiness (the linter's W14 exists for it).
//
// Emptiness in particular is NOT zero. A step that returned nothing leaves
// `restarts` empty, and reading that as 0 makes "no data" indistinguishable
// from "few restarts" — the same reason `is empty` is a form of its own. An
// author for whom emptiness is legal says so, with `var is not empty` in front.
func (s *state) operand(cond, ref string) (number, error) {
	if n, ok := parseNumber(ref); ok {
		return n, nil
	}
	got := strings.TrimSpace(s.payload(ref))
	if isBlank(got) {
		return number{}, fmt.Errorf("condition %q: `%s` is empty (or a marked failure) — there is nothing to "+
			"compare. An empty variable is NOT zero: answering the comparison would make "+
			"\"the step returned nothing\" read as \"the number is small\". If emptiness is legal here, "+
			"guard the step with `%s is not empty`", cond, ref, ref)
	}
	n, ok := parseNumber(got)
	if !ok {
		return number{}, fmt.Errorf("condition %q: `%s` = %q is not a number", cond, ref, clipValue(got))
	}
	return n, nil
}

// clipValue — a value quoted in an error message. A variable can hold a whole
// tool result, and an error that prints it in full buries the sentence that
// says what is wrong.
func clipValue(v string) string {
	const max = 60
	v = strings.Join(strings.Fields(v), " ")
	if r := []rune(v); len(r) > max {
		return string(r[:max]) + "…"
	}
	return v
}
