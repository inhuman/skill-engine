package skillengine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeRunner записывает, что именно просили у исполнителя шага, и отдаёт
// заранее заданные ответы. Ошибки — по имени шага.
type fakeRunner struct {
	seen   []StepRequest
	answer map[string]string
	fail   map[string]error
	// res — готовый результат для тестов, которым важны счётчики вызовов,
	// а не текст по имени шага.
	res *Result
}

func (f *fakeRunner) Run(_ context.Context, req StepRequest) (Result, error) {
	f.seen = append(f.seen, req)
	if err, ok := f.fail[req.Name]; ok {
		return Result{}, err
	}
	if f.res != nil {
		return *f.res, nil
	}
	return Result{Text: f.answer[req.Name]}, nil
}

func run(t *testing.T, src string, r *fakeRunner) (map[string]string, error) {
	t.Helper()
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(src), &f))
	v, _, err := ExecuteWith(t.Context(), &f, Deps{Runner: r}, nil)
	return v, err
}

// Главное, ради чего пакет существует: набор инструментов задаётся ШАГОМ, а не
// просьбой в тексте. Пустой список — не «не задано», а «инструментов нет вовсе».
func TestToolsPerStep(t *testing.T) {
	src := `
tools: ["search", "read", "tree"]
steps:
  - name: classify
    instruction: "определи тип"
    tools: []
    save_as: kind
  - name: lookup
    instruction: "найди схему"
    tools: ["search"]
    save_as: hit
  - name: answer
    instruction: "ответь по {{hit}}"
`
	r := &fakeRunner{answer: map[string]string{"classify": "internal", "lookup": "строка из репы"}}
	_, err := run(t, src, r)
	require.NoError(t, err)
	require.Len(t, r.seen, 3)

	assert.Equal(t, []string{}, r.seen[0].Tools, "шаг классификации обязан идти БЕЗ инструментов")
	assert.Equal(t, []string{"search"}, r.seen[1].Tools, "шаг сузил набор до одного")
	assert.Equal(t, []string{"search", "read", "tree"}, r.seen[2].Tools, "шаг не задал — берётся набор потока")
}

// Шаг может только СУЗИТЬ набор потока. Иначе ограничение потока ничего не
// значит: шаг вернул бы себе инструмент, убранный осознанно.
func TestStepCannotWidenTools(t *testing.T) {
	src := `
tools: ["read"]
steps:
  - name: s
    instruction: "x"
    tools: ["read", "write", "delete"]
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, []string{"read"}, r.seen[0].Tools, "write/delete отброшены — их нет в потоке")
}

func TestVarsAndBranching(t *testing.T) {
	src := `
steps:
  - name: detect
    instruction: "чей ресурс"
    save_as: owner
  - set:
      var: doc
      value: "docs/{{owner}}.md"
  - switch:
      var: owner
      cases:
        internal:
          - name: internal_path
            instruction: "читай {{doc}}"
            save_as: out
        foreign:
          - name: foreign_path
            instruction: "отвечай из знаний"
            save_as: out
      default:
        - name: unknown_path
          instruction: "переспроси"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"detect": "internal", "internal_path": "готово"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "docs/internal.md", vars["doc"])
	assert.Equal(t, "готово", vars["out"])
	require.Len(t, r.seen, 2)
	assert.Equal(t, "читай docs/internal.md", r.seen[1].Instruction, "подстановка дошла до инструкции")
}

func TestSwitchDefault(t *testing.T) {
	src := `
steps:
  - name: detect
    instruction: "?"
    save_as: kind
  - switch:
      var: kind
      cases:
        a:
          - name: branch_a
            instruction: "a"
      default:
        - name: fallback
          instruction: "не разобрал"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"detect": "нечто", "fallback": "переспросил"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "переспросил", vars["out"])
}

// Каскад фолбэков: пока предыдущий шаг не дал результата — пробуем следующий
// способ. Так в живых скиллах устроен поиск схемы: поиск → файл по пути → дерево.
func TestIfCascade(t *testing.T) {
	src := `
steps:
  - name: search
    instruction: "поиск"
    save_as: hit
  - if:
      cond: "hit == MISS"
      then:
        - name: by_path
          instruction: "по пути"
          save_as: hit
  - if:
      cond: "hit == MISS"
      then:
        - name: by_tree
          instruction: "по дереву"
          save_as: hit
`
	r := &fakeRunner{answer: map[string]string{"search": "MISS", "by_path": "MISS", "by_tree": "нашлось"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "нашлось", vars["hit"])
	assert.Len(t, r.seen, 3, "прошли весь каскад")
}

// Пустое значение в условии проверяет незаполненность: «ключ не извлёкся».
func TestCondEmptyValue(t *testing.T) {
	src := `
steps:
  - name: extract
    instruction: "извлеки ключ"
    save_as: key
  - if:
      cond: "key == "
      then:
        - name: ask
          instruction: "нужен ключ"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"extract": "", "ask": "спросил"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "спросил", vars["out"])
}

