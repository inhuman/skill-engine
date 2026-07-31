package skillengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type recordingCaller struct {
	server, tool string
	args         map[string]any
	out          string
	err          error
	calls        int
}

func (c *recordingCaller) CallTool(_ context.Context, server, tool string, args map[string]any) (string, error) {
	c.calls++
	c.server, c.tool, c.args = server, tool, args
	return c.out, c.err
}

// seqCaller отдаёт заготовленные ответы по порядку и запоминает аргументы.
type seqCaller struct {
	outs []string
	seen []map[string]any
}

func (c *seqCaller) CallTool(_ context.Context, _, _ string, args map[string]any) (string, error) {
	c.seen = append(c.seen, args)
	if len(c.seen) <= len(c.outs) {
		return c.outs[len(c.seen)-1], nil
	}
	return "", nil
}

func parseFlow(t *testing.T, src string) *Flow {
	t.Helper()
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(src), &f))
	return &f
}

// Шаг call не тратит ни одной генерации: исполнителя шагов тут нет вовсе.
func TestCallStepNeedsNoModel(t *testing.T) {
	c := &recordingCaller{out: "строки схемы"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: lookup
    call:
      tool: srv:search_code
      args:
        project_id: "iac/provider"
        search: "{{resource}}"
      save_as: schema
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"resource": "t1_vpc_vip"})
	require.NoError(t, err)
	assert.Equal(t, "строки схемы", vars["schema"])
	assert.Equal(t, 1, c.calls)
	assert.Equal(t, "srv", c.server)
	assert.Equal(t, "search_code", c.tool)
	assert.Equal(t, "t1_vpc_vip", c.args["search"], "{{var}} подставлена")
	assert.Equal(t, "iac/provider", c.args["project_id"], "литерал не тронут")
}

// Главное свойство: описание не может обойти собственное сужение серверов.
func TestCallStepCannotEscapeFlowTools(t *testing.T) {
	c := &recordingCaller{out: "не должно случиться"}
	f := parseFlow(t, `
tools: ["srv-dev"]
steps:
  - name: sneaky
    call:
      tool: srv-prod:get_project
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "srv-prod")
	assert.Zero(t, c.calls, "до вызова дело не дошло")
}

// Аргументы могут быть вложенными; подстановка идёт только в строки, форма
// вызова и типы остаются как записаны.
func TestCallStepExpandsNestedArgsOnly(t *testing.T) {
	c := &recordingCaller{}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - call:
      tool: srv:tool
      args:
        recursive: true
        depth: 3
        filter: {path: "docs/{{name}}.md"}
        tags: ["{{name}}", "static"]
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"name": "vpc_vip"})
	require.NoError(t, err)
	assert.Equal(t, true, c.args["recursive"])
	assert.Equal(t, 3, c.args["depth"])
	assert.Equal(t, "docs/vpc_vip.md", c.args["filter"].(map[string]any)["path"])
	assert.Equal(t, []any{"vpc_vip", "static"}, c.args["tags"])
}

func TestCallStepErrorPolicy(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: flaky
    call:
      tool: srv:search
      save_as: schema
      on_error: continue
  - name: after
    call:
      tool: srv:other
      save_as: second
      on_error: continue
`)
	c := &recordingCaller{err: errors.New("upstream 500")}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err, "continue не роняет поток")
	assert.Contains(t, vars["schema"], "ERROR")
	assert.Equal(t, 2, c.calls, "поток дошёл до следующего шага")

	c = &recordingCaller{err: ErrDenied}
	vars, _, _ = ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	assert.Contains(t, vars["schema"], "DENIED", "отказ по правам отличим от сбоя")
}

// Без исполнителя вызовов шаг обязан отказать внятно, а не молча пропустить.
func TestCallStepWithoutCaller(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: lookup
    call:
      tool: srv:tool
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup")
}

func TestCallStepValidation(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, "tools: [\"srv\"]\nsteps:\n  - call:\n      tool: no_colon_here\n"), Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "сервер:инструмент")

	server, tool, ok := SplitToolRef("srv:ns:tool")
	assert.True(t, ok)
	assert.Equal(t, "srv", server)
	assert.Equal(t, "ns:tool", tool, "разделитель — первое двоеточие")
}

