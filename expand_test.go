package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Доступ к полю структурного результата: шаг извлекает несколько значений
// ОДНИМ вызовом вместо трёх — иначе смысл структурного ответа теряется.
func TestExpandStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: msg, value: "кластер {{target.cluster}}, ns {{target.namespace}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"target": `{"cluster":"staging","namespace":"backend","replicas":3}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "кластер staging, ns backend", vars["msg"])
}

// Не-строковое поле подставляется как JSON: число остаётся числом на вид.
func TestExpandStructuredNonStringField(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "реплик: {{t.replicas}}"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"t": `{"replicas":3}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "реплик: 3", vars["out"])
}

// Отсутствующее поле — пустая строка, как и отсутствующая переменная: маркер,
// доехавший до модели, читался бы как часть инструкции.
func TestExpandMissingFieldIsEmpty(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "[{{t.nope}}][{{missing.field}}]"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"t": `{"a":1}`})
	require.NoError(t, err)
	assert.Equal(t, "[][]", vars["out"])
}

// Переменная с точкой в ИМЕНИ выигрывает у разбора «объект.поле»: явное имя
// сильнее догадки.
func TestExpandPrefersLiteralName(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "{{a.b}}"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"a.b": "буквальное",
		"a":   `{"b":"из объекта"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "буквальное", vars["out"])
}

// Не-JSON в переменной не ломает подстановку.
func TestExpandFieldOfNonJSON(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "[{{t.field}}]"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"t": "просто текст"})
	require.NoError(t, err)
	assert.Equal(t, "[]", vars["out"])
}

// Условие по ПОЛЮ структурного результата.
//
// Живой случай: switch научили читать поле, а условие — забыли. Замер
// показал ДВА вызова вместо трёх и ноль обращений в репозиторий — то есть
// «стало дешевле» вместо «сломалось»: ветка «ресурс не назван» выбиралась
// всегда, потому что `req.resource is not empty` молча ложно.
func TestConditionOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "req.resource is not empty"
      then:
        - set: {var: out, value: "есть {{req.resource}}"}
      else:
        - set: {var: out, value: "нет"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"resource":"t1_kaas","owner":"t1"}`})
	require.NoError(t, err)
	assert.Equal(t, "есть t1_kaas", vars["out"])

	vars, _, err = ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"owner":"t1"}`})
	require.NoError(t, err)
	assert.Equal(t, "нет", vars["out"], "поля нет — условие ложно")
}

func TestSwitchOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - switch:
      var: req.owner
      cases:
        t1: [{set: {var: out, value: "наш"}}]
        foreign: [{set: {var: out, value: "чужой"}}]
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"owner":"foreign"}`})
	require.NoError(t, err)
	assert.Equal(t, "чужой", vars["out"])
}

// Сравнение по полю — та же дорога.
func TestEqualityOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "req.genre == write"
      then: [{set: {var: out, value: "пишем конфиг"}}]
      else: [{set: {var: out, value: "разбираем"}}]
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"genre":"write"}`})
	require.NoError(t, err)
	assert.Equal(t, "пишем конфиг", vars["out"])
}

// fakeMemory — рабочая память хода в тестах.
type fakeMemory map[string]string

func (m fakeMemory) Get(id string) (string, bool) { v, ok := m[id]; return v, ok }

// Хост дописывает хендл к ЛЮБОМУ результату инструмента, и переменная перестаёт
// быть валидным JSON. Без снятия пометки `{{var.field}}` не работал НИ РАЗУ ни
// на одном результате `call:` — поле молча пустело.
func TestFieldLookupIgnoresHostMemNote(t *testing.T) {
	s := &state{vars: map[string]string{
		"ctx": `{"head_sha":"abc123","delta_scope":"go"}` + "\n[mem:res-1]",
	}}
	assert.Equal(t, "abc123", s.lookup("ctx.head_sha"))
	assert.Equal(t, "go", s.lookup("ctx.delta_scope"))
}

// У обрезанного превью JSON неполон и не разберётся никогда. Целое лежит в
// рабочей памяти под тем же хендлом — оттуда и берём.
//
// Живой случай: скилл ревью, префетч контекста MR больше порога превью,
// batteries получили пустые DELTA/SCOPE/CHECKOUT, mcpx отверг вызов, judge
// остался без вывода инструментов, ревью не состоялось.
func TestFieldLookupFallsBackToWorkingMemory(t *testing.T) {
	full := `{"head_sha":"deadbeef","delta_regex":"^(a|b)$","delta_scope":"go"}`
	s := &state{
		vars:   map[string]string{"ctx": `{"head_sha":"dead` + "…\n[mem:res-9 — это ПРЕВЬЮ, всего 42kb]"},
		memory: fakeMemory{"res-9": full},
	}
	assert.Equal(t, "deadbeef", s.lookup("ctx.head_sha"))
	assert.Equal(t, "^(a|b)$", s.lookup("ctx.delta_regex"))
}

// Памяти нет — поле пустое, но хода это не роняет.
func TestFieldLookupWithoutMemoryStaysEmpty(t *testing.T) {
	s := &state{vars: map[string]string{"ctx": `{"head_sha":"dead` + "…\n[mem:res-9]"}}
	assert.Equal(t, "", s.lookup("ctx.head_sha"))
}

// В АРГУМЕНТЫ значение едет целиком и без пометки хоста: там его читает скрипт,
// а не модель. Хендл «[mem:id]» полезен модели и ломает разбор на том конце.
//
// Живой случай: render получал findings с хвостом и отвечал
// «RENDER_ERROR: stdin не парсится как поток JSON».
func TestArgsGetCleanFullValue(t *testing.T) {
	s := &state{
		vars:   map[string]string{"findings": `{"a":1` + "…\n[mem:res-3 — это ПРЕВЬЮ, всего 42kb]"},
		memory: fakeMemory{"res-3": `{"a":1,"b":2}`},
	}
	args := s.expandArgs(map[string]any{"stdin": "{{findings}}"})
	assert.Equal(t, `{"a":1,"b":2}`, args["stdin"], "целиком из памяти, без пометки")
}

// В ИНСТРУКЦИИ пометка остаётся: по ней модель понимает, что данные обрезаны и
// как дочитать остальное.
func TestInstructionKeepsMemHandle(t *testing.T) {
	s := &state{vars: map[string]string{"ctx": "данные\n[mem:res-9]"}}
	assert.Contains(t, s.expand("вот {{ctx}}"), "[mem:res-9]")
}
