package skillengine

// {{var}} substitution and normalisation of step values.

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// varRe catches {{var}} and {{var.field}} — one level of nesting.
//
// Deeper is deliberately absent: the observed cases are flat, and nesting
// drags in indexes, filters and the rest of a template engine the format
// avoids.
// assetRe catches {{asset:name}} — substituting a payload's content into the
// TEXT of an instruction.
//
// A separate namespace rather than sharing one with variables: otherwise a
// name collision silently beats one of the two, and the author cannot see what
// was actually substituted.
//
// This is how kind: text and data are used — a lookup table or a reply
// template is useless unless the model reads it. Code and config go the other
// way, into a tool argument past the model.
var assetRe = regexp.MustCompile(`\{\{\s*asset:([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}`)

var varRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)\s*\}\}`)

// expand substitutes {{var}}. An unknown variable becomes an empty string
// rather than staying as text: a marker that reaches the model reads to it as
// part of the instruction and produces questions about "the variable var".
func (s *state) expand(text string) string {
	text = assetRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.asset(assetRe.FindStringSubmatch(m)[1])
	})
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.lookup(varRe.FindStringSubmatch(m)[1])
	})
}

// expandForArgs — substitution into call ARGUMENTS, not into an instruction.
//
// The difference is not cosmetic. A variable holds what the host would show
// the MODEL: a large value is truncated to a preview, and a working-memory
// handle ("[mem:id]") is appended to any of them. That helps the model — it
// sees the data exists and how to read the rest. To the script on the other
// end of the call it is garbage: it receives JSON with a tail and fails to
// parse.
//
// Live case: a render step received the result of the merge_findings step in
// stdin together with the "[mem:…]" tail and answered "RENDER_ERROR: stdin
// does not parse as a JSON stream"; the turn died at the publication gate, two
// steps away from the cause. The third manifestation of one class — after
// field access and for_each.
//
// So values go into arguments WHOLE and WITHOUT the host's note.
func (s *state) expandForArgs(text string) string {
	text = assetRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.asset(assetRe.FindStringSubmatch(m)[1])
	})
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		return trimHostNote(s.fullValue(s.lookup(varRe.FindStringSubmatch(m)[1])))
	})
}

// asset returns a payload's content, fetching it on first use.
//
// An unavailable asset expands to an EMPTY string, same as an unknown
// variable: a marker that reached the model would read as part of the
// instruction. The failure is not silent, though — the resolver reports it to
// the caller.
func (s *state) asset(name string) string {
	if v, ok := s.assetCache[name]; ok {
		return v
	}
	a, ok := s.assets[name]
	if !ok || s.assetsRes == nil {
		return ""
	}
	v, err := s.assetsRes.Resolve(s.assetCtx, name, a)
	if err != nil {
		s.assetCache[name] = ""
		return ""
	}
	s.assetCache[name] = v
	return v
}

// lookup returns the value of a variable or of its field.
//
// A field is looked up in the JSON object a step stored in the variable (a
// structured answer). A missing one yields an empty string, same as a missing
// variable: a marker that reached the model would read as part of the
// instruction.
func (s *state) lookup(name string) string {
	if v, ok := s.vars[name]; ok {
		return v
	}
	base, field, hasField := strings.Cut(name, ".")
	if !hasField {
		return ""
	}
	raw, ok := s.vars[base]
	if !ok {
		return ""
	}
	obj, ok := s.objectOf(raw)
	if !ok {
		return ""
	}
	v, ok := obj[field]
	if !ok {
		return ""
	}
	if str, isStr := v.(string); isStr {
		return str
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// objectOf parses a variable's value as a JSON object.
//
// A variable's value is what the host WOULD show the model, not the raw tool
// output: a working-memory handle ("[mem:id]") is always appended, and a large
// one is truncated to a preview on top of that. Both break parsing, which is
// why the field silently went empty for ANY `call:` result — {{var.field}}
// substitution never worked on such variables.
//
// Order: strip the host's note and try; if that failed (truncated), take the
// whole thing from working memory by the handle — that is what it is appended
// for.
func (s *state) objectOf(raw string) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimHostNote(raw)), &obj); err == nil {
		return obj, true
	}
	id := memHandle(raw)
	if id == "" || s.memory == nil {
		return nil, false
	}
	full, ok := s.memory.Get(id)
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal([]byte(full), &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// fullValue returns a variable's value IN FULL.
//
// A variable holds what the host would show the model: a large result is
// truncated to a preview, and the whole of it sits in working memory under the
// appended handle. For a prompt that is right; for a COLLECTION it is fatal —
// a loop over a truncated list silently processes part of it and returns an
// answer that looks complete (exactly what the loud ceiling in for_each
// exists for).
func (s *state) fullValue(raw string) string {
	id := memHandle(raw)
	if id == "" || s.memory == nil {
		return raw
	}
	full, ok := s.memory.Get(id)
	if !ok {
		return raw
	}
	return full
}

// truncationNotes — how the host marks a truncated result. The Russian
// "обрезано:" is kept alongside the English marker on purpose: this is a
// convention shared with the embedding application, and dropping the old
// spelling would silently stop trimming the note for hosts that still write
// it — which puts the tail back into call arguments (see expandForArgs).
var truncationNotes = []string{"mem:", "truncated:", "обрезано:"}

// trimHostNote strips the note the host appends to a tool result: "[mem:id]"
// for a whole one and "…[mem:id — this is a PREVIEW…]"/"…[truncated: …]" for a
// large one. The note is always the last line — that is what gets cut, leaving
// the content untouched.
func trimHostNote(s string) string {
	i := strings.LastIndex(s, "\n[")
	if i < 0 {
		return s
	}
	tail := s[i+2:]
	for _, note := range truncationNotes {
		if strings.HasPrefix(tail, note) {
			return strings.TrimSuffix(strings.TrimSpace(s[:i]), "…")
		}
	}
	return s
}

// expandArgs substitutes {{var}} into STRING argument values, walking nested
// structures. Numbers, flags and object shape stay as written: substitution is
// about values, not about the call's schema.
func (s *state) expandArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = s.expandAny(v)
	}
	return out
}

// callArgs prepares the arguments of a `call:` step: value substitution plus
// carrying over the delivery route declared by an asset.
//
// An asset declares `deliver`, while the bridge reads the route from
// `_deliver` in the arguments — there was nothing to connect them, so the
// declaration stayed decorative. Live failure: a skill declared
// `deliver: reply` on a render asset, the step's output was not marked as the
// turn's answer, and the publication gate rejected the whole turn ("the
// skill's final step did not record a result"). Exactly the case where a field
// is declared and validated but has no live effect.
//
// An explicit `_deliver` in the step's arguments wins: it is written for that
// specific call, whereas the asset's declaration is a default for all of its
// consumers.
func (s *state) callArgs(args map[string]any) map[string]any {
	out := s.expandArgs(args)
	if _, explicit := out["_deliver"]; explicit {
		return out
	}
	to := s.assetDeliver(args)
	if to == "" {
		return out
	}
	if out == nil {
		out = make(map[string]any, 1)
	}
	out["_deliver"] = map[string]any{"to": to}
	return out
}

// assetDeliver returns the delivery route declared by the first asset that
// goes into the arguments by reference.
//
// The value is passed AS IS: route names are the application's vocabulary and
// the engine has no business interpreting them. Empty or "none" means no
// delivery: that is the only value the engine understands itself, because it
// means declining a route rather than naming one.
func (s *state) assetDeliver(args map[string]any) string {
	for _, name := range AssetRefsInArgs(args) {
		a, ok := s.assets[name]
		if !ok {
			continue
		}
		if route := strings.TrimSpace(a.Deliver); route != "" && route != "none" {
			return route
		}
	}
	return ""
}

func (s *state) expandAny(v any) any {
	switch t := v.(type) {
	case string:
		return s.expandForArgs(t)
	case map[string]any:
		// {from: "asset:name"} — a reference to a payload. The content is
		// substituted HERE, host-side: a `call:` step is built by the skill and
		// not by the model, so the payload never reaches the model at all —
		// exactly what assets exist for.
		if from, ok := t["from"].(string); ok && len(t) == 1 {
			if name, isAsset := strings.CutPrefix(from, "asset:"); isAsset {
				return s.asset(name)
			}
		}
		return s.expandArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = s.expandAny(e)
		}
		return out
	default:
		return v
	}
}

// normalizeOneOf reduces a step's free-form answer to one of the allowed
// values.
//
// The LAST occurrence is sought: a model often lists the options before naming
// the chosen one ("decide between t1 and foreign… Result: foreign"), so the
// first match is the enumeration, not the decision. Same trick as lifting a
// verdict out of reasoning.
func normalizeOneOf(text string, allowed []string) string {
	if len(allowed) == 0 {
		return text
	}
	trimmed := strings.Trim(strings.TrimSpace(text), "\"'`")

	// 1. The answer is exactly one of the values. That is how a model with a
	//    grammar answers, and usually how an obedient model without one does.
	for _, want := range allowed {
		if strings.EqualFold(trimmed, want) {
			return want
		}
	}

	// 2. The value after a decision marker. A model told to answer in one word
	//    still writes "Summary: … Result: foreign" — take what stands AFTER
	//    the marker rather than the last thing in the text.
	if v, ok := afterDecisionMarker(trimmed, allowed); ok {
		return v
	}

	// 3. Exactly one value occurs (possibly several times) — take it.
	if v, ok := onlyMentioned(trimmed, allowed); ok {
		return v
	}

	// 4. One value is named STRICTLY more often than another: having named its
	//    choice, a model usually repeats it ("we must decide between t1 and
	//    foreign; this one is foreign"). An equal count is not a decision —
	//    that is exactly where the previous version broke.
	if v, ok := mostMentioned(trimmed, allowed); ok {
		return v
	}

	// 5. Otherwise — NOTHING. The previous version took the last occurrence and
	//    got "this is definitely t1, not foreign" wrong: a trailing negation is
	//    an ordinary construction (and the default word order in Russian), so
	//    the guess produced the exact opposite answer. An empty value sends the
	//    switch to its default, where the skill can honestly ask again, while a
	//    wrong guess silently steers the turn elsewhere.
	return ""
}