// «Шаг не дал результата» обязано покрывать и пустоту, и помеченный отказ:
// ветвлению «не нашли — ищем иначе» они означают одно и то же.
func TestIsEmptyCondition(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "schema is empty"
      then:
        - set: {var: took_fallback, value: "yes"}
`)
	for name, v := range map[string]string{
		"пусто":      "",
		"пробелы":    "   ",
		"сбой":       "ERROR: upstream 500",
		"отказ прав": "DENIED: skill-engine: denied",
	} {
		t.Run(name, func(t *testing.T) {
			vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": v})
			require.NoError(t, err)
			assert.Equal(t, "yes", vars["took_fallback"], "запасной путь выбран")
		})
	}

	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "строки схемы"})
	require.NoError(t, err)
	assert.Empty(t, vars["took_fallback"], "результат есть — запасной путь не нужен")
}

func TestIsNotEmptyCondition(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "schema is not empty"
      then:
        - set: {var: used, value: "yes"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "строки"})
	require.NoError(t, err)
	assert.Equal(t, "yes", vars["used"])

	vars, _, err = ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "ERROR: нет"})
	require.NoError(t, err)
	assert.Empty(t, vars["used"])
}

// Поток отдаёт РЕЗУЛЬТАТ, а не то, что ему подали на вход. Иначе вызывающий,
// ищущий «самую содержательную переменную», находит поданную историю разговора.
func TestExecuteReturnsOnlyProduced(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: answer, value: "готово"}
`)
	in := map[string]string{
		"input":   "вопрос пользователя",
		"history": "длинная-предлинная история разговора, которая длиннее любого ответа",
	}
	got, _, err := ExecuteWith(context.Background(), f, Deps{}, in)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"answer": "готово"}, got,
		"входные переменные не результат потока")
}

// Шаг, перезаписавший входную переменную, ЕЁ ПРОИЗВЁЛ — она результат.
func TestExecuteStepOverwriteCountsAsProduced(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: input, value: "переписано шагом"}
`)
	got, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"input": "исходное"})
	require.NoError(t, err)
	assert.Equal(t, "переписано шагом", got["input"])
}

// Поток без единого сервера НЕ разрешает всё: иначе скилл без `servers`
// (поле опционально) дотянется до write-инструментов.
func TestCallStepDeniedWhenFlowHasNoServers(t *testing.T) {
	c := &recordingCaller{out: "не должно случиться"}
	f := parseFlow(t, `
steps:
  - name: sneaky
    call:
      tool: gitlab-write-prod:create_merge_request
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "не объявлено ни одного сервера")
	assert.Zero(t, c.calls, "до вызова дело не дошло")
}

// `when` — условие применимости шага: ложно → шаг не исполняется вовсе.
func TestWhenSkipsStep(t *testing.T) {
	c := &recordingCaller{out: "не должно случиться"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: only_if_found
    when: "found == yes"
    call:
      tool: srv:tool
      save_as: out
  - set: {var: done, value: "конец"}
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"found": "no"})
	require.NoError(t, err)
	assert.Zero(t, c.calls, "шаг не исполнялся")
	assert.Equal(t, "конец", vars["done"], "поток пошёл дальше")
	assert.Equal(t, []string{"only_if_found"}, outcome.Skipped,
		"пропуск ВИДЕН: иначе частичное совпадение задачи снова незаметно")
}

func TestWhenRunsStepWhenTrue(t *testing.T) {
	c := &recordingCaller{out: "данные"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: guarded
    when: "found is not empty"
    call:
      tool: srv:tool
      save_as: out
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"found": "да"})
	require.NoError(t, err)
	assert.Equal(t, 1, c.calls)
	assert.Equal(t, "данные", vars["out"])
	assert.Empty(t, outcome.Skipped)
}

// Шаг exit прекращает поток особой ошибкой: вызывающий обязан отличить её от
// сбоя — реакция противоположная.
func TestExitStopsFlow(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: guard
    exit: {reason: "не мой случай: {{kind}}"}
  - set: {var: unreachable, value: "нет"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"kind": "разговор"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExit)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "не мой случай: разговор", ee.Reason, "причина с подстановкой")
	assert.Empty(t, vars["unreachable"], "шаги после exit не исполняются")
}

func TestWhenBadConditionRejectedByValidate(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, `
steps:
  - name: x
    when: "мусор без оператора"
    set: {var: a, value: b}
`), Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "when")
}

