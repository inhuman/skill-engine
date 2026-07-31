// Package skill-engine исполняет декларативное описание скилла: последовательность
// шагов, у каждого — свой набор инструментов, свой лимит вызовов и своя политика
// при отказе.
//
// Зачем. Ограничение, записанное словами, модель выполняет вероятностно:
// замеренный пример — правило «в этот источник не ходи» дало за месяц 53
// попытки и ни одного успешного чтения. Отсутствие источника в наборе
// инструментов шага она не выполнить не может.
//
// Форма заимствована у AgentSPEX (arXiv 2604.13346, Apache 2.0) — YAML-язык
// спецификации агентных workflow. Берётся ФОРМА, не код: там Python со своим
// harness и своей песочницей. Подмножество выбрано по факту: три живых скилла
// (скилл справки, скилл поиска по вики, jira-ticket), выписанные в этом синтаксисе,
// использовали шаги, ветвление и переменные — и ни разу parallel/while/gather.
//
// Пакет пишется ОТЧУЖДАЕМЫМ: домен (инструменты, RBAC, телеметрия) живёт за
// интерфейсами ниже, внутрь не импортируется ничего специфичного для
// конкретного агента. Имена в коде, тестах и примерах — нейтральные.
package skillengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Flow — разобранное описание: набор шагов, исполняемых по порядку.
type Flow struct {
	// Steps исполняются последовательно; ветвление — внутри шага (Switch/If).
	Steps []Step `yaml:"steps"`
	// Tools — набор инструментов, доступный всему потоку. Шаг может только
	// сузить его, не расширить: расширение сделало бы ограничение бессмысленным.
	Tools []string `yaml:"tools,omitempty"`
	// Vars — начальные значения переменных потока.
	Vars map[string]string `yaml:"vars,omitempty"`
	// Assets — именованные нагрузки, передаваемые инструментам по ссылке
	// (см. asset.go). Объявляются в описании скилла, не в шаге: одну и ту же
	// нагрузку часто потребляют несколько шагов.
	Assets map[string]Asset `yaml:"assets,omitempty"`
}

// Step — один шаг. Ровно одно из полей Run/Switch/If/Set должно быть заполнено;
// это проверяет Validate.
type Step struct {
	Name string `yaml:"name,omitempty"`

	// Run — шаг, выполняемый моделью: инструкция + результат в переменную.
	Run *Run `yaml:",inline"`
	// Switch — ветвление по значению переменной.
	Switch *Switch `yaml:"switch,omitempty"`
	// If — ветвление по условию.
	If *If `yaml:"if,omitempty"`
	// Set — вычисление переменной без обращения к модели.
	Set *Set `yaml:"set,omitempty"`
	// Call — вызов инструмента без обращения к модели.
	Call *Call `yaml:"call,omitempty"`
	// Exit — прекратить поток и вернуть ход на прежний путь.
	Exit *Exit `yaml:"exit,omitempty"`
	// Delegate — передать работу другому скиллу (см. тип Delegate).
	Delegate *Delegate `yaml:"delegate,omitempty"`
	// Parallel — несколько независимых веток одновременно (см. тип Parallel).
	Parallel *Parallel `yaml:"parallel,omitempty"`
	// ForEach — повторить шаги для каждого элемента коллекции.
	ForEach *ForEach `yaml:"for_each,omitempty"`

	// OnServer — на каком сервере исполняется шаг. Поддерживает {{var}}: имя
	// вычисляется на исполнении.
	//
	// Для call: адресат вызова; для шага-модели — чьи инструменты ему выдаются.
	// Зачем: 14 из 28 живых скиллов несут ВАРИАНТЫ одного сервера
	// (gitlab dev/prod, пять k8s-кластеров, prometheus dev/prod), и без выбора
	// на исполнении каждый требует switch из 2–5 почти одинаковых веток — тот
	// самый дубль, который расходится при первой правке.
	//
	// Права не ослабевают: проверяется ПОДСТАВЛЕННОЕ имя, и оно обязано входить
	// в набор потока.
	OnServer string `yaml:"on_server,omitempty"`

	// When — условие применимости шага (синтаксис как у If.Cond). Ложно → шаг
	// пропускается, поток идёт дальше.
	//
	// Зачем отдельно от If: выразимо и ветвлением, но `when` читается как
	// СВОЙСТВО шага и не растит вложенность там, где ветки нет — есть только
	// «делать или нет». Живой класс: задача подходит скиллу частично — тогда ход
	// «иногда даёт нужный ответ, иногда нет», и причина не видна. Пропуск по
	// условию виден в событиях.
	When string `yaml:"when,omitempty"`
}

