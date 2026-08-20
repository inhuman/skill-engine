package lint

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	skillengine "github.com/inhuman/skill-engine"
)

// workflowRules — the checks on a skill that is a PROGRAM.
//
// Prose fails softly: the model reads "go to the staging server", does not find
// it and works around it somehow. A program fails hard — a step simply cannot
// call what is not there, and it stops halfway, having burned everything before
// it. So the description is checked before the run rather than during it.
func (r *run) workflowRules(s *skillengine.Skill) {
	if !s.HasWorkflow() {
		return
	}
	flow := s.Workflow

	// W1 — the engine's own refusal. Everything below reads the description as
	// EXECUTION will see it, and Validate is what brings it to that shape:
	// it folds profiles into the steps that name them and moves a `save_as`
	// written at step level into the call it belongs to. Running the rules on
	// the un-normalised form would report on a skill nobody executes.
	if err := flow.Validate(); err != nil {
		r.add("W1", SeverityError, "the description does not pass the engine's validation: %v", err)
		return
	}

	r.workflowServers(s, flow)
	r.workflowTools(flow)
	r.workflowAssets(flow)
	r.workflowCallArgs(flow)
	r.workflowCollect(flow)
	r.workflowEnvelope(flow)
	r.workflowSchemas(flow)
	r.workflowRequiredSlots(flow)
	r.workflowHandles(flow)
	r.workflowBuiltins(s, flow)
	r.declaredBuiltinsExist(s)
	r.workflowToolNamesInText(flow)
	r.workflowVarRefs(flow)
	r.delegateTargets(flow)
}

// W2 — the servers a program names are declared by the skill and exist.
//
// The skill's `servers` is the ceiling of the radius; a step can only narrow
// it. A server named in the program but not in `servers` will not appear at the
// executor out of nowhere.
//
// TWO checks, and only the second needs the installation:
//
//	the program uses a server the skill did not declare  — the skill alone
//	a declared server is not registered                  — Facts.ServerNames
//
// They are two passes for that reason. Interleaved, they were asked for the
// registry on the first DECLARED server, and a missing one abandoned the whole
// walk — so in any healthy skill, where a declared server comes first, the loop
// stopped before reaching the undeclared one below. A rule that switches itself
// off, rather than degrading: a live catalogue reported "0 errors" while
// carrying two, and 21 turns answered "no such pod" about live pods because a
// step called a server its skill never declared.
func (r *run) workflowServers(s *skillengine.Skill, flow *skillengine.Flow) {
	declared := set(s.Servers)

	// Pass 1 — the skill against itself. Nothing outside is needed, so nothing
	// outside can prevent it.
	var used []string
	seen := map[string]bool{}
	for _, srv := range workflowServerNames(flow) {
		// A computed name is not static: what it resolves to is checked at
		// execution against the flow's set, which is what the ceiling is for.
		if seen[srv] || srv == "" || strings.Contains(srv, "{{") {
			continue
		}
		seen[srv] = true
		// builtin is not an MCP server but the pseudo-address of the
		// application's own tools; its radius is `builtin_tools`, and W7
		// checks that one.
		if srv == skillengine.BuiltinServer {
			continue
		}
		if !declared[srv] {
			r.add("W2", SeverityError, "the program uses server `%s`, which is not in the skill's servers — "+
				"the step will not be handed it (servers is the ceiling of the radius, a step can only narrow it)", srv)
			continue
		}
		used = append(used, srv)
	}

	// Pass 2 — the skill against the installation. Asked for only when there is
	// something to ask about, so a skill with no declared servers in use does
	// not file a skip about a check that had no work.
	if len(used) == 0 {
		return
	}
	known, ok := r.servers("W2")
	if !ok {
		return
	}
	for _, srv := range used {
		if !known[srv] {
			r.add("W2", SeverityError, "server `%s` is not registered. Available: %s",
				srv, strings.Join(sortedKeys(known), ", "))
		}
	}
}

// W3 — the tools of `call` steps exist on their servers.
//
// A typo in a call's tool name would surface only at execution, and only on the
// branch execution reached: a switch branch that fires once a week carries the
// typo into production.
func (r *run) workflowTools(flow *skillengine.Flow) {
	refs := flow.DeclaredTools()
	if len(refs) == 0 {
		return
	}
	var tools map[string][]string
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Dynamic || ref.Server == "" || ref.Tool == "" {
			continue // computed at run time — nothing static to check
		}
		key := ref.Server + ":" + ref.Tool
		if seen[key] {
			continue
		}
		seen[key] = true
		if tools == nil {
			var ok bool
			if tools, ok = r.tools("W3"); !ok {
				return
			}
		}
		list, ok := tools[ref.Server]
		if !ok {
			continue // the server's fate is decided by W2
		}
		if !contains(list, ref.Tool) {
			r.add("W3", SeverityError, "a step calls `%s` on server `%s` — there is no such tool there (a typo?). "+
				"It has: %s", ref.Tool, ref.Server, strings.Join(list, ", "))
		}
	}
}

