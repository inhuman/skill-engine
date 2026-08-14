package skillengine

// References to a value: `var`, `var.field`, `var.a.b.c`, `var.items[0].name`.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RefPattern — the shape of a reference: a name, then any number of `.field`
// steps and `[0]` indexes.
//
// Exported for the same reason as CondVars: whoever reads a description — a
// linter, an editor, a visualiser — must find a reference the way the engine
// finds one. A reference a reader cannot parse is a reference it silently does
// not check, and the half of the format it stops seeing is always the newest.
//
// ONE pattern, built into every regex that reads a reference — substitution,
// the branch conditions, the linter. They used to spell it out apiece, and the
// spellings had already drifted (`{{a.b}}` was one level deep while a condition
// accepted any number of dots and a trailing one). A reference is one thing.
//
// Depth used to stop at one field, and the comment beside it said the observed
// cases were flat. That was true and stopped being true: a tool's answer is as
// deep as the tool made it. Measured on sixteen live generations of a step
// description, six were refused by the format and FIVE of the six were one
// case — a number lying three levels inside a `kubectl get` answer, written the
// way such paths are written everywhere:
//
//	pod_details.status.containerStatuses[0].restartCount > 0
//
// Not a model failing to learn the format either: the same measurement with the
// list of allowed fields in the prompt (4.3 KB of it) held the same share of
// refusals. The format was short of a form, not the author short of the docs.
//
// An INDEX is written `[0]` rather than `.0`, and both halves of that matter.
// `[0]` is what authors write, and refusing the notation everyone uses would
// keep producing the refusals this exists to remove. It also keeps a path
// unambiguous: `.0` would be the field named "0" — a legal JSON key — and an
// index at the same time, so a reader could not tell which was meant.
//
// Deliberately NOT here: `[*]`, filters, arithmetic, functions. That is where a
// format turns into a template language and drags in precedence, escaping and
// runtime errors. A path either resolves to one value or it does not.
const RefPattern = `[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*|\[[0-9]+\])*`

var refRe = regexp.MustCompile(`^` + RefPattern + `$`)

// refStepRe splits a reference into its steps: `.field` or `[0]`.
var refStepRe = regexp.MustCompile(`\.([a-zA-Z_][a-zA-Z0-9_]*)|\[([0-9]+)\]`)

// pathStep — one step of a walk: a field of an object, or an index of a list.
type pathStep struct {
	field string
	index int
	byIdx bool
}

func (p pathStep) String() string {
	if p.byIdx {
		return "[" + strconv.Itoa(p.index) + "]"
	}
	return "." + p.field
}

// splitRef splits a reference into the variable it starts from and the steps
// that walk into its value.
func splitRef(ref string) (base string, steps []pathStep, ok bool) {
	if !refRe.MatchString(ref) {
		return "", nil, false
	}
	base = ref
	if i := strings.IndexAny(ref, ".["); i >= 0 {
		base = ref[:i]
	}
	for _, m := range refStepRe.FindAllStringSubmatch(ref[len(base):], -1) {
		if m[1] != "" {
			steps = append(steps, pathStep{field: m[1]})
			continue
		}
		// The pattern has already restricted this to digits; a number too long
		// for an int is out of range for any list anyway.
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return "", nil, false
		}
		steps = append(steps, pathStep{index: n, byIdx: true})
	}
	return base, steps, true
}

// resolve returns the value a reference points at.
//
// WHY A MISS IS AN ERROR, and why only for part of the grammar. An unknown name
// expands to an empty string in silence — a deliberate decision, because a
// marker reaching the model reads to it as part of the instruction. For a path
// that silence is worse than useless: `a.b.c` with `b` missing is
// indistinguishable from "the value is empty", and branches are taken on it.
//
// So a path that does not resolve is an error — the same call the numeric
// operands make ("both ways of not being a number are LOUD"). But ONLY for what
// the old grammar could not express: `var` and `var.field` keep their silence,
// because skills written under that promise are in other people's storage and
// must not start failing on an engine upgrade. Anything deeper, and any index,
// is new syntax that owes nothing to the old contract.
//
// The silent half is not left unwatched: the linter's W14 finds an unknown name
// statically, which is the only place it can be found at all.
func (s *state) resolve(ref string) (string, error) {
	// An exact name wins over a walk: the engine makes variables whose names
	// contain a dot itself (`pods.mem`, `findings.skipped`), and they are names,
	// not paths into a value.
	if v, ok := s.vars[ref]; ok {
		return v, nil
	}
	base, steps, ok := splitRef(ref)
	if !ok || len(steps) == 0 {
		return "", nil
	}
	// `var.field` is the shape that existed before paths did, so a miss inside
	// it stays as quiet as it has always been.
	quiet := len(steps) == 1 && !steps[0].byIdx

	miss := func(format string, args ...any) (string, error) {
		if quiet {
			return "", nil
		}
		return "", fmt.Errorf("reference `%s`: %s", ref, fmt.Sprintf(format, args...))
	}

	raw, ok := s.vars[base]
	if !ok {
		return miss("there is no variable `%s`", base)
	}
	cur, ok := s.valueOf(raw)
	if !ok {
		return miss("`%s` is not JSON, and a path can only be read into a structured value — it holds %q",
			base, clipValue(raw))
	}
	at := base
	for _, st := range steps {
		switch {
		case st.byIdx:
			list, isList := cur.([]any)
			if !isList {
				return miss("`%s` is %s, not a list", at, kindOf(cur))
			}
			if st.index >= len(list) {
				return miss("the list `%s` has %d element(s), and there is no [%d]", at, len(list), st.index)
			}
			cur = list[st.index]
		default:
			obj, isObj := cur.(map[string]any)
			if !isObj {
				return miss("`%s` is %s, not an object", at, kindOf(cur))
			}
			v, has := obj[st.field]
			if !has {
				return miss("`%s` has no field `%s` (it has: %s)", at, st.field, fieldNames(obj))
			}
			cur = v
		}
		at += st.String()
	}
	return itemText(cur), nil
}

// valueOf parses a variable's value as JSON — an object or a list.
//
// Two things stand between the value and the parser, and both are the host's
// doing: a variable holds what the host WOULD show the model, so a
// working-memory handle ("[mem:id]") is appended to any result and a large one
// is truncated to a preview on top of that. Either of them alone makes the text
// not JSON, so a path into a `call:` result has to deal with both.
//
// Order: strip the host's note and try; if that failed — the value is a
// truncated preview and no longer parses at all — take the whole thing from
// working memory by the handle, which is what the handle is appended for.
func (s *state) valueOf(raw string) (any, bool) {
	var v any
	if err := json.Unmarshal([]byte(trimHostNote(raw, s.vocab.TruncationNotes)), &v); err == nil {
		return v, true
	}
	id := memHandle(raw)
	if id == "" || s.memory == nil {
		return nil, false
	}
	full, ok := s.memory.Get(id)
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal([]byte(full), &v); err != nil {
		return nil, false
	}
	return v, true
}

// kindOf names what a value turned out to be, so that a refusal says why the
// step could not be taken rather than only that it could not.
func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return "not a structure"
}

// fieldNames lists what the object DOES have — the half of a refusal that turns
// it into a fix. Sorted, because a map's order would make the same failure read
// differently on every run, and clipped, because a tool's answer can carry
// dozens of keys and the sentence saying what is wrong must survive them.
func fieldNames(obj map[string]any) string {
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	slices.Sort(names)
	const max = 12
	if len(names) > max {
		return strings.Join(names[:max], ", ") + fmt.Sprintf(", … (%d more)", len(names)-max)
	}
	return strings.Join(names, ", ")
}