// Отказ по правам — самый частый класс отказа в живых скиллах, и реакция на
// него всегда одна: сказать честно и продолжить с тем, что есть. Права от
// повтора не появятся, обходные пути искать нечего.
func TestOnErrorContinueOnDenied(t *testing.T) {
	src := `
steps:
  - name: read_attachment
    instruction: "прочитай вложение"
    save_as: att
    on_error: continue
  - name: answer
    instruction: "ответь с учётом {{att}}"
    save_as: out
`
	r := &fakeRunner{
		fail:   map[string]error{"read_attachment": fmt.Errorf("%w: нет прав на инструмент", ErrDenied)},
		answer: map[string]string{"answer": "ответил без вложения"},
	}
	vars, err := run(t, src, r)
	require.NoError(t, err, "отказ по правам не должен ронять поток")
	assert.Contains(t, vars["att"], "DENIED")
	assert.Equal(t, "ответил без вложения", vars["out"])
	assert.Contains(t, r.seen[1].Instruction, "DENIED", "следующий шаг ВИДИТ, что доступа не было")
}

func TestOnErrorAbortIsDefault(t *testing.T) {
	src := `
steps:
  - name: must_work
    instruction: "обязательный шаг"
  - name: never
    instruction: "не должен выполниться"
`
	r := &fakeRunner{fail: map[string]error{"must_work": errors.New("сломалось")}}
	_, err := run(t, src, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must_work")
	assert.Len(t, r.seen, 1, "поток прерван, второй шаг не запускался")
}

func TestOnErrorSkipStopsBranchOnly(t *testing.T) {
	src := `
steps:
  - if:
      cond: "x == "
      then:
        - name: optional
          instruction: "необязательный"
          save_as: o
          on_error: skip
        - name: after_in_branch
          instruction: "внутри ветки после отказа"
  - name: after_branch
    instruction: "снаружи ветки"
    save_as: out
`
	r := &fakeRunner{
		fail:   map[string]error{"optional": errors.New("не вышло")},
		answer: map[string]string{"after_branch": "дошли"},
	}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "дошли", vars["out"], "поток продолжился после прерванной ветки")
	for _, s := range r.seen {
		assert.NotEqual(t, "after_in_branch", s.Name, "остаток ветки пропущен")
	}
}

func TestLimitsReachRunner(t *testing.T) {
	src := `
steps:
  - name: flaky_search
    instruction: "поиск"
    max_calls: 1
  - name: tree_walk
    instruction: "дерево"
    max_calls: 8
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, 1, r.seen[0].MaxCalls)
	assert.Equal(t, 8, r.seen[1].MaxCalls)
}

// Неизвестная переменная превращается в пустую строку. Оставленный маркер
// {{var}} доехал бы до модели и читался ею как часть инструкции.
func TestUnknownVarBecomesEmpty(t *testing.T) {
	src := `
steps:
  - name: s
    instruction: "работай с [{{nothing}}]"
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "работай с []", r.seen[0].Instruction)
}

func TestValidate(t *testing.T) {
	bad := map[string]string{
		"пустой поток":         `steps: []`,
		"шаг без действия":     "steps:\n  - name: x",
		"switch без var":       "steps:\n  - switch:\n      cases: {}",
		"кривое условие":       "steps:\n  - if:\n      cond: \"a > b\"\n      then: []",
		"неизвестная политика": "steps:\n  - instruction: x\n    on_error: retry",
		"отрицательный лимит":  "steps:\n  - instruction: x\n    max_calls: -1",
	}
	for name, src := range bad {
		t.Run(name, func(t *testing.T) {
			var f Flow
			require.NoError(t, yaml.Unmarshal([]byte(src), &f))
			assert.Error(t, f.Validate())
		})
	}

	t.Run("валидный проходит", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte("steps:\n  - instruction: x\n    on_error: continue"), &f))
		assert.NoError(t, f.Validate())
	})
}