// W4 — assets are used the way their kind implies.
//
// The two ways of passing a payload differ in one thing: whether it goes
// through the model's context. `{{asset:name}}` substitutes the content INTO
// the instruction's text, `{from: asset:name}` hands it to a call's argument
// past the model. Mixing them up is not a matter of style: code in the
// instruction burns context and invites the model to rewrite it, while
// reference material passed by reference never reaches the model at all.
func (r *run) workflowAssets(flow *skillengine.Flow) {
	inText, byRef, inArgText := assetUsage(flow)
	r.assetRefsResolve(flow, inText, byRef, inArgText)
	if len(flow.Assets) == 0 {
		return
	}
	v := r.opts.Assets
	for _, name := range sortedKeys(flow.Assets) {
		a := flow.Assets[name]
		r.assetParams(name, a)

		if len(v.CodeKinds) == 0 && len(v.ReferenceKinds) == 0 {
			r.skip("W4", "the asset kind vocabulary is not configured")
			continue
		}
		switch {
		case contains(v.CodeKinds, a.Kind) && inText[name]:
			r.add("W4", SeverityWarn, "asset `%s` is code, and it is substituted into the instruction's text "+
				"({{asset:%s}}): the body goes through the model's context, and the model starts rewriting it. "+
				"Pass it by reference instead: args: {stdin: {from: \"asset:%s\"}}", name, name, name)
		case contains(v.ReferenceKinds, a.Kind) && (byRef[name] || inArgText[name]) && !inText[name]:
			r.add("W4", SeverityWarn, "asset `%s` is reference material, and it is only passed by reference "+
				"({from: asset:%s}): the model never sees it. If it is knowledge to reason with, substitute "+
				"it: {{asset:%s}}", name, name, name)
		}
	}
}

// W19 — every asset a step references is declared, and W20 — every asset
// declared is referenced.
//
// The engine expands an unknown asset to an EMPTY STRING, deliberately: a
// marker left in an instruction would be read by the model as part of it. That
// contract is right, and it has a price nobody was paying — a typo in the name
// is indistinguishable from an empty asset, and what fails is not the typo but
// whatever stood next to it. A call whose argument came from the missing asset
// is rejected by the server for a MISSING ARGUMENT, and the error names the
// argument rather than the substitution a floor above.
//
// Both reference forms are checked because both are used: `{{asset:name}}` puts
// the content into the text for the model, `{from: "asset:name"}` passes it
// into an argument past the context.
//
// The unused half is a warning, not an error: an asset nobody reads costs
// nothing at run time. It is worth saying because assets outlive the steps that
// used them — a step is rewritten, the payload it carried stays behind.
//
// Purely static, both of them: the declarations and the references are in one
// document, and no registry of anything is involved.
func (r *run) assetRefsResolve(flow *skillengine.Flow, inText, byRef, inArgText map[string]bool) {
	used := map[string]bool{}
	for _, set := range []map[string]bool{inText, byRef, inArgText} {
		for name := range set {
			used[name] = true
		}
	}

	for _, name := range sortedKeys(used) {
		if _, ok := flow.Assets[name]; ok {
			continue
		}
		// The declared names go into the finding: a typo is recognised by
		// comparison, and "countr is not declared" next to "counter" answers
		// itself.
		declared := "the skill declares none"
		if len(flow.Assets) > 0 {
			declared = "declared: " + strings.Join(sortedKeys(flow.Assets), ", ")
		}
		r.add("W19", SeverityError, "a step references asset `%s`, which is not declared — the engine expands "+
			"an unknown asset to an EMPTY STRING, so the failure surfaces wherever that emptiness lands "+
			"(a call losing a required argument, say), never here. %s", name, declared)
	}

	for _, name := range sortedKeys(flow.Assets) {
		if !used[name] {
			r.add("W20", SeverityWarn, "asset `%s` is declared and never referenced — nothing resolves it, "+
				"and it will outlive whatever step used to", name)
		}
	}
}

