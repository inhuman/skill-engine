package skillengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAssets struct {
	content map[string]string
	err     error
	calls   int
}

func (f *fakeAssets) Resolve(_ context.Context, name string, _ Asset) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.content[name], nil
}

// Код и конфиг идут в АРГУМЕНТ инструмента: шаг call: формируется описанием, не
// моделью, поэтому нагрузка до модели не доходит вовсе — ровно то, ради чего
// ассеты существуют (модель не может воспроизвести многокилобайтный литерал,
// она его регенерирует и портит).
func TestAssetGoesIntoCallArgs(t *testing.T) {
	c := &recordingCaller{out: "готово"}
	a := &fakeAssets{content: map[string]string{"chart": "import sys\nprint('ok')"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  chart:
    kind: code
    source: inline
    lang: python
    content: "не важно — резолвер подменит"
steps:
  - name: draw
    call:
      tool: exec:exec
      args:
        code: {from: "asset:chart"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, "import sys\nprint('ok')", c.args["code"], "содержимое подставлено хост-сайд")
}

// Текст и данные подставляются В ПРОМПТ: справочник соответствий бесполезен,
// если модель его не прочитает.
func TestAssetSubstitutedIntoInstruction(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{content: map[string]string{"metrics": "загрузка: rate(http_requests_total[5m])"}}
	f := parseFlow(t, `
assets:
  metrics:
    kind: data
    source: inline
    content: "подменится"
steps:
  - name: parse
    instruction: |
      Справочник:
      {{asset:metrics}}
      Запрос: {{input}}
    save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a},
		map[string]string{"input": "загрузка сервиса"})
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Contains(t, r.seen[0].Instruction, "rate(http_requests_total[5m])")
}

// Один ассет, потреблённый несколькими шагами, тянется ОДИН раз: внешний
// источник в горячем пути хода — сеть, и платить за неё трижды незачем.
func TestAssetFetchedOncePerTurn(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{content: map[string]string{"ref": "справочник"}}
	f := parseFlow(t, `
assets:
  ref: {kind: data, source: inline, content: "x"}
steps:
  - {name: one, instruction: "{{asset:ref}}", save_as: a}
  - {name: two, instruction: "{{asset:ref}}", save_as: b}
  - {name: three, instruction: "{{asset:ref}}", save_as: c}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, a.calls, "три потребителя — одно получение")
}

// Недоступный ассет разворачивается в пустоту, а не оставляет маркер: маркер,
// доехавший до модели, читался бы как часть инструкции.
func TestUnavailableAssetExpandsToEmpty(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{err: errors.New("источник недоступен")}
	f := parseFlow(t, `
assets:
  ref: {kind: data, source: mcp, ref: "wiki:get_page"}
steps:
  - {name: one, instruction: "было:{{asset:ref}}:стало", save_as: a}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, "было::стало", r.seen[0].Instruction)
}

// Связки полей проверяются ДО исполнения: ошибку в объявлении надо видеть при
// написании скилла, а не при первом запуске у пользователя.
// Валидация ассета проверяет ФОРМУ, а не словарь: движок не знает, какие у
// приложения бывают источники и роды нагрузки, и отвергать незнакомое значение
// значило бы отвергать чужие. Знает он одно — содержимое либо здесь, либо по
// адресу.
func TestAssetValidationChecksShapeNotVocabulary(t *testing.T) {
	t.Run("отвергается", func(t *testing.T) {
		for name, src := range map[string]string{
			"ни content, ни ref":    `assets: {a: {kind: text}}`,
			"и content, и ref":      `assets: {a: {content: "x", ref: "где-то"}}`,
			"кривой on_unavailable": `assets: {a: {ref: "где-то", fetch: {on_unavailable: как-нибудь}}}`,
		} {
			t.Run(name, func(t *testing.T) {
				f := parseFlow(t, src+"\nsteps: [{set: {var: a, value: b}}]")
				require.Error(t, f.Validate())
			})
		}
	})

	t.Run("пропускается", func(t *testing.T) {
		for name, src := range map[string]string{
			"незнакомый источник": `assets: {a: {source: своё-хранилище, ref: "адрес"}}`,
			"незнакомый род":      `assets: {a: {kind: чертёж, content: "x"}}`,
			"незнакомый маршрут":  `assets: {a: {content: "x", deliver: в-очередь}}`,
			"код без lang":        `assets: {a: {kind: code, content: "x"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				f := parseFlow(t, src+"\nsteps: [{set: {var: a, value: b}}]")
				require.NoError(t, f.Validate(), "словарь приложения движок не судит")
			})
		}
	})
}