// Exit — выход из скилла: поток прекращается, ход возвращается на обычный путь.
//
// Нужен потому, что шаги исполняются целиком, что бы им ни поручили: у шага нет
// повода усомниться в том, что скилл выбран верно. Выход делает сомнение
// выразимым.
//
// Решение — не новый механизм, а новый ТИП ШАГА: классификатор в программе уже
// есть, ему достаточно значения «не мой случай» и ветки с exit.
type Exit struct {
	// Reason — почему вышли. Идёт в событие и в лог: по этим строкам разбирают
	// промахи роутинга.
	Reason string `yaml:"reason,omitempty"`
}

// Call — прямой вызов инструмента, БЕЗ участия модели.
//
// Зачем. Шаг, исполняемый моделью, стоит две генерации даже когда делать
// нечего: одну — чтобы решить позвать инструмент, вторую — чтобы пересказать
// его результат. Замер: 6 генераций на 2 вызова инструментов. При этом
// сам вызов часто ДЕТЕРМИНИРОВАН — все аргументы уже лежат в переменных потока
// (их вычислили предыдущие шаги или Set).
//
// Отличие от Run: здесь нет ни инструкции, ни выбора, ни пересказа — результат
// инструмента попадает в переменную дословно. Заодно исчезает целый класс
// ошибок «модель позвала инструмент не в том конверте».
//
// Права НЕ обходятся: вызов идёт тем же путём, что и вызов от модели, и
// проходит те же проверки. Шаг не может дотянуться до инструмента, который
// вызывающему не разрешён.
type Call struct {
	// Tool — какой инструмент звать, в форме "сервер:инструмент".
	Tool string `yaml:"tool"`
	// Args — аргументы вызова. Значения поддерживают подстановку {{var}};
	// подставляется ТОЛЬКО в строки, структура остаётся как записана.
	Args map[string]any `yaml:"args,omitempty"`
	// SaveAs — переменная для результата ("" = результат не сохраняется).
	SaveAs string `yaml:"save_as,omitempty"`
	// OnError — что делать при отказе. Умолчание — Abort, как у Run.
	OnError ErrorPolicy `yaml:"on_error,omitempty"`
}

