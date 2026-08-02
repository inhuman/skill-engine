package lint

import (
	"regexp"
	"sort"
	"strings"

	skillengine "github.com/inhuman/skill-engine"
)

// headerRules — what the skill says about itself: S1, S6, E1, E3.
func (r *run) headerRules(s *skillengine.Skill, raw string) {
	r.formatVersion(s, raw)

	if err := skillengine.ValidateSkillName(s.Name); err != nil {
		r.add("S1", SeverityError, "%v", err)
	}
	if strings.TrimSpace(s.Description) == "" {
		r.add("S1", SeverityError, "no description — it is what routing decides by, "+
			"and a skill without one is only reachable by being named outright")
	}
	// The mode table is format semantics, and an explicit mode over an empty
	// half is the error it exists to catch (see ResolveMode).
	if _, err := s.ResolveMode(); err != nil {
		r.add("S1", SeverityError, "%v", err)
	}

	// S6 — without triggers a skill is only reachable by being named outright.
	// Info, not a warning: a skill launched on purpose is a legitimate design.
	if len(s.TriggerExamples) == 0 {
		r.add("S6", SeverityInfo, "no trigger_examples — nothing measures the skill's closeness "+
			"to a live phrasing, so it will only run when named outright")
	}

	r.declaredServers(s)
	r.readOnlyRole(s)
}

// S1 — the format version. Split out because the actionable part differs: a
// foreign major is not "fix the field" but "run the migration that ships with
// the engine", and a skill silently read as legacy is the same defect with the
// field missing altogether.
func (r *run) formatVersion(s *skillengine.Skill, raw string) {
	if err := skillengine.CheckEngineVersion(s.EngineVersion); err == nil {
		return
	} else if !r.migratable(raw) {
		r.add("S1", SeverityError, "%v", err)
		return
	}
	declared := s.EngineVersion
	if declared == "" {
		declared = "nothing, which reads as " + skillengine.LegacyEngineVersion
	}
	r.add("S1", SeverityError, "the skill declares %s while the engine speaks %s — "+
		"Migrate() rewrites the file into the current format, keeping comments and key order",
		declared, skillengine.EngineVersion)
}

// migratable asks the engine whether it can carry this file across on its own.
// Asking rather than guessing: the engine owns the list of what changed between
// majors, and a second copy of that list here would drift on the next one.
func (r *run) migratable(raw string) bool {
	_, changed, err := skillengine.Migrate([]byte(raw))
	return err == nil && changed
}

// E1 — every server the skill declares is registered.
//
// The skill's `servers` is the ceiling of its radius. A name that no longer
// exists (a server renamed, an installation without it) does not fail loudly:
// the step is handed a set that does not contain what it needs and reports that
// the model could not manage.
func (r *run) declaredServers(s *skillengine.Skill) {
	if len(s.Servers) == 0 {
		return
	}
	known, ok := r.servers("E1")
	if !ok {
		return
	}
	for _, srv := range s.Servers {
		if !known[srv] {
			r.add("E1", SeverityError, "server `%s` is not registered. Available: %s",
				srv, strings.Join(sortedSet(known), ", "))
		}
	}
}

// E3 — a skill that calls itself read-only and reaches for a server that writes.
//
// One of the two is wrong, and which one matters: either the role is a lie —
// and whatever the host grants by role is granted too widely — or the server is
// left over from an earlier version of the skill and widens its radius for
// nothing.
func (r *run) readOnlyRole(s *skillengine.Skill) {
	if len(r.opts.ReadOnlyRoles) == 0 || s.Role == "" || len(s.Servers) == 0 {
		return
	}
	if !contains(r.opts.ReadOnlyRoles, s.Role) {
		return
	}
	writes, ok := r.writeServers("E3")
	if !ok {
		return
	}
	for _, srv := range s.Servers {
		if writes[srv] {
			r.add("E3", SeverityWarn, "role %q promises to change nothing, and `%s` writes — "+
				"either the role is wrong or the server is left over", s.Role, srv)
		}
	}
}

