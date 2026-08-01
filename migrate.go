package skillengine

// Migrating skill files between format majors.
//
// Why this lives in the library. The engine refuses a skill of a foreign major
// on the grounds that skills live in a user's storage and are NOT updated with
// a deploy — which is precisely the situation that needs migration code. What
// changed between majors is known here and nowhere else; leave it out and every
// embedder writes the same rewrite again, from a CHANGELOG, by hand. The
// guarantee "a field is never lost silently" must not be paid for by someone
// else's storage standing still.

import (
	"fmt"
	"strings"
	"unicode"
)

// Migrate rewrites a skill file into the format this engine speaks and reports
// whether anything changed.
//
// The file is edited as TEXT rather than re-serialised. A skill is a
// hand-written document: its comments carry the reason a field exists and the
// failure that paid for a limit (see examples/), and a round trip through a
// YAML marshaller drops every one of them, reorders the keys and reflows the
// block scalars. A migration that silently strips the comments would trade one
// silent loss for another.
//
// What it does for 1.x → 2.x:
//
//	an asset's `lang: python`  →  `params:` / `  lang: python`
//	skill_engine_version       →  the current major's baseline (added if absent)
//
// The input is a skill file as the FORMAT defines it: one YAML document. An
// embedder that wraps skills in something of its own — front matter, a markdown
// body, a bundle of several documents — unwraps them before calling and wraps
// the result back. Such an input is refused rather than guessed at: the wrapper
// belongs to the embedder, and teaching the library about it would be exactly
// the host-specific knowledge this package is built without.
//
// It does NOT validate the result — parse and validate it as usual afterwards.
// A skill already on the current major is returned untouched with false.
func Migrate(raw []byte) ([]byte, bool, error) {
	lines := strings.Split(string(raw), "\n")
	scan := scanYAML(lines)

	// Checked before anything else, including the "already current" shortcut:
	// the embedder must learn on the first call, not on the first file that
	// happens to need an edit.
	if err := checkSingleDocument(scan); err != nil {
		return nil, false, err
	}

	declared, at := declaredVersion(scan)
	if declared == "" {
		declared = LegacyEngineVersion
	}
	want, err := parseVersion(declared)
	if err != nil {
		return nil, false, fmt.Errorf("skill_engine_version %q: not semver", declared)
	}
	have, _ := parseVersion(EngineVersion)
	switch {
	case want.major == have.major:
		return raw, false, nil
	case want.major > have.major:
		// Migrating down is guesswork: what a future major means is unknown
		// here by definition.
		return nil, false, fmt.Errorf(
			"skill targets format %s, engine is %s: cannot migrate a skill from a newer major", declared, EngineVersion)
	}

	if want.major < 2 && have.major >= 2 {
		lines, scan, err = migrateAssetLang(lines, scan)
		if err != nil {
			return nil, false, err
		}
	}

	// The baseline of the current major, not EngineVersion itself: a migrated
	// skill uses nothing newer, and declaring 2.4.0 would demand an engine it
	// does not actually need.
	lines = setDeclaredVersion(lines, scan, at, fmt.Sprintf("%d.0.0", have.major))
	return []byte(strings.Join(lines, "\n")), true, nil
}

// migrateAssetLang expands an asset's `lang` into `params`, the one breaking
// change of format 2.0.0 that touches a skill's text.
func migrateAssetLang(lines []string, scan []yamlLine) ([]string, []yamlLine, error) {
	expand := map[int]bool{}
	for _, a := range scanAssets(scan) {
		langAt, hasLang := a.fields["lang"]
		if !hasLang {
			continue
		}
		if _, hasParams := a.fields["params"]; hasParams {
			// Merging is a judgement call — which value wins, and does the
			// author know both are there? Guessing here would silently drop
			// one of them, the exact failure this migration exists to prevent.
			return nil, nil, fmt.Errorf(
				"asset %q declares both lang and params: merge them by hand, the migration will not guess", a.name)
		}
		expand[langAt] = true
	}
	if len(expand) == 0 {
		return lines, scan, nil
	}
	out := make([]string, 0, len(lines)+len(expand))
	for i, ln := range lines {
		if !expand[i] {
			out = append(out, ln)
			continue
		}
		indent := strings.Repeat(" ", scan[i].indent)
		// Block style rather than `params: {lang: x}`: a flow mapping would
		// need the value escaped, and a trailing comment would land inside the
		// braces. Here the value — comment and all — moves across untouched.
		out = append(out, indent+"params:", indent+"  lang: "+scan[i].rest)
	}
	return out, scanYAML(out), nil
}

// checkSingleDocument refuses an input that is not one YAML document.
//
// The failure this prevents looked like success. Given a file wrapped in front
// matter (`---`, a header, `---`, a markdown body) the migration used to insert
// the version field ahead of the opening marker: the header stopped being a
// header, the skill lost its name, and the caller got changed=true and no
// error. A refusal costs one message; that result costs a skill that loads as
// nameless in production.
//
// Told apart textually, because a document separator is a line and not a value:
// a `---` inside `content: |` is part of a script and is skipped along with the
// rest of the block scalar. An explicit start of a single document is legal and
// stays legal — what is refused is a SECOND marker, or a first one arriving
// after content has already begun (an implicit first document).
func checkSingleDocument(scan []yamlLine) error {
	markers, content := 0, false
	for i, l := range scan {
		switch {
		case l.inBlock || l.blank || l.comment:
		case l.docMarker:
			markers++
			if markers > 1 || content {
				return fmt.Errorf(
					"line %d: input is not a single YAML document — Migrate takes a skill file as the format defines it; strip your own wrapper (front matter, a markdown body, a multi-document bundle) first",
					i+1)
			}
		default:
			content = true
		}
	}
	return nil
}