// Run — шаг, который исполняет модель.
type Run struct {
	// Instruction — что сделать. Поддерживает подстановку {{var}}.
	Instruction string `yaml:"instruction,omitempty"`
	// SaveAs — имя переменной для результата ("" = результат не сохраняется).
	SaveAs string `yaml:"save_as,omitempty"`

	// Tools — инструменты ЭТОГО шага. Пустой непустого потока = шагу инструменты
	// не выдаются вовсе (шаг обязан ответить из того, что уже собрано).
	// Отличать «не задано» от «пустой список» позволяет указатель.
	Tools *[]string `yaml:"tools,omitempty"`

	// MaxCalls — потолок обращений к инструментам на этом шаге. Поле шага, а не
	// потока: в живых скиллах лимиты разные — поиск по коду одна попытка, обход
	// дерева до восьми.
	//
	// 0 — НЕ «без ограничения»: исполнитель подставляет свой потолок (у нашего
	// это 8 генераций). Безлимитного шага в формате нет намеренно — шаг без
	// потолка возвращает ровно ту проблему, ради которой формат затевался.
	MaxCalls int `yaml:"max_calls,omitempty"`

	// MaxToolErrors — сколько ПРОМАХОВ модели прощается шагу сверх max_calls.
	//
	// Промах — вызов, отвергнутый инструментом (забыт обязательный аргумент,
	// битый JSON). Он стоит генерации, поэтому бесплатным быть не может, но и
	// съедать бюджет ПОЛЕЗНОЙ работы не должен: живой отказ — шаг ревью сжёг все
	// 6 вызовов на промахах (6 из 7 отвергнуты сервером) и не прочитал ни одного
	// диффа.
	//
	// 0 — умолчание исполнителя (2), не «без ограничения»: бесконечные попытки
	// вернут ровно ту проблему, ради которой формат затевался. Превышение
	// останавливает шаг с degraded и названной причиной, а не молча.
	MaxToolErrors int `yaml:"max_tool_errors,omitempty"`

	// OnError — что делать, когда шаг не смог. Умолчание — Abort.
	OnError ErrorPolicy `yaml:"on_error,omitempty"`

	// OneOf — допустимые значения результата шага. Задано → результат
	// нормализуется: из ответа берётся то из перечисленных значений, которое в
	// нём найдено; ничего не найдено — переменная пустеет.
	//
	// Зачем. Шаг-классификатор существует ради ветвления, а модель отвечает
	// связным текстом: на просьбу «ответь одним словом: t1 или foreign» приходит
	// «Итог: определил тип по префиксу. Результат: foreign» — верно по смыслу и
	// бесполезно для switch, который сравнивает точно. Живой отказ: ветка не
	// выбралась, ход ушёл в default.
	//
	// Это НЕ проверка формата у модели (просьба, которую она выполняет
	// вероятностно), а нормализация НА ВЫХОДЕ, в коде.
	OneOf []string `yaml:"one_of,omitempty"`

	// Model — модель ЭТОГО шага (пусто = модель по умолчанию у исполнителя).
	Model string `yaml:"model,omitempty"`
	// Sampling — параметры генерации шага (см. тип Sampling).
	Sampling *Sampling `yaml:"sampling,omitempty"`
	// ResponseSchema — схема структурного ответа шага. Результат кладётся в
	// переменную объектом, поля доступны как {{var.field}}.
	//
	// Осмысленна ТОЛЬКО там, где работает грамматика декодирования: на
	// harmony-пути схема выбрасывается молча, и структурный ответ выродился бы
	// в «модель обычно отвечает JSON» — необнаруживаемую дыру. Поэтому
	// исполнитель ОБЯЗАН отказать, если путь к модели шага грамматику не
	// доносит, а не тихо продолжить.
	ResponseSchema map[string]any `yaml:"response_schema,omitempty"`
}

// Switch — ветвление по значению переменной.
// Delegate — передать работу другому скиллу.
//
// Этим заняты composite-скиллы: bug-triage зовёт jira-ticket, скилл поиска —
// скилл поиска по вики и kg-search. Без такого шага composite, получивший описание,
// исполнился бы как обычный поток и потерял бы делегирование целиком, то есть
// перестал бы делать своё дело.
//
// Радиус — радиус ВЫЗВАННОГО скилла: он объявляет свои серверы сам, и
// делегирование не расширяет доступ вызывающего.
type Delegate struct {
	Skill   string      `yaml:"skill"`
	Task    string      `yaml:"task"`
	SaveAs  string      `yaml:"save_as,omitempty"`
	OnError ErrorPolicy `yaml:"on_error,omitempty"`
}

// Parallel — независимые ветки одновременно: в живых скиллах это параллельный
// сбор улик из разных источников.
//
// Ветки НЕ ВИДЯТ переменных друг друга: иначе результат зависел бы от порядка
// завершения, а он недетерминирован — формат существует ради обратного.
type Parallel struct {
	Branches [][]Step    `yaml:"branches"`
	Collect  string      `yaml:"collect,omitempty"`
	OnError  ErrorPolicy `yaml:"on_error,omitempty"`
}

