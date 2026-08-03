// Package lint checks a skill against the rules of the format — the ones the
// engine cannot enforce at execution time without having already burned the
// turn.
//
// Why it lives with the format. Every rule here was paid for by a broken turn
// in production, and each is about a defect that stays QUIET: a loop that
// collects into a variable nobody writes runs all its iterations and gathers
// nothing, a typo in a variable name resolves to an empty string, a required
// field the instruction allows to be empty sends the model into whitespace up
// to the token ceiling. None of it raises an error; all of it produces an
// answer that looks fine. Left in the embedding application, these rules are
// rewritten from scratch by the next embedder, along with the failures that
// taught them.
//
// The split with Flow.Validate is deliberate: Validate refuses what CANNOT
// run, the linter reports what runs badly. Moving advice into Validate would
// start breaking skills that are already written and already working.
//
// Degradation is built in: every fact about the installation is optional, a
// missing one skips its rules and records the skip in Report.Skipped AND as an
// info finding — a partial check must never look like a full one. A linter
// that falls over because a dependency is unavailable is a linter people stop
// running.
package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	skillengine "github.com/inhuman/skill-engine"
)

// Severity ranks a finding. What BLOCKS is decided by the caller: the library
// says "this is a defect of the format", the embedder decides whether that
// stops a save or fails a build. Without that split every embedder would need
// its own severity scale to express its own policy.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
	SeverityInfo  Severity = "info"
)

// Finding — one rule's result on one skill.
type Finding struct {
	Rule     string
	Severity Severity
	Skill    string
	Path     string
	Line     int // 1-based; 0 = the finding is about the whole file
	Message  string
}

// Report — the findings of one run plus a summary of what was skipped.
type Report struct {
	Findings []Finding
	// Skipped — rules that did not run, with the reason. Read it before
	// concluding a skill is clean.
	Skipped []string
}

// HasErrors reports whether any finding is an error.
func (r Report) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Counts returns the number of findings by severity.
func (r Report) Counts() (errs, warns, infos int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityError:
			errs++
		case SeverityWarn:
			warns++
		default:
			infos++
		}
	}
	return
}

// Observe calls record for every finding — a bridge to the embedder's metrics
// without this package importing a telemetry library.
func (r Report) Observe(record func(rule, severity string)) {
	for _, f := range r.Findings {
		record(f.Rule, string(f.Severity))
	}
}

// Text renders the report for a human, grouped by file.
func (r Report) Text() string {
	if len(r.Findings) == 0 {
		return "No findings — the skills are clean." + r.skippedSuffix()
	}
	var b strings.Builder
	lastKey := "\x00"
	for _, f := range r.Findings {
		key := f.Path
		if key == "" {
			key = f.Skill
		}
		if key != lastKey {
			if lastKey != "\x00" {
				b.WriteString("\n")
			}
			header := f.Skill
			if f.Path != "" && f.Path != f.Skill {
				header = fmt.Sprintf("%s (%s)", f.Skill, f.Path)
			}
			if header == "" {
				header = "(general)"
			}
			b.WriteString(header + ":\n")
			lastKey = key
		}
		loc := ""
		if f.Line > 0 {
			loc = fmt.Sprintf(":%d", f.Line)
		}
		fmt.Fprintf(&b, "  [%s] %s%s: %s\n", f.Severity, f.Rule, loc, f.Message)
	}
	errs, warns, infos := r.Counts()
	fmt.Fprintf(&b, "\nTotal: %d errors, %d warnings, %d infos.", errs, warns, infos)
	return b.String() + r.skippedSuffix()
}

func (r Report) skippedSuffix() string {
	if len(r.Skipped) == 0 {
		return ""
	}
	return "\nSkipped: " + strings.Join(r.Skipped, "; ") + "."
}

// Source — one skill file to check.
type Source struct {
	Path string
	Raw  []byte
}

// Facts — what the embedder knows about ITS installation, and the engine
// cannot know: which servers are up, which tools they carry, which built-in
// tools exist. The rule knows WHAT to compare, the host says WHAT WITH.
//
// Every field is optional. nil (or an empty answer) skips the rules that need
// it and records the reason — never an error, and never silence.
type Facts struct {
	// ServerNames — the registered MCP servers. W2, E1.
	ServerNames func() []string
	// AllTools — server name → the tools it carries. W3, W12, E2.
	AllTools func() map[string][]string
	// ToolSchemas — "server:tool" → the tool's input JSON Schema. W5 checks a
	// call step's arguments against `required` in it.
	ToolSchemas func() map[string][]byte
	// BuiltinTools — the built-in tools the application actually has. W15, E5.
	BuiltinTools func() []string
	// WriteServers — the servers that CHANGE something. E3 uses it to spot a
	// skill that calls itself read-only and reaches for one anyway.
	WriteServers func() []string
	// SkillNames — the catalogue a `delegate` step can name. E4.
	SkillNames func() []string
}

