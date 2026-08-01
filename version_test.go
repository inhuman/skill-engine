package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A skill requiring a format newer than the engine must get an EXPLICIT
// refusal: silently running under the old rules produces a plausible wrong
// result instead of an error.
func TestCheckEngineVersion(t *testing.T) {
	require.NoError(t, CheckEngineVersion(EngineVersion), "its own version runs")
	require.NoError(t, CheckEngineVersion("2.0.0"))

	// A deliberately unreachable minor of ITS OWN major rather than "the next
	// one": otherwise the test breaks on every format bump and gets rewritten
	// without thinking.
	err := CheckEngineVersion("2.99.0")
	require.Error(t, err, "a minor newer than the engine: the skill uses a field the engine does not know")
	assert.Contains(t, err.Error(), "engine supports")

	// A foreign major is refused in BOTH directions. A 1.x skill would parse
	// under a 2.x engine without complaint, silently losing fields the structs
	// no longer have; an empty field is the same 1.x skill that simply did not
	// declare itself.
	for _, v := range []string{"1.0.0", "1.99.0", "0.9.0", "3.0.0", ""} {
		err := CheckEngineVersion(v)
		require.Error(t, err, "version %q is of a different major", v)
		assert.Contains(t, err.Error(), "major")
	}

	require.Error(t, CheckEngineVersion("not-a-version"))
}

func TestCompareSkillVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.0.0", "1.9.9", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.2.3", "1.2.3", 0},
		{"1.0.0", "", 1}, // a declared version is newer than an undeclared one
		{"", "", 0},
	} {
		got, err := CompareSkillVersions(c.a, c.b)
		require.NoError(t, err)
		assert.Equal(t, c.want, got, "%q against %q", c.a, c.b)
	}

	_, err := CompareSkillVersions("1.0", "bad")
	assert.Error(t, err)
}