// ForEach — повторить шаги для каждого элемента коллекции.
//
// Живые случаи, которые иначе не выражаются: «для КАЖДОГО из 5 сервисов найди
// репозиторий и коммит», «по каждому сервису релиза из versions.yaml», «по
// каждому сервису из всех таблиц». Длина списка заранее неизвестна —
// развернуть в фиксированные шаги нельзя.
type ForEach struct {
	// In — переменная с коллекцией. Массив (например, из структурного ответа)
	// итерируется поэлементно; строка — по непустым строкам. Второе покрывает
	// наблюдаемый случай «список, добытый инструментом».
	In string `yaml:"in"`
	// As — имя переменной элемента внутри тела цикла.
	As    string `yaml:"as"`
	Steps []Step `yaml:"steps"`
	// Collect — переменная, куда сводятся результаты итераций.
	Collect string `yaml:"collect,omitempty"`
	// MaxIterations — потолок. Обязателен ПО СМЫСЛУ, даже когда не задан явно:
	// цикл по коллекции неизвестной длины — прямой путь к runaway, а сессионный
	// бюджет остановит его уже после того, как ход сожжён.
	MaxIterations int         `yaml:"max_iterations,omitempty"`
	OnError       ErrorPolicy `yaml:"on_error,omitempty"`
}

// DefaultMaxIterations — потолок цикла, когда описание своего не задало.
const DefaultMaxIterations = 10

// SkillDelegate исполняет шаг delegate. Третья (и последняя) точка
// соприкосновения пакета с внешним миром — рядом с Runner и ToolCaller.
type SkillDelegate interface {
	Delegate(ctx context.Context, skill, task string) (string, error)
}

// Sampling — параметры генерации шага.
//
// Единица настройки — ШАГ, а не скилл, по той же причине, по которой шагу
// принадлежат tools и max_calls: у шагов разная работа. Классификатору нужна
// нулевая температура — он выбирает из двух значений; формулировке ответа
// человеку нужна теплее, иначе текст деревянный. Один параметр на весь скилл
// обслуживает оба плохо.
type Sampling struct {
	Temperature       *float32 `yaml:"temperature,omitempty"`
	TopP              *float32 `yaml:"top_p,omitempty"`
	TopK              *int     `yaml:"top_k,omitempty"`
	MinP              *float64 `yaml:"min_p,omitempty"`
	RepetitionPenalty *float64 `yaml:"repetition_penalty,omitempty"`
	// MaxTokens — потолок выходных токенов ЭТОГО шага. Незадан — действует
	// глобальный MaxOutputTokens.
	//
	// Нужен там, где шаг по природе краток, а сорвавшаяся генерация дорога:
	// два куска ревью выдали 32768 и 22482 токена вместо обычных ~2300,
	// упёршись в глобальный потолок, и один из них при этом сломал свой же
	// структурный ответ. Глобальная ручка тут не помощник — она обслуживает
	// потребителей с разными конвертами.
	MaxTokens *int `yaml:"max_tokens,omitempty"`
	// Reasoning — глубина рассуждения (low|medium|high) у моделей, которые её
	// поддерживают. Задаётся ШАГУ, которому она нужна: замер проекта показал,
	// что high на роли investigator съедал 83% времени субагентов.
	Reasoning string `yaml:"reasoning,omitempty"`
}

type Switch struct {
	Var     string            `yaml:"var"`
	Cases   map[string][]Step `yaml:"cases"`
	Default []Step            `yaml:"default,omitempty"`
}

// If — ветвление по условию вида "var == value" / "var != value".
type If struct {
	Cond string `yaml:"cond"`
	Then []Step `yaml:"then"`
	Else []Step `yaml:"else,omitempty"`
}

// Set — присваивание переменной. Значение поддерживает подстановку {{var}}.
type Set struct {
	Var   string `yaml:"var"`
	Value string `yaml:"value"`
}

// ErrorPolicy — реакция на отказ шага.
//
// Три класса отказа различаются намеренно: отказ по правам (инструмент есть, но
// вызывающему не разрешён) требует другого поведения, чем пустой результат.
// Политика повторялась дословно у трёх разных авторов скиллов — верный признак,
// что она принадлежит движку, а не описанию.
type ErrorPolicy string