// assetBlock — one asset declaration and the lines of its direct fields.
type assetBlock struct {
	name   string
	fields map[string]int
}

// scanAssets finds asset declarations and their DIRECT fields.
//
// Direct is what matters: an asset may carry a nested `args:` mapping, and a
// `lang` key inside it belongs to a tool call, not to the asset. Depth is the
// only thing telling them apart.
func scanAssets(scan []yamlLine) []assetBlock {
	var out []assetBlock
	assetsIndent, nameIndent, fieldIndent := -1, -1, -1
	for i, l := range scan {
		if l.inBlock || l.blank || l.comment {
			continue
		}
		if assetsIndent >= 0 && l.indent <= assetsIndent {
			assetsIndent, nameIndent, fieldIndent = -1, -1, -1
		}
		if assetsIndent < 0 {
			if l.key == "assets" && l.rest == "" {
				assetsIndent = l.indent
			}
			continue
		}
		if nameIndent < 0 {
			nameIndent = l.indent
		}
		switch {
		case l.indent == nameIndent:
			out = append(out, assetBlock{name: l.key, fields: map[string]int{}})
			fieldIndent = -1
		case len(out) == 0:
			// A stray line before any asset name — nothing to attach it to.
		case fieldIndent < 0:
			fieldIndent = l.indent
			fallthrough
		case l.indent == fieldIndent:
			if l.key != "" {
				if _, seen := out[len(out)-1].fields[l.key]; !seen {
					out[len(out)-1].fields[l.key] = i
				}
			}
		}
	}
	return out
}

// declaredVersion returns the skill_engine_version value and the line it sits
// on (-1 when the field is absent).
func declaredVersion(scan []yamlLine) (string, int) {
	for i, l := range scan {
		if l.inBlock || l.indent != 0 || l.key != "skill_engine_version" {
			continue
		}
		value, _ := splitComment(l.rest)
		return strings.Trim(strings.TrimSpace(value), `"'`), i
	}
	return "", -1
}

// setDeclaredVersion writes the version in place, or inserts the field when the
// skill never declared one — a legacy skill must come out of a migration
// saying which format it is now in, otherwise the next engine reads it as 1.0.0
// again.
func setDeclaredVersion(lines []string, scan []yamlLine, at int, version string) []string {
	field := `skill_engine_version: "` + version + `"`
	if at >= 0 {
		if _, comment := splitComment(scan[at].rest); comment != "" {
			field += "  " + comment
		}
		lines[at] = field
		return lines
	}
	insertAt := 0
	for i, l := range scan {
		switch {
		case l.docMarker:
			// A legal explicit document start. The field must land AFTER it —
			// ahead of it, it would become a document of its own.
			insertAt = i + 1
			continue
		case l.blank, l.comment, l.inBlock:
			continue
		}
		insertAt = i // the first real line: the header comments stay on top
		break
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, field)
	return append(out, lines[insertAt:]...)
}

// yamlLine — as much of a line's shape as a textual migration needs.
type yamlLine struct {
	indent    int
	key       string // a plain mapping key, "" when the line is not one
	rest      string // whatever follows "key:"
	blank     bool
	comment   bool
	inBlock   bool // content of a block scalar, to be left alone
	docMarker bool // `---` or `...` at column 0: a document boundary
}

// scanYAML walks the lines, tracking block scalars.
//
// Tracking them is the whole reason this is not a regexp: `content: |` holds a
// script, and a python line reading `lang: sys.argv` is not a YAML key. Editing
// it would corrupt the asset the migration was meant to preserve.
func scanYAML(lines []string) []yamlLine {
	out := make([]yamlLine, len(lines))
	blockIndent := -1
	for i, raw := range lines {
		trimmed := strings.TrimLeft(raw, " ")
		l := yamlLine{indent: len(raw) - len(trimmed)}
		l.blank = strings.TrimSpace(raw) == ""

		if blockIndent >= 0 {
			if l.blank || l.indent > blockIndent {
				l.inBlock = true
				out[i] = l
				continue
			}
			blockIndent = -1
		}
		switch {
		case l.blank:
		case strings.HasPrefix(trimmed, "#"):
			l.comment = true
		case l.indent == 0 && isDocMarker(trimmed):
			l.docMarker = true
		case strings.HasPrefix(trimmed, "- "):
			// A sequence item; none of the keys this migration touches is one.
		default:
			if k, rest, ok := strings.Cut(trimmed, ":"); ok && isPlainKey(k) {
				l.key, l.rest = k, strings.TrimSpace(rest)
				if isBlockScalar(l.rest) {
					blockIndent = l.indent
				}
			}
		}
		out[i] = l
	}
	return out
}

// isDocMarker recognises a document boundary: `---` starts one, `...` ends one,
// either alone on the line or followed by content on the same line.
func isDocMarker(trimmed string) bool {
	for _, m := range []string{"---", "..."} {
		if trimmed == m || strings.HasPrefix(trimmed, m+" ") {
			return true
		}
	}
	return false
}

func isPlainKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '_' && r != '-' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// isBlockScalar recognises `|`, `>` and their chomping/indent indicators.
func isBlockScalar(rest string) bool {
	if rest == "" || (rest[0] != '|' && rest[0] != '>') {
		return false
	}
	for _, r := range rest[1:] {
		if r != '+' && r != '-' && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// splitComment separates a trailing comment from a value. Only used on values
// this migration writes back (a version), where a `#` inside quotes does not
// occur.
func splitComment(rest string) (value, comment string) {
	i := strings.Index(rest, "#")
	if i < 0 {
		return rest, ""
	}
	return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i:])
}
