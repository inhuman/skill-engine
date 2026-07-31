package skillengine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

// readWorkflow достаёт описание хода из файла примера.
//
// Узел берётся ПО ЗНАЧЕНИЮ, не указателем: yaml.v3 не заполняет *yaml.Node при
// разборе в структуру — поле остаётся ненулевым, но пустым, и описание молча
// теряется.
func readWorkflow(t *testing.T, path string) (se.Flow, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc struct {
		Workflow yaml.Node `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.Workflow.Kind == 0 {
		return se.Flow{}, false // словарь значений, а не скилл
	}
	var f se.Flow
	require.NoError(t, doc.Workflow.Decode(&f), "описание не разбирается")
	return f, true
}

// Примеры — часть контракта: по ним читают формат. Пример, переставший
// разбираться движком, хуже отсутствующего — он учит неверному.
func TestExamplesParseAndValidate(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "примеры пропали")

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, ok := readWorkflow(t, path)
			if !ok {
				return
			}
			require.NoError(t, f.Validate(), "описание не проходит валидацию")
			assert.NotEmpty(t, f.Steps)
		})
	}
}

// Пример обязан РАБОТАТЬ, а не только разбираться: шаги ходят к исполнителям,
// сервер вычисляется, ответ доезжает.
func TestExamplePodsRuns(t *testing.T) {
	f, ok := readWorkflow(t, filepath.Join("examples", "pods.yaml"))
	require.True(t, ok)

	answers := map[string]string{
		"understand": `{"cluster":"staging","namespace":"backend"}`,
		"report":     "api-7f9 → Running",
	}
	var asked [][]string
	runner := se.RunnerFunc(func(_ context.Context, req se.StepRequest) (se.Result, error) {
		asked = append(asked, req.Tools)
		return se.Result{Text: answers[req.Name]}, nil
	})
	var calledOn string
	caller := se.ToolCallerFunc(func(_ context.Context, server, _ string, _ map[string]any) (string, error) {
		calledOn = server
		return "api-7f9 Running", nil
	})

	vars, _, err := se.ExecuteWith(t.Context(), &f, se.Deps{Runner: runner, Caller: caller},
		map[string]string{"input": "покажи поды"})
	require.NoError(t, err)

	assert.Equal(t, "staging", calledOn, "сервер вычислен из разбора запроса")
	assert.Equal(t, "api-7f9 → Running", vars[se.AnswerVar], "ответ хода — из последнего шага")
	for _, tools := range asked {
		assert.Empty(t, tools, "шагам разбора и ответа инструменты не выданы")
	}
}
