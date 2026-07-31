package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Скилл, требующий формата новее движка, обязан получить ВНЯТНЫЙ отказ:
// молчаливое исполнение по старым правилам даёт правдоподобный неверный
// результат вместо ошибки.
func TestCheckEngineVersion(t *testing.T) {
	require.NoError(t, CheckEngineVersion("1.0.0"))
	require.NoError(t, CheckEngineVersion(""), "скилл без поля читается как legacy")
	require.NoError(t, CheckEngineVersion("0.9.0"), "старее движка — исполняем")

	err := CheckEngineVersion("1.1.0")
	require.Error(t, err, "минор новее движка: скилл пользуется полем, которого движок не знает")
	assert.Contains(t, err.Error(), "движок умеет")

	require.Error(t, CheckEngineVersion("2.0.0"))
	require.Error(t, CheckEngineVersion("не-версия"))
}

func TestCompareSkillVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.0.0", "1.9.9", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.2.3", "1.2.3", 0},
		{"1.0.0", "", 1}, // объявленная версия новее необъявленной
		{"", "", 0},
	} {
		got, err := CompareSkillVersions(c.a, c.b)
		require.NoError(t, err)
		assert.Equal(t, c.want, got, "%q против %q", c.a, c.b)
	}

	_, err := CompareSkillVersions("1.0", "плохо")
	assert.Error(t, err)
}