// W11 — an asset kind's params are readable.
//
// `params` is an OPEN map: the engine does not look inside it and the schema
// does not constrain it. A typo in a key therefore passes everything and does
// NOTHING — the author is sure they declared the language, and declared
// nothing. Before format 2.0.0 `lang` was a typed field and this mistake was
// impossible; the openness was bought at the price of this check.
//
// A warning rather than an error: the skill does not break from a stray key, it
// loses a check before production.
func (r *run) assetParams(name string, a skillengine.Asset) {
	v := r.opts.Assets
	if len(v.KnownParams) == 0 {
		if len(a.Params) > 0 {
			r.skip("W11", "the list of asset params the resolver reads is not configured")
		}
	} else {
		for _, k := range sortedKeys(a.Params) {
			if !contains(v.KnownParams, k) {
				r.add("W11", SeverityWarn, "asset `%s`: nobody reads param `%s` — the resolver knows only %s. "+
					"The params map is open, so the schema will not catch the typo",
					name, k, strings.Join(v.KnownParams, ", "))
			}
		}
	}
	if v.LangParam != "" && contains(v.CodeKinds, a.Kind) &&
		strings.TrimSpace(paramString(a, v.LangParam)) == "" {
		r.add("W11", SeverityWarn, "asset `%s` is code without `params.%s`: the language is not named, "+
			"so there is nothing to check the syntax with — an error in the body waits for production",
			name, v.LangParam)
	}
}

// paramString reads a string param. The map is untyped by the engine's design;
// the conversion is the reader's business.
func paramString(a skillengine.Asset, key string) string {
	s, _ := a.Params[key].(string)
	return s
}

// assetUsage marks how each asset is used by the program.
func assetUsage(flow *skillengine.Flow) (inText, byRef, inArgText map[string]bool) {
	inText, byRef, inArgText = map[string]bool{}, map[string]bool{}, map[string]bool{}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Run != nil {
			for _, n := range skillengine.AssetRefsInText(s.Run.Instruction) {
				inText[n] = true
			}
		}
		if s.Call != nil {
			for _, n := range skillengine.AssetRefsInArgs(s.Call.Args) {
				byRef[n] = true
			}
			for _, n := range skillengine.AssetRefsInText(s.Call.Tool) {
				inText[n] = true
			}
			// A call passes an asset in TWO ways, and only one of them was
			// counted here. `{from: "asset:x"}` hands the body over past the
			// model; `code: "{{asset:x}}"` substitutes it into an argument as
			// text. The second is how a script gets into an exec call, so the
			// rule about unused assets reported live payloads as dead — 19 of
			// them in one catalogue, none real. A finding that is wrong that
			// often is worse than a missing one: it teaches people to skim the
			// report past everything.
			//
			// Kept SEPARATE from instruction text on purpose. The warning about
			// code reaching the model applies to an instruction, where the
			// model reads what was substituted; a `call` is executed by the
			// host, and its arguments never enter a prompt. Folding the two
			// together would have traded one false finding for another.
			for _, n := range assetRefsInArgText(s.Call.Args) {
				inArgText[n] = true
			}
		}
	})
	return inText, byRef, inArgText
}

// assetRefsInArgText finds `{{asset:name}}` inside argument VALUES, at any
// depth: arguments nest, and a payload is as often two levels down (`args.code`
// under a branch) as at the top.
func assetRefsInArgText(args map[string]any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, sub := range t {
				walk(sub)
			}
		case []any:
			for _, sub := range t {
				walk(sub)
			}
		case string:
			out = append(out, skillengine.AssetRefsInText(t)...)
		}
	}
	walk(args)
	return out
}

// workflowServerNames collects the server names the program mentions.
func workflowServerNames(flow *skillengine.Flow) []string {
	out := append([]string{}, flow.Tools...)
	for _, ref := range flow.DeclaredTools() {
		if ref.Server != "" {
			out = append(out, ref.Server)
		}
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Run != nil && s.Run.Tools != nil {
			out = append(out, *s.Run.Tools...)
		}
		if s.OnServer != "" {
			out = append(out, s.OnServer)
		}
	})
	return out
}

// W12 — a step's instruction names a tool without saying how tools are called.
//
// Where an application hands a step ONE calling tool rather than a function per
// tool name, an instruction that says "fetch it with get_page" reads to the
// model as a list of available functions, and it calls the name directly. That
// costs at least an extra turn of the model, and where the runtime does not
// repair such a call, the whole turn: the error propagates and the step is
// never done.
//
// A warning, not an error: the skill works, it just costs more.
func (r *run) workflowToolNamesInText(flow *skillengine.Flow) {
	// Only a step that was HANDED tools can call a name: elsewhere nothing
	// breaks, and there is nothing to make noise about. Collected before the
	// facts are asked for, so a skill with no such step does not file a skip
	// about a rule that had nothing to do.
	type candidate struct{ label, text string }
	var candidates []candidate
	walkSteps(flow.Steps, func(s *skillengine.Step, idx int) {
		if s.Run != nil && s.Run.Tools != nil && len(*s.Run.Tools) > 0 {
			candidates = append(candidates, candidate{stepLabel(s, idx), s.Run.Instruction})
		}
	})
	if len(candidates) == 0 {
		return
	}
	protocol, ok := r.callProtocol("W12")
	if !ok {
		return
	}
	catalog, ok := r.tools("W12")
	if !ok {
		return
	}
	known := map[string]bool{}
	for _, tools := range catalog {
		for _, t := range tools {
			known[t] = true
		}
	}
	for _, c := range candidates {
		if strings.Contains(c.text, protocol) {
			continue // the protocol is named — the model knows how to call
		}
		for _, tn := range toolNamesMentioned(c.text, known) {
			r.add("W12", SeverityWarn, "step %q mentions tool `%s` but does not say that tools are called "+
				"through %s — the model will call the name directly. Add to the instruction: "+
				"%s(server=\"…\", tool=\"%s\", args={…})", c.label, tn, protocol, protocol, tn)
		}
	}
}

