package skillengine

// Branch conditions: `var == value`, `var != value`, `var is [not] empty`.

import (
	"fmt"
	"regexp"
	"strings"
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

// isBlank — "the step produced nothing useful": empty or a failure marker.
func isBlank(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || strings.HasPrefix(t, "ERROR:") || strings.HasPrefix(t, "DENIED:")
}

// parseCond parses a condition of the form `var == value` / `var != value`.
// The value may be empty (`var == `) — that tests for an unfilled variable.
func parseCond(cond string) (name, op, want string, err error) {
	if m := emptyCondRe.FindStringSubmatch(cond); m != nil {
		op = "is empty"
		if strings.TrimSpace(m[2]) != "" {
			op = "is not empty"
		}
		return m[1], op, "", nil
	}
	m := condRe.FindStringSubmatch(cond)
	if m == nil {
		return "", "", "", fmt.Errorf("condition %q: want `var == value`, `var != value` or `var is [not] empty`", cond)
	}
	return m[1], m[2], strings.Trim(m[3], `"'`), nil
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
	switch op {
	case "is empty":
		return isBlank(s.lookup(name)), nil
	case "is not empty":
		return !isBlank(s.lookup(name)), nil
	}
	got := strings.TrimSpace(s.lookup(name))
	if op == "==" {
		return got == want, nil
	}
	return got != want, nil
}