const (
	// PolicyAbort — прекратить поток (умолчание).
	PolicyAbort ErrorPolicy = "abort"
	// PolicyContinue — записать отказ в переменную шага и идти дальше.
	// Так выражается «нет прав — скажи об этом и продолжай с тем, что есть».
	PolicyContinue ErrorPolicy = "continue"
	// PolicySkip — пропустить остаток текущей ветки, не прерывая поток.
	PolicySkip ErrorPolicy = "skip"
)

// Result — исход шага, попадающий в переменные потока.
type Result struct {
	Text  string
	Calls int
	// CallsFailed — сколько из них ОТКАЗАЛИ. Шаг с calls=7 и нулём полезных
	// результатов внешне неотличим от отработавшего, и разбираться приходится
	// в логах пода.
	CallsFailed int
	// Note — причина, которую знает ИСПОЛНИТЕЛЬ и не может вывести движок:
	// «остановлен по превышению промахов» против «упёрся в потолок». Пустая —
	// движок судит сам. Без неё точная причина тонет в общей «шаг не дал
	// текста», и чинить приходится вслепую (наступили 31.07 на своей же правке).
	Note string
	// Truncated — ответ не дописан: апстрим оборвал генерацию по лимиту токенов.
	// Отдельно от Note, потому что это МАШИННЫЙ признак: по нему решают, стоит
	// ли переиграть шаг, а Note — человеческая причина деградации в отчёте.
	Truncated bool
	Err       error
}

// validateCall проверяет форму вызова до исполнения: имя инструмента должно
// нести и сервер, и инструмент — иначе адресат вызова неизвестен.
func validateCall(c *Call, at string, serverFromStep bool) error {
	if serverFromStep {
		// Сервер задан шагом (on_server) — в tool достаточно имени инструмента.
		if strings.TrimSpace(c.Tool) == "" {
			return fmt.Errorf("%s: call без имени инструмента", at)
		}
		return validateErrPolicy(c.OnError, at)
	}
	server, tool, ok := SplitToolRef(c.Tool)
	if !ok {
		return fmt.Errorf("%s: call %q: ожидается «сервер:инструмент» (или сервер в on_server)", at, c.Tool)
	}
	if server == "" || tool == "" {
		return fmt.Errorf("%s: call %q: пустой сервер или инструмент", at, c.Tool)
	}
	return validateErrPolicy(c.OnError, at)
}

func validateErrPolicy(p ErrorPolicy, at string) error {
	switch p {
	case "", PolicyAbort, PolicyContinue, PolicySkip:
		return nil
	default:
		return fmt.Errorf("%s: неизвестная on_error %q", at, p)
	}
}

// SplitToolRef разбирает «сервер:инструмент». Разделитель — ПЕРВОЕ двоеточие:
// в именах инструментов оно встречается, в именах серверов — нет.
func SplitToolRef(ref string) (server, tool string, ok bool) {
	i := strings.Index(ref, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:]), true
}

// ToolCaller вызывает инструмент напрямую. Вторая (и последняя) точка
// соприкосновения пакета с внешним миром — рядом с Runner.
type ToolCaller interface {
	CallTool(ctx context.Context, server, tool string, args map[string]any) (string, error)
}

// MemoryReader отдаёт полный результат инструмента по хендлу рабочей памяти.
//
// Интерфейс, а не тип хоста: пакет обязан оставаться отчуждаемым (FR-040), и
// про рабочую память приложения он знает ровно столько — «по строке-хендлу можно
// получить строку».
type MemoryReader interface {
	Get(id string) (string, bool)
}

// Runner исполняет один шаг: даёт модели инструкцию, разрешая ровно
// перечисленные инструменты, и возвращает текст ответа.
//
// Это ЕДИНСТВЕННАЯ точка соприкосновения с внешним миром: всё, что знает пакет
// про модели, инструменты, права и телеметрию, спрятано за этим интерфейсом.
type Runner interface {
	Run(ctx context.Context, req StepRequest) (Result, error)
}