// Шаг-классификатор существует ради ветвления, а модель отвечает связным текстом.
// Живой случай: на «ответь одним словом: t1 или foreign» пришло
// «Итог: Определил тип ресурса по префиксу. Результат: foreign» — верно по
// смыслу, но switch сравнивает точно, и ветка не выбралась.
func TestOneOfNormalizesVerboseAnswer(t *testing.T) {
	src := `
steps:
  - name: classify
    instruction: "определи тип"
    one_of: ["t1", "foreign"]
    save_as: owner
  - switch:
      var: owner
      cases:
        foreign:
          - name: public_answer
            instruction: "ответь из знаний"
            save_as: out
        t1:
          - name: internal_answer
            instruction: "сверься со схемой"
            save_as: out
      default:
        - name: ask
          instruction: "переспроси"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{
		"classify":      "Итог: Определил тип ресурса по префиксу. Результат: foreign",
		"public_answer": "ответил",
	}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "foreign", vars["owner"], "ответ сведён к допустимому значению")
	assert.Equal(t, "ответил", vars["out"], "ветка выбрана верно")
}

func TestNormalizeOneOf(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	// Перечисление вариантов перед выбором: берётся ПОСЛЕДНЕЕ вхождение.
	assert.Equal(t, "foreign",
		normalizeOneOf("нужно определить t1 или foreign; здесь foreign", allowed))
	assert.Equal(t, "t1", normalizeOneOf("t1", allowed))
	assert.Equal(t, "foreign", normalizeOneOf("FOREIGN", allowed), "регистр не важен")
	assert.Empty(t, normalizeOneOf("не смог определить", allowed), "ничего не найдено — пусто")
	assert.Equal(t, "как есть", normalizeOneOf("как есть", nil), "без one_of текст не трогаем")
}

// Шаг без save_as — финальный ответ, а не выброшенная работа: его текст обязан
// доехать до вызывающего. Прежде он не сохранялся никуда, и ход отдавал самую
// длинную служебную переменную — разбор первого шага вместо ответа.
func TestStepWithoutSaveAsFillsAnswer(t *testing.T) {
	out, err := run(t, `
steps:
  - name: parse
    instruction: разбери
    tools: []
    save_as: req
  - name: reply
    instruction: ответь
    tools: []
`, &fakeRunner{answer: map[string]string{"parse": "служебное значение подлиннее", "reply": "вот ответ"}})
	require.NoError(t, err)
	assert.Equal(t, "вот ответ", out[AnswerVar])
}

// Ни одна ветка не подошла, а default пуст — это провал ветвления, а не «нечего
// делать»: работа шага не сделана. В следе такой шаг обязан быть отмечен
// деградацией, иначе тихий отказ выглядит успехом.
func TestSwitchWithoutMatchAndEmptyDefaultIsLoud(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - set: {var: verdict, value: ""}
  - switch:
      var: verdict
      cases:
        found:
          - name: a
            instruction: x
            tools: []
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: &fakeRunner{}}, nil)
	require.NoError(t, err)

	var sw *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "switch" {
			sw = &outcome.Steps[i]
		}
	}
	require.NotNil(t, sw, "шага switch нет в следе")
	assert.Equal(t, "degraded", sw.Outcome, "тихий отказ ветвления")
	assert.Contains(t, sw.Reason, "ни одна ветка не подошла")
}

// Шаг, не давший ни слова, — отказ, а не успех: его работа не сделана. Класс
// живой — gpt-oss кладёт ответ в reasoning_content, оставляя content пустым.
func TestStepWithoutTextIsDegraded(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: mute
    instruction: ответь
    tools: []
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: &fakeRunner{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	assert.Equal(t, "degraded", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "не дал текста")
}

// Делегирование — спавн субагента, самая дорогая операция хода. Без следа оно
// невидимо и в agent_events, и в прогресс-посте: человек смотрит на «думаю…»,
// пока минуту работает другой скилл.
func TestDelegateIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: ask_other
    delegate:
      skill: jira-ticket
      task: разбери тикет
      save_as: out
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("готово")}, nil)
	require.NoError(t, err)

	require.Len(t, outcome.Steps, 1, "делегирование не оставило следа")
	assert.Equal(t, "delegate", outcome.Steps[0].Kind)
	assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "jira-ticket", "в следе не видно, какому скиллу отдали работу")
}

// Отказ делегата тоже обязан быть в следе: под политикой continue он иначе
// неотличим от успеха, а работа не сделана.
func TestDelegateFailureIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: ask_other
    delegate:
      skill: jira-ticket
      task: разбери тикет
      save_as: out
      on_error: continue
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: failingDelegate{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	assert.NotEqual(t, "ok", outcome.Steps[0].Outcome)
}

// Развилка отмечает границу: сколько веток пошло и сколько отказало. Иначе
// отказавшая под continue ветка неотличима от не запускавшейся.
func TestParallelIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      on_error: continue
      branches:
        - - name: a
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("готово")}, nil)
	require.NoError(t, err)

	var par *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "parallel" {
			par = &outcome.Steps[i]
		}
	}
	require.NotNil(t, par, "развилка не оставила следа")
	assert.Contains(t, par.Reason, "веток: 2")
}

type constDelegate string

func (c constDelegate) Delegate(context.Context, string, string) (string, error) {
	return string(c), nil
}

type failingDelegate struct{}

func (failingDelegate) Delegate(context.Context, string, string) (string, error) {
	return "", errors.New("субагент упал")
}

// Развилка, где по условию пропущены ВСЕ ветки, ничего не собрала. Следующий
// шаг всё равно сформулирует ответ, и он будет выглядеть законченным — поэтому
// такая развилка обязана быть отмечена деградацией.
func TestParallelAllBranchesSkippedIsDegraded(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      branches:
        - - name: a
            when: pick.wiki == true
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            when: pick.kg == true
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("данные")},
		map[string]string{"pick": `{"wiki":false,"kg":false}`})
	require.NoError(t, err)

	var par *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "parallel" {
			par = &outcome.Steps[i]
		}
	}
	require.NotNil(t, par)
	assert.Equal(t, "degraded", par.Outcome, "все ветки пропущены, а развилка числится успешной")
	assert.Contains(t, par.Reason, "ни одна")
}

// Хоть одна ветка пошла — развилка успешна: частичный сбор это нормальный
// результат, а не отказ.
func TestParallelPartialSelectionIsOK(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      branches:
        - - name: a
            when: pick.wiki == true
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            when: pick.kg == true
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("данные")},
		map[string]string{"pick": `{"wiki":true,"kg":false}`})
	require.NoError(t, err)

	for _, s := range outcome.Steps {
		if s.Kind == "parallel" {
			assert.Equal(t, "ok", s.Outcome, "частичный сбор — не отказ")
		}
	}
}

// Пропущенные ветки доезжают до шага, формулирующего ответ. Без этого он видит
// только собранное и выдаёт «источник ответил пусто» там, где в источник не
// ходили вовсе — то есть непроверенное за проверенное.
func TestParallelExposesSkippedBranches(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: findings
      branches:
        - - name: probe_wiki
            when: pick.wiki == true
            delegate: {skill: скилл поиска по вики, task: t, save_as: from_wiki}
        - - name: probe_jira
            when: pick.jira == true
            delegate: {skill: jira-ticket, task: t, save_as: from_jira}
`), &f))

	vars, _, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("данные вики")},
		map[string]string{"pick": `{"wiki":true,"jira":false}`})
	require.NoError(t, err)

	assert.Contains(t, vars["findings"], "данные вики")
	assert.Contains(t, vars["findings"+SkippedSuffix], "probe_jira",
		"пропущенная ветка не видна следующему шагу")
	assert.NotContains(t, vars["findings"+SkippedSuffix], "probe_wiki")
}