// decisionMarkers — words after which a model names its choice.
//
// Both languages are listed, and the list is not a place to economise: a
// missing marker costs a wrong branch, while an extra one costs nothing.
var decisionMarkers = []string{
	"result:", "summary:", "answer:", "conclusion:", "choice:", "decision:", "verdict:",
	"результат:", "итог:", "ответ:", "вывод:", "выбор:", "решение:",
}

func afterDecisionMarker(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	at := -1
	for _, m := range decisionMarkers {
		if i := strings.LastIndex(low, m); i > at {
			at = i + len(m)
		}
	}
	if at < 0 {
		return "", false
	}
	tail := low[at:]
	best, pos := "", -1
	for _, want := range allowed {
		if i := strings.Index(tail, strings.ToLower(want)); i >= 0 && (pos < 0 || i < pos) {
			best, pos = want, i
		}
	}
	return best, best != ""
}

// mostMentioned returns the value mentioned strictly more often than the rest.
func mostMentioned(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	best, bestN, tie := "", 0, false
	for _, want := range allowed {
		n := countWholeWord(low, strings.ToLower(want))
		switch {
		case n > bestN:
			best, bestN, tie = want, n, false
		case n == bestN && n > 0:
			tie = true
		}
	}
	if bestN == 0 || tie {
		return "", false
	}
	return best, true
}

