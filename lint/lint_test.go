package lint_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/inhuman/skill-engine/lint"
)

// testFacts — a small installation to check against: two servers with tools, a
// couple of built-ins, one skill in the catalogue, one server that writes.
func testFacts() lint.Facts {
	return lint.Facts{
		ServerNames: func() []string { return []string{"wiki", "tracker", "runner", "store"} },
		AllTools: func() map[string][]string {
			return map[string][]string{
				"wiki":    {"page_search", "page_get"},
				"tracker": {"issue_search"},
				"runner":  {"exec"},
				"store":   {"put"},
			}
		},
		ToolSchemas: func() map[string][]byte {
			return map[string][]byte{
				"wiki:page_get": []byte(`{"type":"object","required":["title"]}`),
			}
		},
		BuiltinTools: func() []string { return []string{"run_script", "render_diagram", "memory"} },
		WriteServers: func() []string { return []string{"store"} },
		SkillNames:   func() []string { return []string{"probe", "lookup"} },
	}
}

// testOptions — the host vocabulary the rules need. Without it half of them
// would skip, and the tests would be checking the skips rather than the rules.
func testOptions() lint.Options {
	return lint.Options{
		Unmarshal:        yaml.Unmarshal,
		CallProtocol:     "call_tool",
		Assets:           lint.SchemaVocabulary(),
		ReadOnlyRoles:    []string{"reader"},
		ImplicitBuiltins: []string{"memory"},
		HostVars:         []string{"input"},
		Envelopes: []lint.Envelope{{
			Server:  "runner",
			Fields:  []string{"exit_code", "stdout", "stderr"},
			Payload: "stdout",
		}},
		StaleAPIs: []lint.StaleAPI{{
			Pattern: `\brecall\b`,
			What:    "the `recall` tool",
			Instead: "`memory(op=…)`",
		}},
	}
}

// head — a legal skill header the rule tests build on.
const head = `skill_engine_version: "2.1.0"
skill_version: "1.0.0"
name: probe
description: a probe skill for the rules
trigger_examples: ["a probe phrase"]
servers: ["wiki"]
role: reader
`

func lintSkill(t *testing.T, src string) lint.Report {
	t.Helper()
	rep, err := lint.Lint([]byte(src), testFacts(), testOptions())
	require.NoError(t, err, "the check itself fell over")
	return rep
}

// find returns the first finding of a rule, or nil.
func find(rep lint.Report, rule string) *lint.Finding {
	for i := range rep.Findings {
		if rep.Findings[i].Rule == rule {
			return &rep.Findings[i]
		}
	}
	return nil
}

func requireFinding(t *testing.T, rep lint.Report, rule string, sev lint.Severity) lint.Finding {
	t.Helper()
	f := find(rep, rule)
	require.NotNilf(t, f, "%s did not fire. Report:\n%s", rule, rep.Text())
	assert.Equal(t, sev, f.Severity)
	return *f
}

func findAll(rep lint.Report, rule string) []lint.Finding {
	var out []lint.Finding
	for _, f := range rep.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func requireQuiet(t *testing.T, rep lint.Report, rule string) {
	t.Helper()
	if f := find(rep, rule); f != nil {
		t.Fatalf("%s produced a false finding: %s", rule, f.Message)
	}
}

func TestLintNeedsAParser(t *testing.T) {
	_, err := lint.Lint([]byte(head), testFacts(), lint.Options{})
	require.Error(t, err, "a linter with no way to parse must say so, not report a clean skill")
}

// A file that is not YAML is a finding, not an error: the caller asked about
// the skill, and "it does not parse" is the answer.
func TestBrokenFileIsAFinding(t *testing.T) {
	rep := lintSkill(t, "name: [unclosed\n")
	requireFinding(t, rep, "S1", lint.SeverityError)
	assert.True(t, rep.HasErrors())
}

// A parked skill is deliberately incomplete — an A/B variant switched off, a
// half-finished migration. Reporting on it teaches people to ignore reports.
func TestDisabledSkillIsSkipped(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: parked\ndisabled: true\nplaybook: half a thought with recall in it\n")
	assert.Empty(t, rep.Findings, "a disabled skill produced findings:\n%s", rep.Text())
}

// The degradation contract: a missing fact must be VISIBLE. A rule that
// silently reported nothing whenever its dependency was down would make a
// partial check indistinguishable from a clean one.
func TestMissingFactsAreReportedNotSwallowed(t *testing.T) {
	rep, err := lint.Lint([]byte(head+`workflow:
  tools: ["wiki"]
  steps:
    - name: fetch
      call: {tool: "wiki:page_get", args: {title: x}, save_as: page}
`), lint.Facts{}, lint.Options{Unmarshal: yaml.Unmarshal})
	require.NoError(t, err, "an unavailable dependency must not fail the run")

	assert.NotEmpty(t, rep.Skipped, "nothing was recorded as skipped")
	skipped := strings.Join(rep.Skipped, " ")
	assert.Contains(t, skipped, "W3")
	assert.Contains(t, skipped, "W5")

	f := find(rep, lint.SkipRule)
	require.NotNil(t, f, "a caller reading only findings would see this as a clean skill")
	assert.Equal(t, lint.SeverityInfo, f.Severity)
	assert.False(t, rep.HasErrors())
}

// A rule with nothing to do must not file a skip: a report full of "did not
// run" about rules that had no work is a report nobody reads to the end, and
// the skips that DO matter drown in it.
func TestNothingToCheckFilesNoSkip(t *testing.T) {
	rep, err := lint.Lint([]byte(`skill_engine_version: "2.1.0"
name: prose
description: a skill with no program and no servers
trigger_examples: ["a phrase"]
playbook: just answer the question
`), lint.Facts{}, lint.Options{Unmarshal: yaml.Unmarshal})
	require.NoError(t, err)

	for _, rule := range []string{"W3", "W4", "W5", "W11", "W12", "W15", "E1", "E3", "E4", "E5"} {
		for _, s := range rep.Skipped {
			assert.NotContains(t, s, rule+" (", "%s filed a skip although it had nothing to check", rule)
		}
	}
}

func TestReportText(t *testing.T) {
	rep := lint.Report{
		Findings: []lint.Finding{
			{Rule: "W6", Severity: lint.SeverityError, Skill: "probe", Path: "probe.yaml", Line: 5, Message: "empty collect"},
			{Rule: "S6", Severity: lint.SeverityInfo, Skill: "probe", Path: "probe.yaml", Message: "no triggers"},
		},
		Skipped: []string{"W3 (the tool listing is unavailable)"},
	}
	txt := rep.Text()
	assert.Contains(t, txt, "probe (probe.yaml)")
	assert.Contains(t, txt, "[error] W6:5: empty collect")
	assert.Contains(t, txt, "Total: 1 errors, 0 warnings, 1 infos.")
	assert.Contains(t, txt, "Skipped: W3 (the tool listing is unavailable).")
}

func TestReportTextWithoutFindings(t *testing.T) {
	assert.Equal(t, "No findings — the skills are clean.", lint.Report{}.Text())
}

func TestObserveReachesEveryFinding(t *testing.T) {
	rep := lint.Report{Findings: []lint.Finding{
		{Rule: "W6", Severity: lint.SeverityError},
		{Rule: "S6", Severity: lint.SeverityInfo},
	}}
	var seen []string
	rep.Observe(func(rule, severity string) { seen = append(seen, rule+"/"+severity) })
	assert.Equal(t, []string{"W6/error", "S6/info"}, seen)
}

// LintAll aggregates, and the path of every finding says which file it is about
// — a report over a catalogue that does not is unusable.
func TestLintAllKeepsFilesApart(t *testing.T) {
	rep, err := lint.LintAll([]lint.Source{
		{Path: "a/first.yaml", Raw: []byte(head + "playbook: do the thing\n")},
		{Path: "a/second.yaml", Raw: []byte("skill_engine_version: \"2.1.0\"\nname: second\nplaybook: x\n")},
	}, testFacts(), testOptions())
	require.NoError(t, err)

	f := find(rep, "S1")
	require.NotNil(t, f, "the second skill has no description — that is an S1")
	assert.Equal(t, "a/second.yaml", f.Path)
	assert.Equal(t, "second", f.Skill)
}

// The name of a file is a fallback, not decoration: a skill whose header did
// not parse still has to be identifiable in a report over a whole catalogue.
func TestSkillNameFallsBackToTheFile(t *testing.T) {
	rep, err := lint.LintAll([]lint.Source{{Path: "dir/broken.yaml", Raw: []byte("name: [unclosed\n")}},
		testFacts(), testOptions())
	require.NoError(t, err)
	f := requireFinding(t, rep, "S1", lint.SeverityError)
	assert.Equal(t, "broken", f.Skill)
}

// A stale format is not "your file is wrong" but "run the migration that ships
// with the engine" — the actionable half is what makes the finding worth having.
func TestStaleFormatPointsAtTheMigration(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "1.0.0"
name: old
description: written for the previous major
workflow:
  assets:
    script: {kind: code, source: inline, lang: python, content: "print(1)"}
  steps:
    - name: s
      instruction: do it
      tools: []
`)
	f := requireFinding(t, rep, "S1", lint.SeverityError)
	assert.Contains(t, f.Message, "Migrate()")
}

// A skill of a major the engine cannot carry across must NOT advertise a
// migration that would refuse it.
func TestFutureFormatDoesNotPromiseAMigration(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"9.0.0\"\nname: future\ndescription: from the future\nplaybook: x\n")
	f := requireFinding(t, rep, "S1", lint.SeverityError)
	assert.NotContains(t, f.Message, "Migrate()")
}