// toolNamesMentioned finds catalogue names in a text. Word boundaries matter:
// without them `search` would match inside `research`.
func toolNamesMentioned(text string, known map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range toolWordRE.FindAllString(text, -1) {
		if known[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// toolWordRE — a word that could be a tool's name. Single words without an
// underscore are caught too: the catalogue decides, not the shape.
var toolWordRE = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_]*`)

// stepLabel — how to name a step in a finding: by its name, or by its place.
func stepLabel(s *skillengine.Step, idx int) string {
	if s.Name != "" {
		return s.Name
	}
	return "#" + strconv.Itoa(idx+1)
}

// W5 — a `call` step carries the arguments its tool requires.
//
// W3 checks the name, but a call with the right name and incomplete arguments
// is rejected by the SERVER — at execution, mid-turn, having spent everything
// before it. Only the PRESENCE of keys is checked: values come from variables
// and are statically unknown.
func (r *run) workflowCallArgs(flow *skillengine.Flow) {
	steps := callSteps(flow)
	if len(steps) == 0 {
		return
	}
	schemas, ok := r.toolSchemas("W5")
	if !ok {
		return
	}
	for _, c := range steps {
		if c.server == "" || c.tool == "" {
			continue // a computed address — nothing static to check
		}
		raw, ok := schemas[c.server+":"+c.tool]
		if !ok {
			continue // an unknown tool's fate is decided by W3
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if json.Unmarshal(raw, &schema) != nil {
			continue
		}
		for _, req := range schema.Required {
			if _, has := c.args[req]; !has {
				r.add("W5", SeverityError, "a step calls `%s:%s` without the required argument `%s` — "+
					"the server will reject the call at execution", c.server, c.tool, req)
			}
		}
	}
}

// callRef — a call step with a resolved address.
type callRef struct {
	server, tool string
	args         map[string]any
}

// callSteps collects the call steps together with their addresses.
func callSteps(flow *skillengine.Flow) []callRef {
	var out []callRef
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Call == nil || strings.Contains(s.OnServer, "{{") {
			return
		}
		server, tool, ok := skillengine.SplitToolRef(s.Call.Tool)
		if s.OnServer != "" {
			server = s.OnServer
			if _, bare, split := skillengine.SplitToolRef(s.Call.Tool); split {
				tool = bare
			} else {
				tool = s.Call.Tool
			}
		} else if !ok {
			return
		}
		out = append(out, callRef{server: server, tool: tool, args: s.Call.Args})
	})
	return out
}

// W6 — somebody writes into a loop's `collect` variable.
//
// The loop gathers the variable named by `collect`: a step inside the body has
// to write into THAT one via `save_as`. If none does, the loop honestly runs
// every iteration and gathers nothing, and the next step words its answer over
// an empty space. Nothing fails: the run is green, the answer looks fine.
func (r *run) workflowCollect(flow *skillengine.Flow) {
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.ForEach == nil || s.ForEach.Collect == "" {
			return
		}
		if !writesVar(s.ForEach.Steps, s.ForEach.Collect) {
			r.add("W6", SeverityError, "the loop collects `%s`, and no step in its body writes into it — "+
				"what is gathered will stay empty. Add `save_as: %s` to the step whose result accumulates",
				s.ForEach.Collect, s.ForEach.Collect)
		}
	})
}

// writesVar reports whether any step of a set writes into the named variable.
func writesVar(steps []skillengine.Step, name string) bool {
	found := false
	walkSteps(steps, func(s *skillengine.Step, _ int) {
		if found {
			return
		}
		switch {
		case s.Run != nil && s.Run.SaveAs == name,
			s.Call != nil && s.Call.SaveAs == name,
			s.Delegate != nil && s.Delegate.SaveAs == name,
			s.Set != nil && s.Set.Var == name,
			s.Parallel != nil && s.Parallel.Collect == name,
			s.ForEach != nil && s.ForEach.Collect == name:
			found = true
		}
		// A step without save_as writes into the answer variable — also a write.
		if s.Run != nil && s.Run.SaveAs == "" && strings.TrimSpace(s.Run.Instruction) != "" &&
			name == skillengine.AnswerVar {
			found = true
		}
	})
	return found
}

// W7 — the built-in tools a `call: builtin:<name>` step uses are declared.
//
// The same floor as `servers` for MCP servers: undeclared means not handed to
// the executor. Separate from E5 because E5 reads PROSE — what a playbook tells
// the executor to call — while here the address is named structurally.
func (r *run) workflowBuiltins(s *skillengine.Skill, flow *skillengine.Flow) {
	declared := set(s.BuiltinTools)
	seen := map[string]bool{}
	for _, c := range callSteps(flow) {
		if c.server != skillengine.BuiltinServer || seen[c.tool] {
			continue
		}
		seen[c.tool] = true
		if !declared[c.tool] {
			r.add("W7", SeverityError, "a step calls built-in `%s`, and the skill does not declare it in "+
				"builtin_tools — the executor will not be handed it. Add: builtin_tools: [%s]", c.tool, c.tool)
		}
	}
}

// W15 — a declared built-in tool exists.
//
// `builtin_tools` is a REQUEST, and a dispatcher typically drops an unknown
// name with a line in a log. The skill loads, the step runs, the tool is not
// there — and the failure reads as "the model did not figure it out".
func (r *run) declaredBuiltinsExist(s *skillengine.Skill) {
	if len(s.BuiltinTools) == 0 {
		return
	}
	known, available, ok := r.builtins("W15")
	if !ok {
		return
	}
	for _, want := range s.BuiltinTools {
		if known[want] {
			continue
		}
		r.add("W15", SeverityError, "the skill declares built-in `%s`, which is not in the registry — "+
			"the executor will not be handed it, and the failure will look like the model's fault. "+
			"Available: %s", want, strings.Join(available, ", "))
	}
}

// W8 — the result of a call is substituted whole where a field was meant.
//
// Some servers return an ENVELOPE rather than what the call produced: an exit
// code, stdout, stderr around the payload. `{{var}}` gives the envelope, and a
// consumer expecting the payload gets an object with its data hidden in a
// string inside.
//
// What is dangerous here is not the breakage but its absence: everything stays
// green. The loop honestly makes one iteration over the envelope, the renderer
// honestly prints "no findings", the rejection counters honestly show zeros —
// because there was nothing to reject.
func (r *run) workflowEnvelope(flow *skillengine.Flow) {
	if len(r.opts.Envelopes) == 0 {
		return
	}
	// variable → the envelope it holds.
	wrapped := map[string]Envelope{}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Call == nil || s.Call.SaveAs == "" {
			return
		}
		if env, ok := r.envelopeOf(s); ok {
			wrapped[s.Call.SaveAs] = env
		}
	})
	if len(wrapped) == 0 {
		return
	}

	// The severity depends on WHO reads the substitution. A script or a loop
	// will not unwrap it — there the wrong result is silent, and that is an
	// error. A model will read the envelope and dig the payload out of it:
	// noise in the context and a reason to retell an exit code as part of the
	// answer, but not breakage — a warning.
	report := func(v, where string, env Envelope, sev Severity) {
		fields := ""
		if len(env.Fields) > 0 {
			fields = " {" + strings.Join(env.Fields, ", ") + "}"
		}
		r.add("W8", sev, "%s `%s` whole, and that is `%s`'s envelope%s — the payload sits inside as a "+
			"string. Take the field: `%s.%s`", where, v, env.Server, fields, v, env.Payload)
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.ForEach != nil {
			if env, ok := wrapped[strings.TrimSpace(s.ForEach.In)]; ok {
				report(strings.TrimSpace(s.ForEach.In), "the loop iterates over", env, SeverityError)
			}
		}
		if s.Call != nil && len(s.Call.Args) > 0 {
			if raw, err := json.Marshal(s.Call.Args); err == nil {
				for _, v := range sortedKeys(wrapped) {
					if strings.Contains(string(raw), "{{"+v+"}}") {
						report(v, "the call passes", wrapped[v], SeverityError)
					}
				}
			}
		}
		if s.Run != nil {
			for _, v := range sortedKeys(wrapped) {
				if strings.Contains(s.Run.Instruction, "{{"+v+"}}") {
					report(v, "the step's instruction takes", wrapped[v], SeverityWarn)
				}
			}
		}
	})
}

// envelopeOf reports whether a step's address returns a wrapped result.
func (r *run) envelopeOf(s *skillengine.Step) (Envelope, bool) {
	server := s.OnServer
	if server == "" {
		server, _, _ = skillengine.SplitToolRef(s.Call.Tool)
	}
	for _, env := range r.opts.Envelopes {
		if env.Server == server {
			return env, true
		}
	}
	return Envelope{}, false
}

// W9 — an object in a structured answer has at least one required field.
//
// Anything outside `required` the model is entitled not to send, and under load
// it uses that right: a long diff, many findings — and a field that is in the
// schema but not in `required` starts going missing.
//
// The whole chain stays quiet about it: the schema is satisfied, the step is
// `ok`, the answer parses. A live miss: findings carried a `file` that was
// declared and not required; the model sent six findings without paths, the
// delta filter dropped all six as "outside the delta", and the report said
// "no remarks".
//
// An object with nothing required permits an empty `{}`, which is almost always
// an oversight rather than a design.
//
// The exception is when emptiness is HANDLED: the flow branches on `x is
// empty`, i.e. "there is no value" is a legitimate answer for it rather than
// silence. Demanding `required` there would be demanding the impossible — a
// schema with a single field the request may genuinely not contain cannot make
// it required (see W16) and could not leave it optional under this rule.
func (r *run) workflowSchemas(flow *skillengine.Flow) {
	// Only the NAME half of W13 needs words; the structural half — a string
	// inside an array — is a runaway risk in any language and always runs.
	if len(r.opts.FreeTextFields) == 0 && hasResponseSchema(flow) {
		r.skip("W13", "the names that mark a field as free text are not configured; "+
			"only fields inside arrays are checked")
	}
	handled := emptinessHandled(flow)
	var scan func(node any, where, saveAs string, inArray bool)
	scan = func(node any, where, saveAs string, inArray bool) {
		switch v := node.(type) {
		case map[string]any:
			props, hasProps := v["properties"].(map[string]any)
			if hasProps && len(props) > 0 {
				if req, _ := v["required"].([]any); len(req) == 0 && !handled[saveAs] {
					r.add("W9", SeverityError, "the response schema of step `%s`: the object with fields %s "+
						"has no `required` — the model is entitled to send none of them, and the consumer "+
						"waits for them in silence", where, strings.Join(sortedKeys(props), ", "))
				}
				// W13 is checked at the OBJECT level, not the field's: the
				// field's name is only visible from here, and without it the
				// finding would point nowhere.
				for _, field := range sortedKeys(props) {
					sub, ok := props[field].(map[string]any)
					if !ok || !freeTextField(sub) || !r.freeTextRisk(field, inArray) {
						continue
					}
					if _, has := sub["maxLength"]; !has {
						r.add("W13", SeverityWarn, "the response schema of step `%s`: string field `%s` has no "+
							"`maxLength` — the model is entitled to write up to the token ceiling, break off "+
							"mid-string and take the WHOLE document with it. Take the ceiling from the data "+
							"(p95 of the field's length), not from a guess", where, field)
					}
				}
			}
			// An array OF STRINGS needs a ceiling no less than a field does,
			// and it has no `properties` — the check above cannot see it.
			if items, ok := v["items"].(map[string]any); ok && freeTextField(items) {
				if _, has := items["maxLength"]; !has {
					r.add("W13", SeverityWarn, "the response schema of step `%s`: an array of strings whose items "+
						"have no `maxLength` — every entry may write up to the token ceiling. Bound both the "+
						"item's length and the number of entries (`maxItems`)", where)
				}
			}
			for k, item := range v {
				if k == "required" || k == "enum" {
					continue
				}
				// Below `items` an array's ELEMENT begins: every entry carries
				// its own text there, and breaking off on one takes the whole
				// document. The emptiness exemption does NOT descend: the branch
				// handles the step's empty ANSWER, not an empty entry inside it.
				scan(item, where, "", inArray || k == "items")
			}
		case []any:
			for _, item := range v {
				scan(item, where, saveAs, inArray)
			}
		}
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Run != nil && len(s.Run.ResponseSchema) > 0 {
			scan(s.Run.ResponseSchema, s.Name, s.Run.SaveAs, false)
		}
	})
}

// emptinessHandled — the variables whose emptiness the flow examines itself
// (`rel.id is empty`, `pick is empty`). For those, "the model sent nothing" is
// not silence but an answer with a branch of its own.
func emptinessHandled(flow *skillengine.Flow) map[string]bool {
	out := map[string]bool{}
	mark := func(cond string) {
		expr, ok := strings.CutSuffix(strings.TrimSpace(cond), "is empty")
		if !ok {
			return
		}
		name, _, _ := strings.Cut(strings.TrimSpace(expr), ".")
		if name != "" {
			out[name] = true
		}
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.If != nil {
			mark(s.If.Cond)
		}
		if s.When != "" {
			mark(s.When)
		}
	})
	return out
}

