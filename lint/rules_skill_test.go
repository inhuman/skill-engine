package lint_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/inhuman/skill-engine/lint"
)

// S1 covers the header: a name the format does not allow, a missing
// description, a mode pointing at a half that is not there.
func TestS1_HeaderDefects(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"a name the format does not allow",
			"skill_engine_version: \"2.1.0\"\nname: Probe\ndescription: d\nplaybook: x\n", "lowercase"},
		{"no description",
			"skill_engine_version: \"2.1.0\"\nname: probe\nplaybook: x\n", "no description"},
		{"a mode over an empty half",
			"skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\nmode: workflow\nplaybook: x\n", "no steps"},
		{"describes no turn at all",
			"skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\n", "describes no turn"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := requireFinding(t, lintSkill(t, c.src), "S1", lint.SeverityError)
			assert.Contains(t, f.Message, c.want)
		})
	}
}

// S3: a construct the embedder has removed. The vocabulary belongs to the host
// — the engine knows nothing of the tools an application used to have.
func TestS3_StaleConstruct(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Look it up with recall and report.\n")
	f := requireFinding(t, rep, "S3", lint.SeverityError)
	assert.Contains(t, f.Message, "recall")
	assert.Contains(t, f.Message, "memory(op=…)", "the finding says what to use instead")
	assert.NotZero(t, f.Line, "a finding in prose without a line is a finding nobody can locate")
}

// Word boundaries matter: a longer name that merely contains the removed one is
// not the removed one.
func TestS3_NoFalsePositiveInsideALongerName(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Call graph_recall_facts and show the result.\n")
	requireQuiet(t, rep, "S3")
}

// Nothing configured — the rule cannot run, and says so rather than passing the
// skill as checked.
func TestS3_UnconfiguredIsASkipNotSilence(t *testing.T) {
	opts := testOptions()
	opts.StaleAPIs = nil
	rep, err := lint.Lint([]byte(head+"playbook: do the thing\n"), testFacts(), opts)
	require.NoError(t, err)

	require.NotEmpty(t, rep.Skipped)
	assert.Contains(t, rep.Skipped[0], "S3")
}

// A bad pattern is the caller's mistake, not the skill's: it fails the run
// rather than becoming a finding about somebody's skill.
func TestBrokenStalePatternIsAnError(t *testing.T) {
	opts := testOptions()
	opts.StaleAPIs = []lint.StaleAPI{{Pattern: "([", What: "x", Instead: "y"}}
	_, err := lint.Lint([]byte(head+"playbook: x\n"), testFacts(), opts)
	require.Error(t, err)
}

// S5: the playbook's weight is the cost of every single run — the text goes
// into the context each time the skill starts.
func TestS5_PlaybookBudget(t *testing.T) {
	long := "playbook: |\n"
	for i := 0; i < 40; i++ {
		long += "  a line of the playbook that is long enough to add up over forty of them\n"
	}
	opts := testOptions()
	opts.PlaybookBudget = 512
	rep, err := lint.Lint([]byte(head+long), testFacts(), opts)
	require.NoError(t, err)

	f := requireFinding(t, rep, "S5", lint.SeverityWarn)
	assert.Contains(t, f.Message, "512")
}

func TestS5_CanBeTurnedOff(t *testing.T) {
	opts := testOptions()
	opts.PlaybookBudget = -1
	rep, err := lint.Lint([]byte(head+"playbook: |\n  a short one\n"), testFacts(), opts)
	require.NoError(t, err)
	requireQuiet(t, rep, "S5")
}

// S6: without triggers nothing measures the skill's closeness to a live
// phrasing. Info, not a warning — a skill launched on purpose is legitimate.
func TestS6_NoTriggerExamples(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\nplaybook: do it\n")
	requireFinding(t, rep, "S6", lint.SeverityInfo)
}

func TestS6_TriggersPresentIsQuiet(t *testing.T) {
	requireQuiet(t, lintSkill(t, head+"playbook: do it\n"), "S6")
}

// E1: a server the skill declares does not exist in this installation. The
// failure is not loud — the step is handed a set without what it needs and
// reports that the model could not manage.
func TestE1_UnregisteredServer(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\nservers: [\"ghost\"]\nplaybook: do it\n")
	f := requireFinding(t, rep, "E1", lint.SeverityError)
	assert.Contains(t, f.Message, "ghost")
	assert.Contains(t, f.Message, "docs", "the finding lists what is actually registered")
}

// E2: the playbook writes out a call to a tool that is not on that server.
func TestE2_UnknownToolInThePlaybook(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Run call_tool(server=\"docs\", tool=\"page_serch\", args={}) and report.\n")
	f := requireFinding(t, rep, "E2", lint.SeverityError)
	assert.Contains(t, f.Message, "page_serch")
}

func TestE2_KnownToolIsQuiet(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Run call_tool(server=\"docs\", tool=\"page_search\", args={}) and report.\n")
	requireQuiet(t, rep, "E2")
}

// An unknown SERVER is E1's business — two findings about one name would send
// the author fixing it twice.
func TestE2_UnknownServerIsLeftToE1(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\nservers: [\"ghost\"]\n"+
		"playbook: |\n  Run call_tool(server=\"ghost\", tool=\"whatever\", args={}).\n")
	requireQuiet(t, rep, "E2")
	requireFinding(t, rep, "E1", lint.SeverityError)
}

// E3: the skill calls itself read-only and reaches for a server that writes.
// One of the two is wrong, and which one matters.
func TestE3_ReadOnlyRoleWithAWritingServer(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\nrole: reader\n"+
		"servers: [\"docs\", \"store\"]\nplaybook: do it\n")
	f := requireFinding(t, rep, "E3", lint.SeverityWarn)
	assert.Contains(t, f.Message, "store")
}

func TestE3_ReadOnlyRoleWithReadingServersIsQuiet(t *testing.T) {
	requireQuiet(t, lintSkill(t, head+"playbook: do it\n"), "E3")
}

// E5 catches the class that cost a live skill a month: the playbook tells the
// executor to call a built-in tool that was never declared, so the call fires
// into the void and the model repeats it until the budget runs out.
func TestE5_UndeclaredBuiltinInThePlaybook(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Count them: run_script(name=\"count_per_day\").\n")
	f := requireFinding(t, rep, "E5", lint.SeverityError)
	assert.Contains(t, f.Message, "run_script")
}

func TestE5_DeclaredBuiltinIsSilent(t *testing.T) {
	rep := lintSkill(t, "skill_engine_version: \"2.1.0\"\nname: probe\ndescription: d\n"+
		"builtin_tools: [\"run_script\"]\nplaybook: |\n  Count them: run_script(name=\"count_per_day\").\n")
	requireQuiet(t, rep, "E5")
}

// A tool handed out without being declared is not something to declare.
func TestE5_ImplicitBuiltinNeedsNoDeclaration(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Look in memory: memory(op=\"peek\", id=\"x\").\n")
	requireQuiet(t, rep, "E5")
}

// A word shaped like a call that names nothing the application has is prose.
func TestE5_ProseIsNotACall(t *testing.T) {
	rep := lintSkill(t, head+"playbook: |\n  Summarise (briefly) and answer.\n")
	requireQuiet(t, rep, "E5")
}