// playbookRules — the prose half of a skill: S3, S5, E2, E5.
//
// The playbook is a full way to describe a skill, not a draft, so it gets
// checked like one. What can be checked in prose is narrow — it is text, and
// the rules here look only for things the author SPELLED OUT: a call written in
// the application's own syntax, a construct that no longer exists.
func (r *run) playbookRules(s *skillengine.Skill, raw string) {
	if !s.HasPlaybook() {
		return
	}
	body := s.Playbook

	// S3 — constructs the embedder has removed.
	if len(r.stale) == 0 {
		if strings.TrimSpace(body) != "" {
			r.skip("S3", "no removed constructs are configured")
		}
	} else {
		for _, st := range r.stale {
			if m := st.re.FindString(body); m != "" {
				r.addAt("S3", SeverityError, lineOf(raw, m),
					"%s is no longer supported — use %s", st.what, st.instead)
			}
		}
	}

	// S5 — the playbook's weight. It is not a limit but the cost of every
	// single run: the text goes into the context each time the skill starts.
	budget := r.opts.PlaybookBudget
	if budget == 0 {
		budget = DefaultPlaybookBudget
	}
	if budget > 0 && len(body) > budget {
		r.add("S5", SeverityWarn, "the playbook is %d bytes against a budget of %d — "+
			"that is context weight on every run; consider shortening it or moving parts into assets",
			len(body), budget)
	}

	r.playbookToolRefs(s, body, raw)
	r.playbookBuiltins(s, body, raw)
}

// E2 — a tool the playbook names on a server that does not carry it.
//
// Only a call the author wrote out in the application's calling syntax counts:
// mentioning a tool in passing is prose, and the model is free to ignore it.
func (r *run) playbookToolRefs(s *skillengine.Skill, body, raw string) {
	protocol, ok := r.callProtocol("E2")
	if !ok {
		return
	}
	refs := callRefRE(protocol).FindAllStringSubmatch(body, -1)
	if len(refs) == 0 {
		return
	}
	tools, ok := r.tools("E2")
	if !ok {
		return
	}
	for _, ref := range refs {
		srv, tool := ref[1], ref[2]
		list, hasServer := tools[srv]
		if !hasServer {
			continue // the server's fate is decided by E1
		}
		if !contains(list, tool) {
			r.addAt("E2", SeverityError, lineOf(raw, ref[0]),
				"tool `%s` is not on server `%s` (a typo?). It has: %s",
				tool, srv, strings.Join(list, ", "))
		}
	}
}

// callRefRE builds the pattern for a written-out call: protocol(server="…", tool="…").
func callRefRE(protocol string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(protocol) +
		`\(\s*server\s*=\s*"([^"]+)"\s*,\s*tool\s*=\s*"([^"]+)"`)
}

// E5 — the playbook tells the executor to call a built-in tool the skill never
// declared.
//
// A built-in tool is handed out on declaration. Without it the call fires into
// the void: the runtime answers "no such tool", and the model repeats the same
// call until the budget runs out — which reads in a transcript as the model
// being stubborn rather than as one missing line in the header.
func (r *run) playbookBuiltins(s *skillengine.Skill, body, raw string) {
	matches := builtinCallRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return
	}
	known, _, ok := r.builtins("E5")
	if !ok {
		return
	}
	declared := set(s.BuiltinTools)
	for _, n := range r.opts.ImplicitBuiltins {
		declared[n] = true
	}
	seen := map[string]bool{}
	for _, m := range matches {
		tool := m[1]
		// !known: a word that merely looks like a call and names nothing the
		// application has is prose, not a mistake.
		if seen[tool] || declared[tool] || !known[tool] {
			continue
		}
		seen[tool] = true
		r.addAt("E5", SeverityError, lineOf(raw, m[0]),
			"the playbook says to call `%s`, and the skill does not declare it in builtin_tools — "+
				"the executor will not be handed it and the call will miss. Add: builtin_tools: [%s]",
			tool, tool)
	}
}

// builtinCallRE — a call written out as name(...). A tool mentioned in prose
// ("the chart is drawn by render_diagram") is not an instruction to call it.
var builtinCallRE = regexp.MustCompile(`\b([a-z][a-z0-9_]{2,})\s*\(`)

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func sortedSet(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// squeeze — a quote on one line: findings are read in a console and in a
// review comment.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }
