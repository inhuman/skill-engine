package skillengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Composite-скилл, выписанный в формате, ДЕЙСТВИТЕЛЬНО исполняется движком.
//
// Проверка не «схема принимает», а «конструкции работают вместе»: делегирование,
// параллельные ветки и условная применимость в одном потоке — тот класс,
// который ревью назвал невыразимым.
func TestCompositeSkillExampleRuns(t *testing.T) {
	path := filepath.Join("..", "..", "specs", "skill-programs", "examples", "06-triage.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("пример недоступен: %v", err)
	}
	var file struct {
		Workflow Flow `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))
	require.NoError(t, file.Workflow.Validate(), "описание примера валидно для движка")

	// Модель отвечает так, чтобы ход прошёл по ветке runtime: тикет прочитан,
	// баг рантаймовый — значит собираются и логи, и метрики.
	r := &scriptedRunner{answers: map[string]string{
		"extract_key":    "PROJ-1",
		"classify_bug":   "runtime",
		"extract_target": "state-service",
		"synthesize":     "Гипотеза: падает на старте из-за пустого конфига.",
	}}
	d := &recordingDelegate{out: map[string]string{
		"file-ticket":         "симптом: 500 на /orders",
		"gitlab-find-code":    "state.go:120",
		"fetch-logs":            "panic: nil map",
		"prom-service-health": "rate ошибок 12%",
	}}

	vars, outcome, err := ExecuteWith(context.Background(), &file.Workflow,
		Deps{Runner: r, Delegate: d}, map[string]string{"input": "разберись с PROJ-1"})
	require.NoError(t, err)

	assert.Contains(t, vars["answer"], "Гипотеза", "финальный ответ собран")
	assert.Len(t, d.calls, 4, "делегировано четырём скиллам: тикет + код + логи + метрики")
	assert.Empty(t, outcome.Skipped, "при рантайм-баге ни одна ветка не пропущена")
}

// Тот же скилл на КОД-баге: ветки логов и метрик пропускаются по when, и это
// видно — guardrail «не тяни логи на чистом код-баге» перестаёт быть просьбой.
func TestCompositeSkillSkipsRuntimeEvidenceOnCodeBug(t *testing.T) {
	path := filepath.Join("..", "..", "specs", "skill-programs", "examples", "06-triage.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("пример недоступен: %v", err)
	}
	var file struct {
		Workflow Flow `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))

	r := &scriptedRunner{answers: map[string]string{
		"extract_key":    "PROJ-2",
		"classify_bug":   "code",
		"extract_target": "billing",
		"synthesize":     "Гипотеза: логическая ошибка в расчёте.",
	}}
	d := &recordingDelegate{out: map[string]string{
		"file-ticket":      "симптом: неверная сумма",
		"gitlab-find-code": "calc.go:44",
	}}

	_, outcome, err := ExecuteWith(context.Background(), &file.Workflow,
		Deps{Runner: r, Delegate: d}, map[string]string{"input": "разберись с PROJ-2"})
	require.NoError(t, err)

	assert.Len(t, d.calls, 2, "логи и метрики НЕ запрашивались")
	assert.ElementsMatch(t, []string{"fetch_logs", "fetch_metrics"}, outcome.Skipped,
		"пропуск виден — иначе частичное применение скилла снова незаметно")
}

// scriptedRunner отвечает по имени шага.
type scriptedRunner struct {
	answers map[string]string
	seen    []StepRequest
}

func (s *scriptedRunner) Run(_ context.Context, req StepRequest) (Result, error) {
	s.seen = append(s.seen, req)
	for name, ans := range s.answers {
		if strings.EqualFold(req.Name, name) {
			return Result{Text: ans}, nil
		}
	}
	return Result{Text: ""}, nil
}
