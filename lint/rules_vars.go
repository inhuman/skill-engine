package lint

import (
	"regexp"
	"strings"

	skillengine "github.com/inhuman/skill-engine"
)

// W14 — a reference to a variable that does not exist at that point in the flow.
//
// The engine resolves an unknown name to an EMPTY string and says nothing:
// `{{history_brief}}` with a typo leaves the step without its context, and the
// failure reads as "the model did not understand the question". The mistake is
// neither in the model nor in the prompt but in one letter, and the only way to
// find it is by comparing names — which is exactly what a linter does.
//
// ORDER is checked, not merely existence: a variable declared by a step further
// down is empty in a step above. Same class, only nastier — the name exists, a
// search through the file finds it, and suspicion falls on anything except the
// order of the steps.

// varRefRE — a `{{name}}` reference. Dots are allowed inside (a field of an
// object) along with the engine's suffixes; the engine trims the spaces itself.
var varRefRE = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

func (r *run) workflowVarRefs(flow *skillengine.Flow) {
	known := set(r.opts.HostVars)
	for v := range flow.Vars {
		known[v] = true
	}
	// The answer variable always exists: a step without save_as writes into it,
	// and referring to it is legitimate.
	known[skillengine.AnswerVar] = true

	var walk func(steps []skillengine.Step, scope map[string]bool)
	walk = func(steps []skillengine.Step, scope map[string]bool) {
		for i := range steps {
			s := &steps[i]

			r.switchVarIsAName(s, i)
			r.containsAlternatives(s, i)

			// READING first, writing after: a step does not see its own result,
			// so `{{x}}` in the very step that declares `save_as: x` is a
			// reference to emptiness, not to itself.
			for _, ref := range refsOfStep(s) {
				if base, ok := knownVarBase(ref.name, scope); !ok {
					r.add("W14", SeverityError, "step `%s` refers to %s, and there is no variable `%s` "+
						"at that point — the engine will silently substitute an EMPTY string. Available: %s",
						stepLabel(s, i), ref.shown, base, strings.Join(sortedKeys(scope), ", "))
				}
			}

			// The iteration variable is declared BEFORE the loop's body: the
			// body is a branch, and without this order the rule complains about
			// `{{item}}` inside the very loop that defines it.
			if s.ForEach != nil && s.ForEach.As != "" {
				scope[s.ForEach.As] = true
			}

			// A branch runs in the SAME flow state (the engine hands it its own
			// state), so what is declared inside is visible afterwards too.
			// Assuming otherwise means inventing an isolation that does not
			// exist — measured on a live catalogue, where that guess produced
			// 42 findings out of thin air.
			for _, br := range s.Branches() {
				walk(br, scope)
			}
			for _, produced := range producedBy(s) {
				scope[produced] = true
			}
		}
	}
	walk(flow.Steps, known)
}

// varRef — one reference a step makes: the variable it resolves to, and how it
// is written in the skill. The two differ because a name is spelled one way
// inside a template and another inside a condition, and a finding that quotes
// the wrong one sends the reader looking for text that is not in the file.
type varRef struct {
	name  string
	shown string
}

// refsOfStep collects every reference a step makes.
//
// TWO forms, and both matter. A template (`{{name}}`) in an instruction, an
// argument, a value. And a BARE name — which is what `for_each.in`, `switch.var`
// and the conditions take: there the field IS the name, no braces involved.
// Reading only the templates leaves the bare half of the format unchecked, and
// that half is where branching lives.
func refsOfStep(s *skillengine.Step) []varRef {
	var texts []string
	var bare []string

	if s.Run != nil {
		texts = append(texts, s.Run.Instruction, s.Run.OnEmptyValue)
	}
	if s.Set != nil {
		texts = append(texts, s.Set.Value)
	}
	if s.Call != nil {
		texts = append(texts, s.Call.Tool, s.Call.OnEmptyValue)
		texts = append(texts, jsonStrings(s.Call.Args)...)
	}
	if s.Delegate != nil {
		texts = append(texts, s.Delegate.Task)
	}
	if s.OnServer != "" {
		texts = append(texts, s.OnServer)
	}
	if s.ForEach != nil {
		bare = append(bare, s.ForEach.In)
	}
	// The engine is asked which variable a condition depends on: its grammar
	// has a NAME on the left and a literal on the right, and a second parser of
	// it here would report the value as a missing variable.
	for _, cond := range []string{s.When, condOf(s.If)} {
		if cond == "" {
			continue
		}
		if name, ok := skillengine.CondVar(cond); ok {
			bare = append(bare, name)
		}
	}
	if s.Switch != nil && !strings.Contains(s.Switch.Var, "{{") {
		bare = append(bare, s.Switch.Var)
	}

	var out []varRef
	seen := map[string]bool{}
	add := func(name, shown string) {
		if name == "" || seen[shown] {
			return
		}
		seen[shown] = true
		out = append(out, varRef{name: name, shown: shown})
	}
	for _, t := range texts {
		for _, m := range varRefRE.FindAllStringSubmatch(t, -1) {
			add(m[1], "`{{"+m[1]+"}}`")
		}
	}
	for _, name := range bare {
		add(strings.TrimSpace(name), "`"+strings.TrimSpace(name)+"`")
	}
	return out
}