// W16 — a required field the description beside it allows to be empty.
//
// The schema demands the field, the instruction says "not named — an empty
// string". No output satisfies both, and the model picks one of two — both
// observed live:
//
//   - it writes filler prose into the field. A parsing step asked for a ticket
//     key on a request that named none produced `key = "ABC-1, ABC-2, … (the
//     list of keys, if the data is available)"`; the `key is empty` branch did
//     not fire (the string is not empty), the call went out with rubbish, and
//     the turn assembled an answer out of thin air — a table of tickets that do
//     not exist;
//   - it writes nothing at all. Then the decoding grammar will not let it close
//     the `}` until the required field is produced, while whitespace between
//     JSON tokens is always legal: the model walks off into whitespace up to the
//     token ceiling. One parsing step failed that way on 4 requests out of 4.
//
// A DEFAULT is not a contradiction: "not named — use staging" gives a legal
// output, and such a field may be required. This is only about EMPTINESS.
//
// A note saying "always send the key" does not fix it — it was already there in
// the live case and did not help: the instruction argues with itself, and it is
// the model that executes.
func (r *run) workflowRequiredSlots(flow *skillengine.Flow) {
	if len(r.opts.EmptyWords) == 0 {
		if hasResponseSchema(flow) {
			r.skip("W16", "the words meaning \"empty\" are not configured")
		}
		return
	}
	var scan func(node any, step, instruction string)
	scan = func(node any, step, instruction string) {
		switch v := node.(type) {
		case map[string]any:
			props, _ := v["properties"].(map[string]any)
			req, _ := v["required"].([]any)
			for _, item := range req {
				field, ok := item.(string)
				if !ok {
					continue
				}
				sub, _ := props[field].(map[string]any)
				desc, _ := sub["description"].(string)
				where := fieldMention(instruction, field, sortedKeys(props))
				switch {
				case r.emptyAllowed(desc):
					r.requiredSlot(step, field, desc)
				case r.emptyAllowed(where):
					r.requiredSlot(step, field, where)
				}
			}
			for k, item := range v {
				if k == "required" || k == "enum" {
					continue
				}
				scan(item, step, instruction)
			}
		case []any:
			for _, item := range v {
				scan(item, step, instruction)
			}
		}
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Run != nil && len(s.Run.ResponseSchema) > 0 {
			scan(s.Run.ResponseSchema, s.Name, s.Run.Instruction)
		}
	})
}

