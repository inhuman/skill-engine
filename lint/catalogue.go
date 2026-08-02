package lint

// Rule — one entry of the catalogue: what a rule catches and what it needs to
// run at all.
//
// The catalogue exists so an embedder can show the list without reading the
// source, and so that "which rules did not run, and why" has an answer before
// the run rather than after it. A test keeps it honest in both directions: a
// rule the code emits and the catalogue does not list is a rule nobody can look
// up, and a catalogued rule that fires nowhere is a rule that was renamed or
// quietly lost its call site.
type Rule struct {
	// ID — the rule's number, e.g. "W14". The numbers are owned by this
	// package: while rules were being added on both sides of the boundary they
	// collided, and a number that means two things in two places is worse than
	// no number.
	ID string
	// Title — what it catches, in one line.
	Title string
	// Emits — the severities the rule can produce.
	Emits []Severity
	// Needs — the Facts and Options fields the rule cannot run without. Empty
	// means it only needs the skill itself.
	Needs []string
}

// Rules returns the catalogue, ordered by id.
func Rules() []Rule {
	return []Rule{
		{ID: "S1", Emits: []Severity{SeverityError},
			Title: "the file parses, the header is legal, and the format version is one this engine speaks"},
		{ID: "S3", Emits: []Severity{SeverityError}, Needs: []string{"Options.StaleAPIs"},
			Title: "the playbook uses a construct the embedder has removed"},
		{ID: "S5", Emits: []Severity{SeverityWarn},
			Title: "the playbook's size against the budget — it is context weight on every run"},
		{ID: "S6", Emits: []Severity{SeverityInfo},
			Title: "no trigger_examples: the skill is only reachable by being named outright"},

		{ID: "W1", Emits: []Severity{SeverityError},
			Title: "the description does not pass the engine's own validation"},
		{ID: "W2", Emits: []Severity{SeverityError}, Needs: []string{"Facts.ServerNames"},
			Title: "a server the program names is declared by the skill and registered"},
		{ID: "W3", Emits: []Severity{SeverityError}, Needs: []string{"Facts.AllTools"},
			Title: "a call step's tool exists on its server"},
		{ID: "W4", Emits: []Severity{SeverityWarn}, Needs: []string{"Options.Assets"},
			Title: "an asset is passed the way its kind implies — through the model's context or past it"},
		{ID: "W5", Emits: []Severity{SeverityError}, Needs: []string{"Facts.ToolSchemas"},
			Title: "a call step carries the arguments its tool requires"},
		{ID: "W6", Emits: []Severity{SeverityError},
			Title: "somebody writes into the variable a loop collects"},
		{ID: "W7", Emits: []Severity{SeverityError},
			Title: "a built-in tool called by a step is declared in builtin_tools"},
		{ID: "W8", Emits: []Severity{SeverityError, SeverityWarn}, Needs: []string{"Options.Envelopes"},
			Title: "a wrapped call result is substituted whole where a field was meant"},
		{ID: "W9", Emits: []Severity{SeverityError},
			Title: "an object in a response schema has at least one required field"},
		{ID: "W10", Emits: []Severity{SeverityError},
			Title: "`from:` in a call's arguments receives a handle, not the value's text"},
		{ID: "W11", Emits: []Severity{SeverityWarn}, Needs: []string{"Options.Assets"},
			Title: "an asset's params are keys the resolver actually reads"},
		{ID: "W12", Emits: []Severity{SeverityWarn}, Needs: []string{"Options.CallProtocol", "Facts.AllTools"},
			Title: "an instruction names a tool without saying how tools are called"},
		{ID: "W13", Emits: []Severity{SeverityWarn},
			Title: "a free-text field of a response schema has a length ceiling"},
		{ID: "W14", Emits: []Severity{SeverityError},
			Title: "every reference — a {{template}} or a bare name in a condition — names a variable that exists at that point"},
		{ID: "W15", Emits: []Severity{SeverityError}, Needs: []string{"Facts.BuiltinTools"},
			Title: "a declared built-in tool exists in the application's registry"},
		{ID: "W16", Emits: []Severity{SeverityError},
			Title: "a required field is not one the description beside it allows to be empty"},
		{ID: "W17", Emits: []Severity{SeverityError},
			Title: "`switch.var` is given a variable's name, not a {{template}}"},

		{ID: "E1", Emits: []Severity{SeverityError}, Needs: []string{"Facts.ServerNames"},
			Title: "every server the skill declares is registered"},
		{ID: "E2", Emits: []Severity{SeverityError}, Needs: []string{"Options.CallProtocol", "Facts.AllTools"},
			Title: "a tool the playbook calls exists on the server it names"},
		{ID: "E3", Emits: []Severity{SeverityWarn}, Needs: []string{"Options.ReadOnlyRoles", "Facts.WriteServers"},
			Title: "a skill that calls itself read-only does not reach for a server that writes"},
		{ID: "E4", Emits: []Severity{SeverityWarn}, Needs: []string{"Facts.SkillNames"},
			Title: "a delegate step names a skill that exists"},
		{ID: "E5", Emits: []Severity{SeverityError}, Needs: []string{"Facts.BuiltinTools"},
			Title: "a built-in tool the playbook says to call is declared in builtin_tools"},

		{ID: SkipRule, Emits: []Severity{SeverityInfo},
			Title: "a rule did not run, and why — so a partial check is not read as a clean one"},
	}
}