func condOf(i *skillengine.If) string {
	if i == nil {
		return ""
	}
	return i.Cond
}

// W18 — an alternative of a `contains` condition that can never matter.
//
// A `contains` matches a word that STARTS with the alternative, which is what
// lets a dictionary hold roots instead of every inflected form. The same
// property makes a longer alternative dead weight beside a shorter one:
// anything `жира` finds, `жир` has already found. A duplicate is the same
// mistake with the two spellings equal.
//
// Worth reporting rather than ignoring, because a dead alternative is almost
// never a harmless extra — it is the author believing they have covered a case
// that the shorter root was already swallowing, and it hides the real question:
// is that root short enough to collide with words nobody meant?
//
// The two cases the format could get wrong here are already closed elsewhere:
// a condition with NO alternatives is refused by the engine's own validation
// (there is nothing to look for, so it can never fire), and a condition naming
// a variable that does not exist at that point is W14.
func (r *run) containsAlternatives(s *skillengine.Step, idx int) {
	for _, cond := range []string{s.When, condOf(s.If)} {
		if cond == "" {
			continue
		}
		alts, ok := skillengine.CondContains(cond)
		if !ok {
			continue
		}
		for i, long := range alts {
			for j, short := range alts {
				if i == j || !covers(short, long) {
					continue
				}
				// Equal spellings: report the later one, once.
				if len(short) == len(long) && j > i {
					continue
				}
				if strings.EqualFold(short, long) {
					r.add("W18", SeverityWarn, "step `%s`: in `contains`, the alternative `%s` is listed twice — "+
						"the second one can never matter", stepLabel(s, idx), long)
					break
				}
				r.add("W18", SeverityWarn, "step `%s`: in `contains`, the alternative `%s` can never matter — "+
					"`%s` already finds every word it does (a match starts at a word's start and is free at "+
					"its end). Drop it, or shorten `%s` to what was actually meant",
					stepLabel(s, idx), long, short, short)
				break
			}
		}
	}
}

// covers reports whether finding short also finds long: long starts with short.
func covers(short, long string) bool {
	return len(short) <= len(long) && strings.EqualFold(long[:len(short)], short)
}

// W17 — `switch.var` is given a template instead of a name.
//
// The field takes a variable's NAME: the engine looks up exactly what is
// written there. A `{{x}}` in it makes it look for a variable literally called
// "{{x}}", find nothing, and compare an empty string against every case — so
// EVERY branch falls through to default. Nothing fails; the skill simply always
// takes the same path, and the branch that was the point of the switch never
// runs.
//
// The same trap on `for_each.in` is refused by the engine's own Validate. This
// one is not, because refusing it now would stop skills that already load —
// which is exactly the line between Validate and this package.
func (r *run) switchVarIsAName(s *skillengine.Step, idx int) {
	if s.Switch == nil || !strings.Contains(s.Switch.Var, "{{") {
		return
	}
	r.add("W17", SeverityError, "step `%s`: `switch.var: %s` — this is a variable NAME, not a template. "+
		"The engine will look for a variable spelled with the braces, find nothing, and every case will "+
		"fall through to default. Write it without `{{ }}`", stepLabel(s, idx), s.Switch.Var)
}

// producedBy — the names a step puts into the flow.
func producedBy(s *skillengine.Step) []string {
	var out []string
	switch {
	case s.Run != nil && s.Run.SaveAs != "":
		out = append(out, s.Run.SaveAs)
	case s.Set != nil && s.Set.Var != "":
		out = append(out, s.Set.Var)
	case s.Call != nil && s.Call.SaveAs != "":
		out = append(out, s.Call.SaveAs)
	case s.Delegate != nil && s.Delegate.SaveAs != "":
		out = append(out, s.Delegate.SaveAs)
	}
	if s.ForEach != nil {
		// The iteration variable lives INSIDE the loop, but the body's steps
		// are walked by the same function, so it is declared here — before the
		// branches are walked.
		if s.ForEach.As != "" {
			out = append(out, s.ForEach.As)
		}
		if s.ForEach.Collect != "" {
			out = append(out, s.ForEach.Collect)
		}
	}
	if s.Parallel != nil && s.Parallel.Collect != "" {
		out = append(out, s.Parallel.Collect)
	}
	return out
}

// knownVarBase checks a reference and returns the base name when it is unknown.
//
// `x.field` is legitimate when `x` exists: the value is parsed as an object and
// the field is taken from it. The engine's suffixes (the memory handle, the
// skipped marker) are references to the base name too.
func knownVarBase(ref string, scope map[string]bool) (string, bool) {
	if scope[ref] {
		return "", true
	}
	base := ref
	if i := strings.Index(ref, "."); i > 0 {
		base = ref[:i]
	}
	if scope[base] {
		return "", true
	}
	return base, false
}

// jsonStrings pulls every string value out of a call's arguments: a reference
// may sit in any of them, at any depth.
func jsonStrings(node any) []string {
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(node)
	return out
}