func (r *run) requiredSlot(step, field, quote string) {
	r.add("W16", SeverityError, "the response schema of step `%s`: field `%s` is required, and the description "+
		"allows it to be empty (%q). The model cannot both send a value and not have one: it will either "+
		"invent it or walk off into whitespace up to the token ceiling. Take the field out of `required` "+
		"and branch on its absence — or give it a default instead of emptiness",
		step, field, squeeze(quote))
}

// emptyAllowed — the description permits the field to be empty. It looks for
// EMPTINESS specifically: "not named — 30" is a default, a legal output, and
// the rule stays quiet about it.
//
// The words come from the embedder (Options.EmptyWords): skills are written in
// the language of whoever writes them, and a list baked into the library would
// silently do nothing for everyone who writes in another one.
//
// A match must start a word, and that check is the engine's — the same one
// behind the `contains` condition, so there is ONE definition of "starts a
// word" rather than two that drift. It matters here: without it "пуст" is found
// inside "перезапустить", which is a false finding this rule produced on its
// very first run. (Go's own `\b` is ASCII-only and would not have helped.)
func (r *run) emptyAllowed(s string) bool {
	for _, w := range r.opts.EmptyWords {
		if skillengine.ContainsWord(s, w) {
			return true
		}
	}
	return false
}