// on_server: сервер вычисляется на исполнении — 14 из 28 скиллов несут
// варианты одного сервера, и без этого каждому нужен switch из 2–5 веток.
func TestOnServerPicksServerAtRuntime(t *testing.T) {
	c := &recordingCaller{out: "логи"}
	f := parseFlow(t, `
tools: ["staging", "prod"]
steps:
  - name: logs
    on_server: "{{cluster}}"
    call:
      tool: kubectl_logs
      args: {name: "pod-1"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"cluster": "prod"})
	require.NoError(t, err)
	assert.Equal(t, "prod", c.server, "вызов ушёл на вычисленный сервер")
	assert.Equal(t, "kubectl_logs", c.tool)
}

// Вычисленное имя проходит ТУ ЖЕ проверку набора: подстановка не расширяет
// радиус доступа.
func TestOnServerCannotEscapeFlowTools(t *testing.T) {
	c := &recordingCaller{}
	f := parseFlow(t, `
tools: ["staging"]
steps:
  - name: logs
    on_server: "{{cluster}}"
    call:
      tool: kubectl_logs
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"cluster": "prod-k8s"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prod-k8s")
	assert.Zero(t, c.calls)
}

// Для шага-МОДЕЛИ on_server сужает набор инструментов до одного сервера.
func TestOnServerNarrowsModelStepTools(t *testing.T) {
	r := &fakeRunner{}
	f := parseFlow(t, `
tools: ["staging", "prod", "sandbox"]
steps:
  - name: pick
    on_server: "{{cluster}}"
    instruction: "найди под"
    save_as: pod
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, map[string]string{"cluster": "sandbox"})
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Equal(t, []string{"sandbox"}, r.seen[0].Tools,
		"модель видит инструменты ОДНОГО кластера, а не всех трёх")
}

// Хендл рабочей памяти доезжает до следующего шага: без него программа может
// работать только с тем, что влезло в превью, а большие выборки (полный json
// подов кластера) пришлось бы гнать через контекст модели.
func TestCallStepExposesMemHandle(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
    save_as: pods
  - name: crunch
    call:
      tool: srv:exec
      args: {stdin: {from: "{{pods.mem}}"}}
    save_as: out
`)
	c := &seqCaller{outs: []string{
		"превью…\n[mem:podsjson-a1b2 — это ПРЕВЬЮ, всего 812kb]",
		"готово",
	}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err)

	require.Len(t, c.seen, 2)
	stdin, ok := c.seen[1]["stdin"].(map[string]any)
	require.True(t, ok, "stdin не объект: %#v", c.seen[1])
	assert.Equal(t, "podsjson-a1b2", stdin["from"], "хендл не подставился")
}

// save_as рядом с call (а не внутри) — то, как его пишет автор: у шага модели
// оно живёт именно там. Молчаливая потеря стоила судье поисковый скилл вердикта по
// пустому списку, поэтому поле принимается на обоих уровнях.
func TestCallStepAcceptsStepLevelSaveAs(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
    save_as: hits
`)
	require.NoError(t, f.Validate())
	c := &seqCaller{outs: []string{"строки выдачи"}}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err)
	assert.Equal(t, "строки выдачи", vars["hits"], "результат call не сохранился")
}

// Две записи одного и того же поля с разными значениями — ошибка описания, а не
// повод угадывать, какая из них главная.
func TestCallStepConflictingSaveAsIsError(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
      save_as: inner
    save_as: outer
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save_as указан дважды")
}

// builtin — псевдо-сервер встроенных тулов, а не MCP-адресат: набор серверов
// потока про него ничего не знает, его радиус задаёт builtin_tools скилла.
func TestCallStepAllowsBuiltinServer(t *testing.T) {
	f := parseFlow(t, `
tools: ["tracker"]
steps:
  - name: draw
    call:
      tool: builtin:run_script
      args: {name: chart_timeseries}
    save_as: chart
`)
	c := &seqCaller{outs: []string{"готово"}}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err, "builtin отклонён радиусом потока")
	assert.Equal(t, "готово", vars["chart"])
	require.Len(t, c.seen, 1)
}

// Отказ радиуса оставляет след: под политикой continue шаг иначе исчезал из
// трассы бесследно, и выглядело это как отсутствие шага в описании.
func TestCallStepDeniedByScopeIsTraced(t *testing.T) {
	f := parseFlow(t, `
tools: ["tracker"]
steps:
  - name: sneak
    call:
      tool: vcs:get_file_contents
      args: {}
      on_error: continue
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: &seqCaller{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1, "отклонённый шаг не оставил следа")
	assert.NotEqual(t, "ok", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "vcs")
}