func onlyMentioned(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	var found string
	for _, want := range allowed {
		if countWholeWord(low, strings.ToLower(want)) > 0 {
			if found != "" && !strings.EqualFold(found, want) {
				return "", false // several mentioned — cannot choose
			}
			found = want
		}
	}
	return found, found != ""
}

// countWholeWord counts occurrences of needle as a SEPARATE word.
//
// A plain Count lies when one allowed value is contained in another: with the
// set [found, not_found] the answer "not_found" was credited to both, both
// layers saw a tie and returned nothing — the step silently ended up without a
// value, the switch went to its default, and the turn answered with an
// internal variable. A boundary is anything but letters, digits and
// underscore: the underscore is part of names like not_found.
func countWholeWord(text, needle string) int {
	if needle == "" {
		return 0
	}
	n, from := 0, 0
	for {
		i := strings.Index(text[from:], needle)
		if i < 0 {
			return n
		}
		i += from
		if isWordBoundary(text, i-1) && isWordBoundary(text, i+len(needle)) {
			n++
		}
		from = i + len(needle)
	}
}

// isWordBoundary reports that the position is outside a word: past the end of
// the string, or not a letter/digit/underscore.
func isWordBoundary(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	r := rune(text[i])
	return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// MemSuffix — the suffix of the variable holding a working-memory handle: the
// result of a `save_as: pods` step puts the handle into `pods.mem`.
const MemSuffix = ".mem"

// memRefRe catches a working-memory handle in result text. The "[mem:id]" form
// is a convention shared by the host and the engine: that is how the handle is
// seen both by the model in a preview and by the skill in substitution. Parsed
// out of text rather than added to an interface, because the handle is meant
// to be read from text in the first place.
var memRefRe = regexp.MustCompile(`\[mem:([a-zA-Z0-9_-]+)`)

// memHandle returns the working-memory handle from a tool result, if present.
func memHandle(text string) string {
	if m := memRefRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// SkippedSuffix — the suffix of the variable holding the list of skipped
// branches: `collect: findings` puts them into `findings.skipped`.
const SkippedSuffix = ".skipped"