// fieldMention — the part of an instruction that describes a field.
//
// The description starts at the line naming the field and runs until the next
// field takes over or the paragraph ends. Where it ENDS is the whole point:
// taking the instruction whole, or even a whole paragraph, lets "an empty
// string" from the NEXT field's line hang the finding on this one — which is
// what happened to a shipped example, where two fields sat on adjacent lines
// and the one with a legitimate default was reported.
//
// siblings are the object's other field names: they are what tells "the next
// field starts here" from an ordinary continuation line.
func fieldMention(instruction, field string, siblings []string) string {
	lines := strings.Split(instruction, "\n")
	start := -1
	for i, line := range lines {
		if declaresField(line, field) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	out := []string{strings.TrimSpace(lines[start])}
	for _, line := range lines[start+1:] {
		if strings.TrimSpace(line) == "" || startsAnotherField(line, field, siblings) {
			break
		}
		out = append(out, strings.TrimSpace(line))
	}
	return strings.Join(out, " ")
}

// declaresField — the line introduces the field: its name, then a dash or a
// colon. "namespace — the namespace" declares it; "use namespace as given" does
// not.
func declaresField(line, field string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), field)
	if !ok {
		return false
	}
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, "—") || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, ":")
}

func startsAnotherField(line, field string, siblings []string) bool {
	for _, s := range siblings {
		if s != field && declaresField(line, s) {
			return true
		}
	}
	return false
}

