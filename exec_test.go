package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Ответ хода записывает либо модель (шаг instruction), либо инструмент (шаг
// call), и вызывающий обязан их различать: черновик модели правомерно переписать
// «голосом», детерминированный отчёт — нет. скилл ревью печатает свой отчёт
// вместе со строкой метрик python-скриптом, и пересказ этого текста моделью
// уничтожил бы ровно те гарантии, ради которых рендер детерминированный.
func TestOutcomeReportsWhatWroteTheAnswer(t *testing.T) {
	t.Run("ответила модель", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: report
    instruction: ответь
    tools: []
`), &f))
		_, out, err := ExecuteWith(t.Context(), &f,
			Deps{Runner: &fakeRunner{answer: map[string]string{"report": "готово"}}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "instruction", out.AnsweredBy)
	})

	t.Run("ответил инструмент", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
tools: [srv]
steps:
  - name: render
    call:
      tool: srv:render
      save_as: answer
`), &f))
		_, out, err := ExecuteWith(t.Context(), &f,
			Deps{Caller: &recordingCaller{out: "## отчёт\n_метрики: published=10_"}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "call", out.AnsweredBy)
	})
}