// Маршрут доставки, объявленный ассетом, обязан доехать до аргументов вызова:
// мост читает его из `_deliver`, и без переноса объявление — украшение.
//
// Живой случай: скилл объявлял `deliver: reply` у ассета-рендера,
// вывод шага не помечался как ответ хода, и гейт публикации в MR отвергал ход
// целиком («финальный шаг скилла не зафиксировал результат»).
func TestAssetDeliverReachesCallArgs(t *testing.T) {
	c := &recordingCaller{out: "## Ревью\nзамечаний нет"}
	a := &fakeAssets{content: map[string]string{"render": "print('отчёт')"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  render:
    kind: code
    source: inline
    lang: python
    deliver: reply
    content: "не важно"
steps:
  - name: render
    call:
      tool: exec:exec
      args:
        code: {from: "asset:render"}
      save_as: answer
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"to": "reply"}, c.args["_deliver"],
		"объявленный ассетом маршрут переносится в аргументы вызова")
}

// Явный `_deliver` шага сильнее объявления ассета: он написан под конкретный
// вызов, а объявление — умолчание для всех потребителей ассета.
func TestStepDeliverBeatsAssetDeliver(t *testing.T) {
	c := &recordingCaller{out: "данные"}
	a := &fakeAssets{content: map[string]string{"probe": "print(1)"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  probe:
    kind: code
    source: inline
    lang: python
    deliver: reply
    content: "не важно"
steps:
  - name: probe
    call:
      tool: exec:exec
      args:
        code: {from: "asset:probe"}
        _deliver: {to: memory}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"to": "memory"}, c.args["_deliver"], "шаг перебивает ассет")
}

// deliver: none — осознанный отказ автора от доставки, а не «забыл указать».
func TestAssetDeliverNoneInjectsNothing(t *testing.T) {
	c := &recordingCaller{out: "данные"}
	a := &fakeAssets{content: map[string]string{"calc": "print(2)"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  calc:
    kind: code
    source: inline
    lang: python
    deliver: none
    content: "не важно"
steps:
  - name: calc
    call:
      tool: exec:exec
      args:
        code: {from: "asset:calc"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.NotContains(t, c.args, "_deliver")
}

// Ассет по ссылке ВНУТРИ СПИСКА: `command: ["sh","-c",{from: "asset:x"}]`.
// Форма не экзотическая — так задаётся запуск скрипта в k8s-job, и до перевода
// такой скилл в программу она была единственной рабочей. При переводе
// обёртка sh -c потерялась, содержимое ассета уехало в command СТРОКОЙ, и
// инструмент отверг вызов схемой (ждал массив) — а увидели это только когда
// починилась подстановка полей и аргументы перестали быть пустыми.
func TestAssetRefInsideListIsResolved(t *testing.T) {
	c := &recordingCaller{out: "ok"}
	a := &fakeAssets{content: map[string]string{"battery": "echo привет"}}
	f := parseFlow(t, `
tools: ["k8s-job"]
assets:
  battery:
    kind: code
    source: inline
    lang: bash
    content: "не важно"
steps:
  - name: run
    call:
      tool: k8s-job:run_job
      args:
        image: go-review
        command: ["sh", "-c", {from: "asset:battery"}]
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, []any{"sh", "-c", "echo привет"}, c.args["command"],
		"ссылка внутри списка подставляется содержимым, а не остаётся объектом")
}

// Хендл рабочей памяти в аргументах — это ССЫЛКА ({from: "<id>"}), а не строка.
// Строкой скрипт получает идентификатор вместо данных и падает на разборе.
//
// Живой случай: шаг рендера получал stdin ["<json>", "mrctx-ab",
// "mrctx-ab"] и отвечал «RENDER_ERROR: stdin не парсится как поток JSON»;
// доставлять было нечего, ревью не публиковалось. Форма потеряна при переводе
// скилла в YAML — тот же класс, что и утраченная обёртка sh -c у батарей.
func TestMemHandleRefStaysAReference(t *testing.T) {
	c := &recordingCaller{out: "ok"}
	f := parseFlow(t, `
tools: ["exec"]
steps:
  - name: render
    call:
      tool: exec:exec
      args:
        stdin: ["{{findings}}", {from: "{{ctx.mem}}"}]
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"findings": "{}", "ctx.mem": "res-7"})
	require.NoError(t, err)
	stdin, ok := c.args["stdin"].([]any)
	require.True(t, ok, "stdin остался списком")
	require.Equal(t, map[string]any{"from": "res-7"}, stdin[1],
		"хендл уехал ссылкой — резолвер подставит данные; строкой скрипт получил бы id")
}