// StepRequest — то, что нужно исполнителю шага.
type StepRequest struct {
	Name        string
	Instruction string
	// Tools — точный набор разрешённых инструментов. nil = набор потока.
	Tools    []string
	MaxCalls int
	// MaxToolErrors — сколько промахов модели прощается сверх MaxCalls.
	MaxToolErrors int
	// Model / Sampling — чем и как генерировать шаг. Пусто = умолчания
	// исполнителя.
	Model          string
	Sampling       *Sampling
	ResponseSchema map[string]any
	// OneOf — допустимые значения результата. Исполнитель, чья модель держит
	// грамматику декодирования, передаёт их как enum: тогда значение вне списка
	// становится НЕВОЗМОЖНЫМ, а не исправляется задним числом.
	OneOf []string
}

// ErrDenied — отказ по правам. Возвращается Runner'ом, чтобы политика могла
// отличить «не разрешено» от «сломалось»: на первое повторы и обходные пути
// бессмысленны, на второе — иногда осмысленны.
var ErrDenied = errors.New("skill-engine: denied")

// ErrExit — поток прекращён шагом `exit`. НЕ ошибка исполнения: вызывающий
// обязан отличить её от сбоя, потому что реакция противоположная — не
// «показать, что собрали», а «этот скилл не подходит, иди обычным путём».
var ErrExit = errors.New("skill-engine: exit")

// ExitError несёт причину выхода до вызывающего.
type ExitError struct{ Reason string }

func (e *ExitError) Error() string {
	if e.Reason == "" {
		return ErrExit.Error()
	}
	return ErrExit.Error() + ": " + e.Reason
}

func (e *ExitError) Is(target error) bool { return target == ErrExit }

// ToolRef — инструмент, объявленный описанием.
type ToolRef struct {
	Server string
	Tool   string
	// Dynamic — сервер вычисляется на исполнении ({{var}} в on_server), то
	// есть статически неизвестен: проверять права нужно по всему набору потока.
	Dynamic bool
}

// DeclaredTools перечисляет инструменты, которые поток может позвать.
//
// Чистая функция над описанием: права проверяет вызывающий, движок про них не
// знает. Смысл — узнать об отказе ДО первой генерации, а не на середине, когда
// шаги уже сожжены.
func (f *Flow) DeclaredTools() []ToolRef {
	var out []ToolRef
	collectTools(f.Steps, f.Tools, &out)
	return out
}

func collectTools(steps []Step, flowTools []string, out *[]ToolRef) {
	for _, s := range steps {
		dynamic := strings.Contains(s.OnServer, "{{")
		switch {
		case s.Call != nil:
			server, tool, _ := SplitToolRef(s.Call.Tool)
			if s.OnServer != "" {
				if !dynamic {
					server = s.OnServer
				}
				if _, bare, ok := SplitToolRef(s.Call.Tool); ok {
					tool = bare
				} else {
					tool = s.Call.Tool
				}
			}
			*out = append(*out, ToolRef{Server: server, Tool: tool, Dynamic: dynamic})
		case s.Run != nil && strings.TrimSpace(s.Run.Instruction) != "":
			// У шага-модели конкретный инструмент выбирает она сама —
			// статически известен только СЕРВЕР.
			servers := flowTools
			if s.Run.Tools != nil {
				servers = *s.Run.Tools
			}
			if s.OnServer != "" && !dynamic {
				servers = []string{s.OnServer}
			}
			for _, srv := range servers {
				*out = append(*out, ToolRef{Server: srv, Dynamic: dynamic})
			}
		}
		if s.Switch != nil {
			for _, br := range s.Switch.Cases {
				collectTools(br, flowTools, out)
			}
			collectTools(s.Switch.Default, flowTools, out)
		}
		if s.If != nil {
			collectTools(s.If.Then, flowTools, out)
			collectTools(s.If.Else, flowTools, out)
		}
		if s.ForEach != nil {
			collectTools(s.ForEach.Steps, flowTools, out)
		}
		if s.Parallel != nil {
			for _, br := range s.Parallel.Branches {
				collectTools(br, flowTools, out)
			}
		}
	}
}

