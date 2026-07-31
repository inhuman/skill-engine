package skillengine

// Обход потока: состояние исполнения и порядок шагов.
// Исполнение отдельных типов шага — steps.go, условия — cond.go,
// подстановка — expand.go.

import (
	"context"
	"time"
)

// Execute проходит поток шаг за шагом и возвращает переменные, ПРОИЗВЕДЁННЫЕ
// шагами. Входные (`vars`) в результат не попадают: они и так есть у
// вызывающего, а вот перепутать их с результатом — дорого. Живой класс: поток,
// не заполнивший ответ, отдавал вызывающему самую длинную переменную, и ею
// оказывалась поданная на вход история разговора — пользователь получал в чат
// стенограмму собственных реплик вместо ответа.
//
// Ошибка возвращается только когда поток прерван (PolicyAbort или сбой
// исполнителя). Отказ шага под другой политикой ошибкой не считается — он
// записывается в переменные, и это осознанно: «нет прав на чтение вложения»
// должно приводить к ответу по тому, что есть, а не к падению хода.
// Deps — исполнители, которые движок получает извне. Структурой, а не списком
// аргументов: их три, и добавление четвёртого не должно править каждый вызов.
type Deps struct {
	Runner   Runner
	Caller   ToolCaller
	Delegate SkillDelegate
	// Assets резолвит содержимое объявленных нагрузок.
	Assets AssetResolver
	// Memory отдаёт полный результат инструмента по хендлу рабочей памяти.
	//
	// Нужен подстановке поля: хост кладёт в переменную ПРЕВЬЮ (крупное — с
	// обрезкой), а `{{var.field}}` разбирает переменную как JSON. У обрезанного
	// превью разбор невозможен в принципе, хотя целое лежит в памяти под тем же
	// хендлом. Без этого поле молча пустеет — и вызов уезжает с пустым
	// аргументом.
	//
	// Необязателен: nil = поля доступны только у непревышенного результата.
	Memory MemoryReader
	// OnStepStart зовётся ПЕРЕД шагом. Нужен тем, кто показывает работу
	// человеку: шаг, идущий 14 секунд, до своего завершения не даёт ни одного
	// события, и всё это время показывать нечего.
	OnStepStart func(name, kind string)
	// OnStep зовётся СРАЗУ после каждого шага, а не в конце потока.
	//
	// Иначе вызывающий узнаёт обо всех шагах разом, когда ход уже закончен:
	// прогресс-пост показывает готовый список вместо работы по мере её
	// выполнения, и пользователь девять секунд смотрит на «brewing…».
	OnStep func(StepTrace)
}

// ExecuteWith исполняет поток с полным набором исполнителей.
func ExecuteWith(ctx context.Context, f *Flow, deps Deps, vars map[string]string) (map[string]string, Outcome, error) {
	st, err := newState(f, deps, vars)
	if err != nil {
		return nil, Outcome{}, err
	}
	st.assetCtx = ctx
	_, runErr := st.run(ctx, f.Steps)
	return st.produced(), Outcome{Skipped: st.skipped, Steps: st.traces, AnsweredBy: st.answeredBy}, runErr
}

// StepTrace — что случилось с одним шагом.
//
// Собирается движком и отдаётся вызывающему: тот кладёт это в события. Сам
// движок в телеметрию не ходит — он отчуждаемый и про неё не знает.
type StepTrace struct {
	// StartedAt — когда шаг начался. Вызывающему нужно, чтобы поставить строку
	// прогресса на её хронологическое место: событие приходит по ЗАВЕРШЕНИИ, а
	// относится к началу работы.
	StartedAt time.Time
	Name      string
	Kind      string // instruction | call | delegate | parallel | for_each | set | switch | if | exit
	Outcome   string // ok | skipped | denied | error | exit | truncated
	Reason    string
	Calls     int
	// CallsFailed — сколько вызовов шага отказали (см. Result.CallsFailed).
	CallsFailed int
	Duration    time.Duration
}

