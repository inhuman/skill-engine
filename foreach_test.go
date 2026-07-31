package skillengine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// for_each по списку строк — наблюдаемый случай «список, добытый инструментом».
func TestForEachOverLines(t *testing.T) {
	c := &recordingCaller{out: "коммит найден"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: per_service
    for_each:
      in: services
      as: service
      collect: findings
      steps:
        - call:
            tool: srv:find_commit
            args: {service: "{{service}}"}
            save_as: findings
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c},
		map[string]string{"services": "billing\norders\n\nstate"})
	require.NoError(t, err)
	assert.Equal(t, 3, c.calls, "пустая строка не считается элементом")
	assert.Equal(t, "коммит найден\n\nкоммит найден\n\nкоммит найден", vars["findings"])
}

// JSON-массив (структурный результат шага) итерируется поэлементно.
func TestForEachOverJSONArray(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      steps:
        - set: {var: acc, value: "элемент {{it}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": `["a","b"]`})
	require.NoError(t, err)
	assert.Equal(t, "элемент a\n\nэлемент b", vars["acc"])
}

// Потолок обязателен по смыслу: цикл по коллекции неизвестной длины — прямой
// путь к runaway. Частичная обработка ГОВОРИТСЯ, а не проглатывается.
func TestForEachRespectsLimitAndSaysSo(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      max_iterations: 2
      steps:
        - set: {var: acc, value: "{{it}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": "1\n2\n3\n4\n5"})
	require.NoError(t, err)
	assert.Contains(t, vars["acc"], "обработано 2 из 5")
}

// Умолчание потолка применяется, даже когда описание его не задало.
func TestForEachDefaultLimit(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      steps:
        - set: {var: acc, value: "{{it}}"}
`)
	many := make([]string, 25)
	for i := range many {
		many[i] = "x"
	}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": strings.Join(many, "\n")})
	require.NoError(t, err)
	assert.Contains(t, vars["acc"], "обработано 10 из 25", "потолок по умолчанию")
}

func TestForEachEmptyCollection(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each: {in: items, as: it, steps: [{set: {var: a, value: "{{it}}"}}]}
  - set: {var: after, value: "поток продолжился"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "поток продолжился", vars["after"])
}

// on_error цикла обязано РАБОТАТЬ, а не только парситься: описание просит
// пометить элемент и идти дальше, и до этой правки цикл вместо этого падал на
// первом же отказе, теряя остальные итерации.
func TestForEachOnErrorContinue(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: loop
    for_each:
      in: items
      as: it
      collect: out
      on_error: continue
      max_iterations: 5
      steps:
        - name: work
          instruction: обработай
          tools: []
          save_as: out
`), &f))

	// Вторая итерация отказывает: одна неудача не должна ронять остальные.
	r := &nthFailRunner{failAt: 2, text: "готово"}

	vars, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: r},
		map[string]string{"items": "a\nb\nc"})
	require.NoError(t, err, "цикл упал на отказавшей итерации")
	assert.Equal(t, 3, r.calls, "цикл прервался вместо продолжения")

	var loop *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "for_each" {
			loop = &outcome.Steps[i]
		}
	}
	require.NotNil(t, loop, "цикл не оставил следа")
	assert.Equal(t, "degraded", loop.Outcome)
	assert.Contains(t, loop.Reason, "отказали: 1")
	assert.NotEmpty(t, vars["out"])
}

// nthFailRunner отказывает на N-й итерации и работает на остальных.
type nthFailRunner struct {
	failAt, calls int
	text          string
}

func (r *nthFailRunner) Run(context.Context, StepRequest) (Result, error) {
	r.calls++
	if r.calls == r.failAt {
		return Result{}, errors.New("элемент не найден")
	}
	return Result{Text: r.text}, nil
}

// Цикл обязан идти по ПОЛНОЙ коллекции, а не по превью.
//
// Крупный результат `call:` приезжает в переменную обрезанным (4096 символов) с
// хендлом рабочей памяти. Цикл по такому значению обошёл бы часть списка и
// отдал результат, который выглядит полным — это хуже, чем честный потолок,
// потому что молчит. Нашлось при переводе скилл на поштучный обзор
// кусков диффа .
func TestForEachReadsFullCollectionFromMemory(t *testing.T) {
	full := "часть-1\nчасть-2\nчасть-3\nчасть-4"
	r := &fakeRunner{answer: map[string]string{"look": "ок"}}
	f := parseFlow(t, `
steps:
  - for_each:
      in: parts
      as: p
      max_iterations: 10
      steps:
        - name: look
          instruction: "смотрю {{p}}"
`)
	_, _, err := ExecuteWith(context.Background(), f,
		Deps{Runner: r, Memory: fakeMemory{"res-1": full}},
		map[string]string{"parts": "часть-1\n[mem:res-1]"})
	require.NoError(t, err)
	require.Len(t, r.seen, 4, "обошли все четыре части, а не одну из превью")
}

// `in` — имя переменной, не шаблон. Написанное шаблоном молчит: переменной с
// именем «{{parts}}» нет, цикл делает ноль итераций и отчитывается «ok» —
// пустой обход неотличим от успешного. Отказ на разборе дешевле.
func TestForEachInRejectsTemplate(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - for_each:
      in: "{{parts}}"
      as: p
      steps:
        - name: look
          instruction: "смотрю {{p}}"
`), &f))
	err := f.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "ИМЯ переменной")
}

// `in` умеет поле, а не только целую переменную: результат инструмента — это
// конверт, и обходить нужно его содержимое, а не сам конверт.
func TestForEachInAcceptsField(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"look": "ок"}}
	f := parseFlow(t, `
steps:
  - for_each:
      in: res.stdout
      as: p
      max_iterations: 10
      steps:
        - name: look
          instruction: "смотрю {{p}}"
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r},
		map[string]string{"res": `{"exit_code":0,"stdout":"часть-1\nчасть-2\nчасть-3"}`})
	require.NoError(t, err)
	require.Len(t, r.seen, 3, "обошли строки stdout, а не конверт целиком")
}