// Validate проверяет описание до исполнения: пустой поток, шаг без действия,
// два действия в одном шаге, ссылка на неизвестную политику.
func (f *Flow) Validate() error {
	if f == nil || len(f.Steps) == 0 {
		return fmt.Errorf("skill-engine: пустой поток — нет ни одного шага")
	}
	if err := normalizeSteps(f.Steps, "steps"); err != nil {
		return err
	}
	if err := validateAssets(f.Assets); err != nil {
		return fmt.Errorf("skill-engine: %w", err)
	}
	return validateSteps(f.Steps, "steps")
}

// normalizeSteps приводит описание к тому, как его читает исполнение.
//
// Шаг модели объявляет save_as на своём уровне (Run встроен в шаг), а call — у
// себя внутри. Разница не видна глазом, и автор пишет save_as там же, где
// привык. Результат молча не сохранялся: следующий шаг получал пустую строку и
// решал вслепую — живой случай 29.07, судья скилл поиска по вики выносил вердикт по
// пустому списку. Поэтому save_as, записанный на уровне шага рядом с call,
// принимается как принадлежащий call.
func normalizeSteps(steps []Step, path string) error {
	for i := range steps {
		s := &steps[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		if s.Name != "" {
			at = fmt.Sprintf("%s (%s)", at, s.Name)
		}
		if s.Run != nil && strings.TrimSpace(s.Run.Instruction) == "" && s.Run.SaveAs != "" {
			switch {
			case s.Call != nil && s.Call.SaveAs != "" && s.Call.SaveAs != s.Run.SaveAs:
				return fmt.Errorf("%s: save_as указан дважды и по-разному (%q у шага, %q внутри call)",
					at, s.Run.SaveAs, s.Call.SaveAs)
			case s.Call != nil:
				s.Call.SaveAs = s.Run.SaveAs
				s.Run = nil
			case s.Delegate != nil && s.Delegate.SaveAs == "":
				s.Delegate.SaveAs = s.Run.SaveAs
				s.Run = nil
			}
		}
		for _, br := range s.Branches() {
			if err := normalizeSteps(br, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSteps(steps []Step, path string) error {
	for i, s := range steps {
		at := fmt.Sprintf("%s[%d]", path, i)
		if s.Name != "" {
			at = fmt.Sprintf("%s (%s)", at, s.Name)
		}
		n := 0
		if s.Run != nil && strings.TrimSpace(s.Run.Instruction) != "" {
			n++
			switch s.Run.OnError {
			case "", PolicyAbort, PolicyContinue, PolicySkip:
			default:
				return fmt.Errorf("%s: неизвестная on_error %q", at, s.Run.OnError)
			}
			if s.Run.MaxCalls < 0 {
				return fmt.Errorf("%s: max_calls отрицательный", at)
			}
		}
		if s.Switch != nil {
			n++
			if strings.TrimSpace(s.Switch.Var) == "" {
				return fmt.Errorf("%s: switch без var", at)
			}
			for k, br := range s.Switch.Cases {
				if err := validateSteps(br, at+".cases."+k); err != nil {
					return err
				}
			}
			if err := validateSteps(s.Switch.Default, at+".default"); err != nil {
				return err
			}
		}
		if s.If != nil {
			n++
			if _, _, _, err := parseCond(s.If.Cond); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			if err := validateSteps(s.If.Then, at+".then"); err != nil {
				return err
			}
			if err := validateSteps(s.If.Else, at+".else"); err != nil {
				return err
			}
		}
		if s.Set != nil {
			n++
			if strings.TrimSpace(s.Set.Var) == "" {
				return fmt.Errorf("%s: set без var", at)
			}
		}
		if s.Call != nil {
			n++
			if err := validateCall(s.Call, at, s.OnServer != ""); err != nil {
				return err
			}
		}
		if s.Exit != nil {
			n++
		}
		if s.Delegate != nil {
			n++
			if strings.TrimSpace(s.Delegate.Skill) == "" {
				return fmt.Errorf("%s: delegate без skill", at)
			}
			if strings.TrimSpace(s.Delegate.Task) == "" {
				return fmt.Errorf("%s: delegate без task", at)
			}
			if err := validateErrPolicy(s.Delegate.OnError, at); err != nil {
				return err
			}
		}
		if s.ForEach != nil {
			n++
			if strings.TrimSpace(s.ForEach.In) == "" {
				return fmt.Errorf("%s: for_each без in", at)
			}
			// `in` — ИМЯ переменной, а не шаблон. Поле выглядит так, будто примет
			// «{{parts}}», и молча не примет: движок ищет переменную с таким
			// именем, не находит, цикл делает НОЛЬ итераций и рапортует «ok».
			// Отказ на разборе дешевле: пустой обход выглядит как успешный
			// .
			if strings.Contains(s.ForEach.In, "{{") {
				return fmt.Errorf("%s: for_each.in = %q — здесь ИМЯ переменной, без {{ }}",
					at, s.ForEach.In)
			}
			if strings.TrimSpace(s.ForEach.As) == "" {
				return fmt.Errorf("%s: for_each без as", at)
			}
			if s.ForEach.MaxIterations < 0 {
				return fmt.Errorf("%s: max_iterations отрицательный", at)
			}
			if err := validateSteps(s.ForEach.Steps, at+".for_each"); err != nil {
				return err
			}
			if err := validateErrPolicy(s.ForEach.OnError, at); err != nil {
				return err
			}
		}
		if s.Parallel != nil {
			n++
			if len(s.Parallel.Branches) < 2 {
				return fmt.Errorf("%s: parallel меньше чем из двух веток — это обычная последовательность", at)
			}
			for i, br := range s.Parallel.Branches {
				if err := validateSteps(br, fmt.Sprintf("%s.branches[%d]", at, i)); err != nil {
					return err
				}
			}
			if err := validateErrPolicy(s.Parallel.OnError, at); err != nil {
				return err
			}
		}
		if s.When != "" {
			if _, _, _, err := parseCond(s.When); err != nil {
				return fmt.Errorf("%s: when: %w", at, err)
			}
		}
		switch n {
		case 1:
		case 0:
			return fmt.Errorf("%s: шаг ничего не делает", at)
		default:
			return fmt.Errorf("%s: в одном шаге несколько действий", at)
		}
	}
	return nil
}

// Branches отдаёт вложенные наборы шагов: ветки switch/if, тело for_each, ветви
// parallel. Нужна тем, кто обходит описание целиком — валидаторам и линтеру:
// иначе каждый заново перечисляет места вложенности и забывает то, что добавили
// последним (ровно так ветка switch унесла бы опечатку в прод).
func (s *Step) Branches() [][]Step {
	var out [][]Step
	if s.Switch != nil {
		for _, br := range s.Switch.Cases {
			out = append(out, br)
		}
		if len(s.Switch.Default) > 0 {
			out = append(out, s.Switch.Default)
		}
	}
	if s.If != nil {
		if len(s.If.Then) > 0 {
			out = append(out, s.If.Then)
		}
		if len(s.If.Else) > 0 {
			out = append(out, s.If.Else)
		}
	}
	if s.ForEach != nil && len(s.ForEach.Steps) > 0 {
		out = append(out, s.ForEach.Steps)
	}
	if s.Parallel != nil {
		out = append(out, s.Parallel.Branches...)
	}
	return out
}

// AnswerVar — переменная, из которой берётся ответ хода. Шаг, не назвавший
// save_as, пишет сюда: не назвать её — обычная форма «это финальный шаг».
const AnswerVar = "answer"

// BuiltinServer — псевдо-сервер для встроенных инструментов встраивающего приложения
// (`call: {tool: "builtin:run_script"}`).
//
// Нужен, чтобы детерминированную цепочку можно было выразить шагами `call`, а
// не поручать модели: цепочка jira_search → count_per_day → chart_timeseries
// каждый раз одна и та же, и три модельных вызова ради неё — это три шанса
// перепутать хендл или аргументы.
const BuiltinServer = "builtin"