// Outcome — что произошло с потоком помимо переменных.
type Outcome struct {
	// Steps — след каждого исполненного (и пропущенного) шага.
	Steps []StepTrace
	// Skipped — шаги, не исполнённые из-за ложного `when`. Пустой список у
	// потока без условий; непустой означает, что задача подошла ЧАСТИЧНО, и
	// это единственный способ такое заметить.
	Skipped []string
	// AnsweredBy — вид шага, записавшего ОТВЕТ хода: "instruction" (текст писала
	// модель) либо "call" (текст напечатал инструмент). Разница не косметическая:
	// ответ модели — черновик, и его правомерно переписать «голосом»; вывод
	// детерминированного рендера черновиком не является, и переписывать его
	// значит терять ровно те гарантии, ради которых он детерминированный.
	AnsweredBy string
}

func newState(f *Flow, deps Deps, vars map[string]string) (*state, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	st := &state{vars: map[string]string{}, tools: f.Tools,
		runner: deps.Runner, caller: deps.Caller, delegate: deps.Delegate,
		onStep: deps.OnStep, onStepStart: deps.OnStepStart,
		assets: f.Assets, assetsRes: deps.Assets, memory: deps.Memory,
		assetCache: map[string]string{}, seeded: map[string]bool{}}
	for k, v := range f.Vars {
		st.vars[k] = v
		st.seeded[k] = true
	}
	for k, v := range vars {
		st.vars[k] = v
		st.seeded[k] = true
	}
	return st, nil
}

type state struct {
	vars     map[string]string
	tools    []string
	runner   Runner
	caller   ToolCaller
	delegate SkillDelegate
	// skipped — имена шагов, пропущенных по `when`. Возвращается вызывающему:
	// без этого частичное совпадение задачи снова становится невидимым, а это
	// одна из двух проблем, ради которых формат существует.
	skipped []string
	// traces — след каждого шага для событий вызывающего.
	traces []StepTrace
	// answeredBy — вид шага, последним записавшего ответ хода (см. Outcome).
	answeredBy string
	// onStep / onStepStart — уведомления вызывающего о шаге.
	onStep      func(StepTrace)
	onStepStart func(name, kind string)
	// assets — объявления и резолвер нагрузок.
	assets    map[string]Asset
	assetsRes AssetResolver
	memory    MemoryReader
	// assetCache — содержимое, уже добытое в ЭТОМ ходе: один ассет,
	// потреблённый тремя шагами, тянется один раз.
	assetCache map[string]string
	// assetCtx — контекст хода для резолва нагрузок. expand() зовётся из мест
	// без ctx под рукой, а тянуть его через все подписи ради одной ветки
	// дороже, чем сохранить здесь: state живёт ровно один ход.
	assetCtx context.Context
	// seeded — имена переменных, пришедших на ВХОД (vars потока и аргумент
	// Execute). Шаги их читают, но результатом потока они не являются.
	seeded map[string]bool
}

// produced отдаёт переменные, записанные ШАГАМИ. Шаг, перезаписавший входную
// переменную, попадает сюда — он её произвёл.
func (s *state) produced() map[string]string {
	out := make(map[string]string, len(s.vars))
	for k, v := range s.vars {
		if !s.seeded[k] {
			out[k] = v
		}
	}
	return out
}

// run исполняет список шагов. Возвращает true, если ветка была прервана
// политикой skip (вызывающий решает, продолжать ли снаружи).
func (s *state) run(ctx context.Context, steps []Step) (bool, error) {
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		skipped, err := s.one(ctx, step)
		if err != nil {
			return false, err
		}
		if skipped {
			return true, nil
		}
	}
	return false, nil
}

// set записывает переменную от имени ШАГА: значение становится результатом
// потока, даже если имя совпало со входным.
func (s *state) set(name, value string) {
	s.vars[name] = value
	delete(s.seeded, name)
}