// freeTextRisk answers whether this field could take the document with it.
//
// Not every string is a risk. `env`, `id`, `project` are slots: the model writes
// a word there, and it does not break off. Demanding a ceiling from those fills
// the report with noise, and noise is what stops the real findings being read.
//
// The risk comes from one of two things:
//   - the field sits inside an ARRAY: many entries, each with its own text, and
//     breaking off on one takes the whole document;
//   - a name behind which stands FREE text rather than a value.
func (r *run) freeTextRisk(name string, inArray bool) bool {
	if inArray {
		return true
	}
	low := strings.ToLower(name)
	for _, marker := range r.opts.FreeTextFields {
		if strings.Contains(low, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// freeTextField — a string field that FREE text is written into.
//
// An enumeration (`enum`) and a field with a format do not count: their length
// is set by the list of values, and demanding a ceiling from them is noise.
func freeTextField(field map[string]any) bool {
	if t, _ := field["type"].(string); t != "string" {
		return false
	}
	if _, isEnum := field["enum"]; isEnum {
		return false
	}
	if _, hasFormat := field["format"]; hasFormat {
		return false
	}
	return true
}

// W10 — `from:` in a call's arguments receives a HANDLE, not content.
//
// `from:` passes a payload by reference: an asset (`asset:name`) or a
// working-memory handle. A `{{var}}` substitution gives the value's TEXT, and
// the executor then goes looking for a handle whose name starts with the first
// bytes of that text.
//
// A live miss: `from: {{tools_out}}` instead of `{{tools_out.mem}}` — the step
// failed with "handle [== BUILD ==… not found", the report was never assembled,
// and it only came out of a live run. The handle of a result delivered into
// working memory is always `<var>` plus the engine's memory suffix.
func (r *run) workflowHandles(flow *skillengine.Flow) {
	var check func(node any, step string)
	check = func(node any, step string) {
		switch v := node.(type) {
		case map[string]any:
			if s, ok := v["from"].(string); ok && strings.Contains(s, "{{") &&
				!strings.HasSuffix(strings.TrimSpace(s), skillengine.MemSuffix+"}}") {
				r.add("W10", SeverityError, "step `%s`: `from: %s` will substitute the value's TEXT, and `from` "+
					"expects a handle — the executor will go looking for one by the first bytes of that text. "+
					"The handle of a result delivered into memory is `%s` on the same variable",
					step, s, skillengine.MemSuffix)
			}
			for _, item := range v {
				check(item, step)
			}
		case []any:
			for _, item := range v {
				check(item, step)
			}
		}
	}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Call != nil && len(s.Call.Args) > 0 {
			check(s.Call.Args, s.Name)
		}
	})
}

// E4 — a `delegate` step names a skill that exists.
//
// Delegation is how composite skills are built, and the name is a plain string:
// a renamed or deleted skill leaves the reference looking exactly as it did.
// A warning rather than an error, because the catalogue a skill is checked
// against is not always the one it will run in — an installation may carry a
// different set.
func (r *run) delegateTargets(flow *skillengine.Flow) {
	var targets []string
	seen := map[string]bool{}
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Delegate == nil || s.Delegate.Skill == "" || seen[s.Delegate.Skill] ||
			strings.Contains(s.Delegate.Skill, "{{") {
			return
		}
		seen[s.Delegate.Skill] = true
		targets = append(targets, s.Delegate.Skill)
	})
	if len(targets) == 0 {
		return
	}
	known, ok := r.skillNames("E4")
	if !ok {
		return
	}
	for _, t := range targets {
		if !known[t] {
			r.add("E4", SeverityWarn, "a step delegates to skill `%s`, which is not in the catalogue", t)
		}
	}
}

// walkSteps visits every step of a description, nesting included.
//
// One walk for all the rules: each of them re-enumerating the places nesting
// can occur is how the one added last gets forgotten — which is exactly how a
// typo inside a switch branch reaches production. The nesting itself is
// enumerated by the engine (Step.Branches), so a new kind of branch is picked
// up here without an edit.
func walkSteps(steps []skillengine.Step, visit func(s *skillengine.Step, idx int)) {
	for i := range steps {
		s := &steps[i]
		visit(s, i)
		for _, br := range s.Branches() {
			walkSteps(br, visit)
		}
	}
}

// sortedKeys — a map's keys in a stable order, so that a finding listing them
// does not read as a new finding on the next run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasResponseSchema reports whether any step declares a structured answer —
// asked before a rule files a skip, so a skill with no schemas does not collect
// notes about rules that had nothing to do.
func hasResponseSchema(flow *skillengine.Flow) bool {
	found := false
	walkSteps(flow.Steps, func(s *skillengine.Step, _ int) {
		if s.Run != nil && len(s.Run.ResponseSchema) > 0 {
			found = true
		}
	})
	return found
}