// Envelope describes a server whose call result WRAPS the payload instead of
// being it — an exit code, stdout and stderr around what the script printed.
//
// The engine cannot know which server does that, and the difference is
// invisible: substituting the whole envelope where the payload is expected
// breaks nothing loudly. A loop honestly makes one iteration over the wrapper,
// a renderer honestly reports zero findings — because there was nothing to
// find.
type Envelope struct {
	// Server — whose results are wrapped.
	Server string
	// Fields — the wrapper's fields, named in the finding so the author
	// recognises what they are looking at.
	Fields []string
	// Payload — the field holding what the call actually produced.
	Payload string
}

// StaleAPI — a construct the embedder has REMOVED, with what replaced it.
//
// The vocabulary belongs to the host: the engine knows nothing of the tools an
// application used to have. Empty list → rule S3 does not run.
type StaleAPI struct {
	// Pattern — a regular expression matched against the playbook.
	Pattern string
	// What / Instead — how the finding reads: "<what> is no longer supported —
	// use <instead>".
	What    string
	Instead string
}

// AssetVocabulary — the names the embedder uses for asset kinds and params.
//
// The format deliberately does not close these lists (kinds are an
// application's vocabulary — that is why `params` is an open map), so the
// rules about them cannot be grounded without the host saying what it calls
// things. Left empty, the rules that need it skip with a reason. The vocabulary
// used by the shipped examples is returned by SchemaVocabulary.
type AssetVocabulary struct {
	// CodeKinds — kinds whose content is a program: it belongs PAST the model,
	// passed by reference into a call's arguments.
	CodeKinds []string
	// ReferenceKinds — kinds whose content is knowledge for the model: it
	// belongs IN the instruction's text, or the model never sees it.
	ReferenceKinds []string
	// KnownParams — the param keys the host's resolver actually reads.
	KnownParams []string
	// LangParam — the key naming a code asset's language, if the host has one.
	LangParam string
}

// SchemaVocabulary returns the asset vocabulary the shipped examples use. A
// starting point, not a default: an embedder with its own kinds passes its own.
func SchemaVocabulary() AssetVocabulary {
	return AssetVocabulary{
		CodeKinds:      []string{"code", "config"},
		ReferenceKinds: []string{"text", "data"},
		KnownParams:    []string{"lang", "format"},
		LangParam:      "lang",
	}
}

// Options — the knobs and the host's vocabulary. The zero value is valid: the
// rules that need a name they were not given skip with a reason.
type Options struct {
	// Unmarshal — how to parse YAML. Required, for the same reason the engine
	// takes it as a parameter: a library that picks a YAML implementation picks
	// it for everyone who embeds it.
	Unmarshal skillengine.Unmarshal

	// PlaybookBudget — the size of a playbook, in bytes, above which S5 warns.
	// 0 = DefaultPlaybookBudget; negative = the rule is off.
	PlaybookBudget int

	// CallProtocol — the name of the tool an MCP tool is called THROUGH, if the
	// application has one (a step is handed that one tool, not a function per
	// tool name). Empty → W12 and E2 do not run.
	CallProtocol string

	// Envelopes — servers whose result wraps the payload. Empty → W8 does not run.
	Envelopes []Envelope

	// Assets — what the host calls its asset kinds and params. See AssetVocabulary.
	Assets AssetVocabulary

	// StaleAPIs — constructs the host has removed. Empty → S3 does not run.
	StaleAPIs []StaleAPI

	// ReadOnlyRoles — the role names that promise to change nothing. Empty →
	// E3 does not run.
	ReadOnlyRoles []string

	// ImplicitBuiltins — built-in tools handed out WITHOUT being declared. A
	// skill is not required to declare them, and E5 does not ask it to.
	ImplicitBuiltins []string

	// EmptyWords — how the authors of THIS installation write "empty" in an
	// instruction or a field's description: "empty", "пустая строка", "vide".
	// W16 needs them to spot a required field the text beside it allows to be
	// empty. Matched case-insensitively at the start of a word, so a stem is
	// enough ("пуст" covers «пустая», «пусто»).
	//
	// The library ships none: agents sharing this format do not share a
	// language, and a list baked in here would silently do nothing for whoever
	// writes in another one. Empty → W16 does not run, and says so.
	EmptyWords []string

	// FreeTextFields — name fragments that mark a schema field as FREE TEXT
	// rather than a slot: "message", "summary", "описание", "raison". W13 uses
	// them to ask for a length ceiling only where an answer can run away —
	// demanding one from `id` or `env` would be noise.
	//
	// The structural half of W13 needs no words and always runs: a string field
	// inside an ARRAY is a runaway risk whatever it is called. Empty → only
	// that half runs, and the rule says so.
	FreeTextFields []string

	// HostVars — variables the embedding application puts into the flow before
	// the first step (the request, the history). A skill does not declare them
	// and is free to read them; W14 would otherwise report every one of them as
	// a typo. Leaving this empty is safe but noisy — the rule cannot tell an
	// injected variable from a misspelled one.
	HostVars []string
}

