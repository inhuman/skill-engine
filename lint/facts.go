package lint

// The accessors below are the ONE place a missing fact turns into a skip.
//
// Written this way because the alternative was measured elsewhere: with every
// rule asking for its own dependency, one of them eventually forgets to record
// the skip, and that rule silently reports nothing whenever the dependency is
// down. A clean report and an unrun rule then look identical — which is the
// exact failure the degradation contract exists to prevent.
//
// Each accessor returns ok=false having ALREADY recorded the reason, so a rule
// reads as: get the fact, or return.

// servers — the registered server names. W2, E1.
func (r *run) servers(rule string) (map[string]bool, bool) {
	if r.facts.ServerNames == nil {
		r.skip(rule, "the server registry is unavailable")
		return nil, false
	}
	names := r.facts.ServerNames()
	if len(names) == 0 {
		r.skip(rule, "the server registry is empty")
		return nil, false
	}
	return set(names), true
}

// tools — server → its tools. W3, W12, E2.
func (r *run) tools(rule string) (map[string][]string, bool) {
	if r.facts.AllTools == nil {
		r.skip(rule, "the tool listing is unavailable")
		return nil, false
	}
	tools := r.facts.AllTools()
	if len(tools) == 0 {
		r.skip(rule, "the tool listing is empty")
		return nil, false
	}
	return tools, true
}

// toolSchemas — "server:tool" → the tool's input schema. W5.
func (r *run) toolSchemas(rule string) (map[string][]byte, bool) {
	if r.facts.ToolSchemas == nil {
		r.skip(rule, "tool schemas are unavailable")
		return nil, false
	}
	schemas := r.facts.ToolSchemas()
	if len(schemas) == 0 {
		r.skip(rule, "tool schemas are empty")
		return nil, false
	}
	return schemas, true
}

// builtins — the built-in tools the application has. W15, E5. The slice is
// returned alongside the set: the findings list what IS available, and a
// message that lists it in a different order every run reads as a new finding.
func (r *run) builtins(rule string) (map[string]bool, []string, bool) {
	if r.facts.BuiltinTools == nil {
		r.skip(rule, "the built-in tool registry is unavailable")
		return nil, nil, false
	}
	names := r.facts.BuiltinTools()
	if len(names) == 0 {
		r.skip(rule, "the built-in tool registry is empty")
		return nil, nil, false
	}
	return set(names), names, true
}

// writeServers — the servers that change something. E3.
//
// An EMPTY answer is a legitimate one here, unlike everywhere else: an
// installation may genuinely have no writing servers, and treating that as a
// missing fact would file a skip on every run of a read-only installation.
func (r *run) writeServers(rule string) (map[string]bool, bool) {
	if r.facts.WriteServers == nil {
		r.skip(rule, "the list of writing servers is unavailable")
		return nil, false
	}
	return set(r.facts.WriteServers()), true
}

// skillNames — the catalogue a delegate step can name. E4.
func (r *run) skillNames(rule string) (map[string]bool, bool) {
	if r.facts.SkillNames == nil {
		r.skip(rule, "the skill catalogue is unavailable")
		return nil, false
	}
	names := r.facts.SkillNames()
	if len(names) == 0 {
		r.skip(rule, "the skill catalogue is empty")
		return nil, false
	}
	return set(names), true
}

// callProtocol — the name a tool call goes through. W12, E2.
func (r *run) callProtocol(rule string) (string, bool) {
	if r.opts.CallProtocol == "" {
		r.skip(rule, "the name of the call protocol is not configured")
		return "", false
	}
	return r.opts.CallProtocol, true
}

func set(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out
}