// Шаг, у которого ОТКАЗАЛИ все вызовы, обязан быть degraded, а не ok.
//
// Живой случай: шаг ревью сделал 7 вызовов, сервер отверг все
// («Either mergeRequestIid or branchName must be provided»), шаг упёрся в
// потолок и отдал пустоту — а событие писало outcome=ok, calls=7. По событиям
// ход был неотличим от успешного, и разбираться пришлось в логах пода.
func TestAllCallsFailedIsDegraded(t *testing.T) {
	r := &fakeRunner{res: &Result{Text: "что-то модель всё же сказала", Calls: 7, CallsFailed: 7}}
	f := parseFlow(t, `
tools: ["gitlab-dev"]
steps:
  - name: judge
    instruction: "разбери MR"
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	tr := outcome.Steps[0]
	assert.Equal(t, "degraded", tr.Outcome)
	assert.Equal(t, 7, tr.CallsFailed)
	assert.Contains(t, tr.Reason, "все вызовы инструментов отказали")
}

// Частичный отказ — это НЕ провал шага: полезный результат получен.
func TestSomeCallsFailedStaysOK(t *testing.T) {
	r := &fakeRunner{res: &Result{Text: "разбор готов", Calls: 5, CallsFailed: 2}}
	f := parseFlow(t, `
tools: ["gitlab-dev"]
steps:
  - name: judge
    instruction: "разбери MR"
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	assert.Equal(t, 2, outcome.Steps[0].CallsFailed, "число отказов видно и на успешном шаге")
}

// Бюджет промахов из описания обязан доехать до исполнителя. Поле, объявленное
// в схеме и не переданное дальше, — украшение: скилл его пишет, а движок живёт
// по умолчанию, и расхождение молчит (ровно так `deliver` у ассета прожил до
// первого живого отказа).
func TestMaxToolErrorsReachesRunner(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"judge": "готово"}}
	_, err := run(t, `
tools: ["gitlab-dev"]
steps:
  - name: judge
    instruction: "разбери MR"
    max_calls: 6
    max_tool_errors: 4
    save_as: out
`, r)
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Equal(t, 6, r.seen[0].MaxCalls)
	assert.Equal(t, 4, r.seen[0].MaxToolErrors)
}