// DefaultPlaybookBudget — the playbook size above which S5 warns. Not a limit
// but a reminder: a playbook is the weight of the context of every single run.
const DefaultPlaybookBudget = 12288

// Lint checks one skill. An error means the check itself could not run (a
// missing parser, a broken option) — "the skill is bad" is findings in the
// report, never an error.
func Lint(raw []byte, facts Facts, opts Options) (Report, error) {
	return LintAll([]Source{{Raw: raw}}, facts, opts)
}

// LintAll checks a set of sources and aggregates one report.
func LintAll(sources []Source, facts Facts, opts Options) (Report, error) {
	if opts.Unmarshal == nil {
		return Report{}, fmt.Errorf("skill-lint: Options.Unmarshal is required")
	}
	stale, err := compileStale(opts.StaleAPIs)
	if err != nil {
		return Report{}, err
	}
	r := &run{facts: facts, opts: opts, stale: stale, skips: map[string]string{}}
	for _, src := range sources {
		r.lintOne(src)
	}
	return r.report(), nil
}

// run accumulates findings and run-level skips (one X1 per rule, not per file).
type run struct {
	facts    Facts
	opts     Options
	stale    []staleAPI
	findings []Finding
	skips    map[string]string // rule → reason

	// the skill being checked
	skill string
	path  string
}

type staleAPI struct {
	re      *regexp.Regexp
	what    string
	instead string
}

func compileStale(in []StaleAPI) ([]staleAPI, error) {
	out := make([]staleAPI, 0, len(in))
	for _, s := range in {
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return nil, fmt.Errorf("skill-lint: stale API %q: %w", s.Pattern, err)
		}
		out = append(out, staleAPI{re: re, what: s.What, instead: s.Instead})
	}
	return out, nil
}

// add records a finding on the skill currently being checked.
func (r *run) add(rule string, sev Severity, format string, args ...any) {
	r.findings = append(r.findings, Finding{
		Rule: rule, Severity: sev, Skill: r.skill, Path: r.path,
		Message: fmt.Sprintf(format, args...),
	})
}

// addAt is add with a line number.
func (r *run) addAt(rule string, sev Severity, line int, format string, args ...any) {
	r.findings = append(r.findings, Finding{
		Rule: rule, Severity: sev, Skill: r.skill, Path: r.path, Line: line,
		Message: fmt.Sprintf(format, args...),
	})
}

// skip records that a rule could not run — once per run, not per file.
func (r *run) skip(rule, reason string) {
	if _, ok := r.skips[rule]; !ok {
		r.skips[rule] = reason
	}
}

func (r *run) report() Report {
	rep := Report{Findings: r.findings}
	rules := make([]string, 0, len(r.skips))
	for rule := range r.skips {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	for _, rule := range rules {
		rep.Skipped = append(rep.Skipped, rule+" ("+r.skips[rule]+")")
		rep.Findings = append(rep.Findings, Finding{
			Rule: SkipRule, Severity: SeverityInfo,
			Message: fmt.Sprintf("rule %s did not run: %s", rule, r.skips[rule]),
		})
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Line < b.Line
	})
	return rep
}

// SkipRule — the rule id carrying "a rule did not run". It is a FINDING and not
// only an entry in Report.Skipped, because a caller that reads findings and
// nothing else would otherwise see a partial check as a clean one.
const SkipRule = "X1"

func (r *run) lintOne(src Source) {
	r.path = src.Path
	r.skill = strings.TrimSuffix(baseName(src.Path), ".yaml")

	skill, err := skillengine.ParseSkill(src.Raw, r.opts.Unmarshal)
	if err != nil {
		r.add("S1", SeverityError, "the file does not parse: %v", err)
		return
	}
	if skill.Name != "" {
		r.skill = skill.Name
	}
	// A parked skill is deliberately incomplete — an A/B variant switched off,
	// half-migrated, a draft. Reporting on it teaches people to ignore reports.
	if skill.Disabled {
		return
	}

	raw := string(src.Raw)
	r.headerRules(&skill, raw)
	r.playbookRules(&skill, raw)
	r.workflowRules(&skill)
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// lineOf returns the 1-based line of needle's first occurrence (0 = not found).
func lineOf(content, needle string) int {
	idx := strings.Index(content, needle)
	if idx < 0 {
		return 0
	}
	return strings.Count(content[:idx], "\n") + 1
}
