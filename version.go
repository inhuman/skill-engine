package skillengine

import (
	"fmt"
	"strconv"
	"strings"
)

// Format and skill versions (semver).
//
// TWO DIFFERENT things, and confusing them is expensive:
//
//	EngineVersion       — the FORMAT version this engine understands;
//	Skill.EngineVersion — the version a skill was written for, i.e. the
//	                      minimum engine it requires.
//
// Why at all. User skills live in file storage and are NOT updated together
// with a deploy: change the meaning of a field and we silently change the
// behaviour of other people's skills, written under the old rules. The live
// risk is `tools: []` — today it means "hand out no tools", a central
// construct of the format; let an empty list start meaning "the flow's set"
// and old skills gain access their author deliberately withheld.

// EngineVersion — the format version supported by this engine.
//
//	major — incompatible change (a skill of a previous major does not load
//	        without migration);
//	minor — an optional field was added (a skill using it requires an engine
//	        no older than that minor);
//	patch — engine fixes, the format did not change.
const EngineVersion = "2.1.0"

// LegacyEngineVersion — what counts as the declared version when the field is
// absent (skills written before it was introduced).
//
// From major 2 on, such a skill is rejected rather than executed: it was
// written under 1.x rules, and parsing it silently would drop fields that no
// longer exist in the structs.
const LegacyEngineVersion = "1.0.0"

// CheckEngineVersion reports whether the engine can execute a skill.
//
// The refusal is EXPLICIT rather than a silent run under the old rules: a
// skill that needs fields from a future version would, on a quiet fallback,
// work "somehow" — that is, produce a plausible wrong result instead of an
// honest error. Same class of failure as the model bridge where a dropped
// field vanished instead of being rejected.
func CheckEngineVersion(declared string) error {
	if declared == "" {
		declared = LegacyEngineVersion
	}
	want, err := parseVersion(declared)
	if err != nil {
		return fmt.Errorf("skill_engine_version %q: not semver", declared)
	}
	have, _ := parseVersion(EngineVersion)
	// A foreign major is refused in BOTH directions, not just "from the
	// future". A major is a major precisely because older descriptions read by
	// different rules: a 1.x skill with an asset `lang` field parses under a
	// 2.x engine without a single complaint — the field simply disappears,
	// because the struct no longer has it. That silent loss is the very
	// failure versioning exists to prevent.
	if want.major != have.major {
		return fmt.Errorf("skill targets format %s, engine is %s: different major, migration needed", declared, EngineVersion)
	}
	if want.compare(have) > 0 {
		return fmt.Errorf("skill requires format version %s, engine supports %s", declared, EngineVersion)
	}
	return nil
}

// CompareSkillVersions compares versions of ONE skill: >0 when a is newer.
//
// Needed where a content hash is compared today: a hash answers "did it
// change" but not "which is newer", so it supports neither a deliberate
// rollback nor resolving a divergence.
//
// An empty version counts as the oldest: a skill that did not declare one must
// not overwrite a skill that did.
func CompareSkillVersions(a, b string) (int, error) {
	va, err := parseSkillVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSkillVersion(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

func parseSkillVersion(v string) (version, error) {
	if v == "" {
		return version{}, nil
	}
	p, err := parseVersion(v)
	if err != nil {
		return version{}, fmt.Errorf("skill_version %q: not semver", v)
	}
	return p, nil
}

// version — major.minor.patch. Hand-rolled instead of a semver library: the
// engine is embedded into someone else's application, and every dependency
// here becomes a dependency of that application — with its versions and its
// conflicts. All the format needs from semver is parsing and comparing three
// numbers; prerelease suffixes are of no use to it.
type version struct{ major, minor, patch int }

func parseVersion(s string) (version, error) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 4)
	if len(parts) != 3 {
		return version{}, fmt.Errorf("want three parts major.minor.patch")
	}
	var v version
	for i, dst := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return version{}, fmt.Errorf("part %q is not a number", parts[i])
		}
		*dst = n
	}
	return v, nil
}

// compare: >0 when the receiver is newer than the argument.
func (v version) compare(o version) int {
	for _, p := range [][2]int{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}} {
		if p[0] != p[1] {
			if p[0] > p[1] {
				return 1
			}
			return -1
		}
	}
	return 0
}
