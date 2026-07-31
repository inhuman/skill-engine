package skillengine

// Исполнение отдельных типов шага и политика отказа.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
)

// stepName — имя шага для следа и прогресса. Имя необязательно, а пустое в
// ленте не читается: у ветвлений роль видна по типу.
func stepName(step Step) string {
	if step.Name != "" {
		return step.Name
	}
	return stepKind(step)
}

// stepKind — тип шага для следа.
func stepKind(step Step) string {
	switch {
	case step.Call != nil:
		return "call"
	case step.Delegate != nil:
		return "delegate"
	case step.Parallel != nil:
		return "parallel"
	case step.ForEach != nil:
		return "for_each"
	case step.Exit != nil:
		return "exit"
	case step.Set != nil:
		return "set"
	case step.Switch != nil:
		return "switch"
	case step.If != nil:
		return "if"
	default:
		return "instruction"
	}
}

func (s *state) trace(step Step, outcome, reason string, calls int, started time.Time) {
	s.traceCalls(step, outcome, reason, calls, 0, started)
}

// traceCalls — след шага с разбивкой вызовов на все и отказавшие.
func (s *state) traceCalls(step Step, outcome, reason string, calls, failed int, started time.Time) {
	kind := stepKind(step)
	name := stepName(step)
	tr := StepTrace{
		StartedAt: started,
		Name:      name, Kind: kind, Outcome: outcome,
		Reason: reason, Calls: calls, CallsFailed: failed, Duration: time.Since(started),
	}
	s.traces = append(s.traces, tr)
	if s.onStep != nil {
		s.onStep(tr)
	}
}

func (s *state) one(ctx context.Context, step Step) (bool, error) {
	started := time.Now()
	if s.onStepStart != nil {
		s.onStepStart(stepName(step), stepKind(step))
	}
	// Условие применимости проверяется ДО действия: шаг, чьё предусловие
	// ложно, не исполняется вовсе — ни модель, ни инструмент не зовутся.
	if step.When != "" {
		ok, err := s.eval(step.When)
		if err != nil {
			return false, fmt.Errorf("шаг %q: when: %w", stepLabel(step), err)
		}
		if !ok {
			s.skipped = append(s.skipped, stepLabel(step))
			// Пропуск — не «ничего не произошло»: это единственный след того,
			// что задача подошла скиллу частично.
			s.trace(step, "skipped", "условие "+step.When+" ложно", 0, started)
			return false, nil
		}
	}
	switch {
	case step.Set != nil:
		s.set(step.Set.Var, s.expand(step.Set.Value))
		s.trace(step, "ok", "", 0, started)
		return false, nil

	// Ветвление ПОГЛОЩАЕТ сигнал skip: он означает «пропустить остаток ТЕКУЩЕЙ
	// ветки», а не «прервать поток». Иначе необязательный шаг внутри ветки
	// уносил бы с собой весь остаток скилла — для полной остановки есть abort.
	case step.Switch != nil:
		key := strings.TrimSpace(s.lookup(step.Switch.Var))
		branch, ok := step.Switch.Cases[key]
		chosen, outcome := key, "ok"
		if !ok {
			branch = step.Switch.Default
			// Уход в default — частая причина «скилл ответил не то»: значение
			// не совпало ни с одной веткой. В следе это видно сразу.
			chosen = "default (значение " + key + ")"
			if len(branch) == 0 {
				// Пустой default при непустых ветках — не «нечего делать», а
				// провал ветвления: работа, ради которой шаг существовал, не
				// сделана, а поток идёт дальше как ни в чём не бывало. Отказ
				// обязан быть громким, иначе он выглядит успехом (живой случай
				// verdict оказался пуст, ни одна ветка не отработала, и
				// ход ответил служебной переменной).
				outcome = "degraded"
				chosen = "ни одна ветка не подошла (значение " + key + "), default пуст"
			}
		}
		s.trace(step, outcome, chosen, 0, started)
		_, err := s.run(ctx, branch)
		return false, err

	case step.If != nil:
		ok, err := s.eval(step.If.Cond)
		if err != nil {
			return false, err
		}
		if ok {
			s.trace(step, "ok", "then", 0, started)
			_, err = s.run(ctx, step.If.Then)
		} else {
			s.trace(step, "ok", "else", 0, started)
			_, err = s.run(ctx, step.If.Else)
		}
		return false, err

	case step.Delegate != nil:
		return s.delegateStep(ctx, step)

	case step.ForEach != nil:
		return s.forEachStep(ctx, step)

	case step.Parallel != nil:
		return s.parallelStep(ctx, step)

	case step.Exit != nil:
		reason := s.expand(step.Exit.Reason)
		s.trace(step, "exit", reason, 0, started)
		return false, &ExitError{Reason: reason}

	case step.Call != nil:
		return s.callStep(ctx, step)

	case step.Run != nil:
		return s.runStep(ctx, step)
	}
	return false, fmt.Errorf("skill-engine: шаг %q ничего не делает", step.Name)
}

func (s *state) runStep(ctx context.Context, step Step) (bool, error) {
	run := step.Run
	tools := s.toolsFor(run)
	if step.OnServer != "" {
		// Радиус шага сужается до одного сервера: сегодня скилл на пять
		// кластеров отдаёт модели инструменты ВСЕХ пяти, и она может позвать
		// не тот. Сужение делает ошибку невозможной, а не маловероятной.
		only := s.expand(step.OnServer)
		if err := s.allowServer(only); err != nil {
			return s.onError(step, err)
		}
		tools = []string{only}
	}
	req := StepRequest{
		Name:           step.Name,
		Instruction:    s.expand(run.Instruction),
		Model:          run.Model,
		Sampling:       run.Sampling,
		ResponseSchema: run.ResponseSchema,
		OneOf:          run.OneOf,
		Tools:          tools,
		MaxCalls:       run.MaxCalls,
		MaxToolErrors:  run.MaxToolErrors,
	}
	started := time.Now()
	res, err := s.runner.Run(ctx, req)
	if err == nil {
		err = res.Err
	}
	if err != nil {
		s.traceCalls(step, outcomeFor(err), err.Error(), res.Calls, res.CallsFailed, started)
		return s.onError(step, err)
	}
	// Шаг отработал без единого слова — это отказ, а не успех: работа, ради
	// которой шаг существовал, не сделана. Живой класс — gpt-oss кладёт ответ в
	// reasoning_content и оставляет content пустым; шаг записывался как ok, а
	// ход отвечал служебной переменной. Отказ обязан быть громким.
	switch {
	// Причина исполнителя точнее любой выведенной здесь — она первая.
	case res.Note != "":
		s.traceCalls(step, "degraded", res.Note, res.Calls, res.CallsFailed, started)
	case strings.TrimSpace(res.Text) == "":
		s.traceCalls(step, "degraded", "шаг не дал текста", res.Calls, res.CallsFailed, started)
	// Все вызовы отказали — шаг НЕ отработал, даже если текст остался от модели.
	// Иначе ход с семью отвергнутыми вызовами пишется как успешный, и причина
	// ищется в логах пода.
	case res.Calls > 0 && res.CallsFailed == res.Calls:
		s.traceCalls(step, "degraded",
			fmt.Sprintf("все вызовы инструментов отказали (%d)", res.CallsFailed),
			res.Calls, res.CallsFailed, started)
	default:
		s.traceCalls(step, "ok", "", res.Calls, res.CallsFailed, started)
	}
	// Шаг без save_as — финальный ответ хода, а не выброшенная работа. Раньше его
	// результат не сохранялся никуда: скилл отрабатывал, отвечать было нечем, и
	// ход отдавал самую длинную служебную переменную — то есть разбор первого
	// шага.
	target := run.SaveAs
	if target == "" {
		target = AnswerVar
	}
	if target != "" {
		s.set(target, normalizeOneOf(res.Text, run.OneOf))
		s.noteAnswerWriter(target, stepKind(step))
	}
	return false, nil
}

// callStep зовёт инструмент напрямую, без модели.
func (s *state) callStep(ctx context.Context, step Step) (bool, error) {
	call := step.Call
	server, tool, _ := SplitToolRef(call.Tool)
	if step.OnServer != "" {
		// Сервер назван шагом — имя инструмента в call.tool может быть без
		// префикса. Вычисленное имя проходит ту же проверку набора.
		server = s.expand(step.OnServer)
		if _, bare, ok := SplitToolRef(call.Tool); ok {
			tool = bare
		} else {
			tool = call.Tool
		}
	}

	started := time.Now()
	// Отказы ДО вызова тоже оставляют след: без него шаг, отклонённый радиусом,
	// исчезал из трассы бесследно — под политикой continue поток шёл дальше, а
	// в событиях не было ни шага, ни причины: живой промах — два шага молча
	// выпали, и это выглядело как их отсутствие в описании.
	if err := s.allowServer(server); err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	if s.caller == nil {
		err := errors.New("вызов инструментов недоступен")
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}

	out, err := s.caller.CallTool(ctx, server, tool, s.callArgs(call.Args))
	if err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 1, started)
		return s.onError(step, err)
	}
	s.trace(step, "ok", "", 1, started)
	if call.SaveAs != "" {
		s.set(call.SaveAs, out)
		s.noteAnswerWriter(call.SaveAs, stepKind(step))
		// Крупный результат хост кладёт в рабочую память и возвращает превью с
		// хендлом. Сам хендл нужен следующему шагу, чтобы передать данные ПО
		// ССЫЛКЕ: args: {stdin: {from: "{{имя.mem}}"}} — тогда мегабайтный json
		// уходит в код, минуя контекст модели. Без этого программа умеет
		// работать только с тем, что влезает в превью.
		if id := memHandle(out); id != "" {
			s.set(call.SaveAs+MemSuffix, id)
		}
	}
	return false, nil
}

// delegateStep передаёт работу другому скиллу.
func (s *state) delegateStep(ctx context.Context, step Step) (bool, error) {
	d := step.Delegate
	started := time.Now()
	if s.delegate == nil {
		err := errors.New("делегирование скиллам недоступно")
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	out, err := s.delegate.Delegate(ctx, d.Skill, s.expand(d.Task))
	if err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	// Делегирование — спавн субагента, самая дорогая операция хода. Без следа
	// оно невидимо и в событиях, и в прогресс-посте: человек смотрит на
	// «думаю…», пока минуту работает другой скилл.
	s.trace(step, "ok", "скилл "+d.Skill, 1, started)
	if d.SaveAs != "" {
		s.set(d.SaveAs, out)
		s.noteAnswerWriter(d.SaveAs, stepKind(step))
	}
	return false, nil
}

// forEachStep повторяет шаги по коллекции.
//
// Потолок применяется ВСЕГДА: список длиннее — обрабатывается частично, и об
// этом ГОВОРИТСЯ в результате. Молча обработать половину значит отдать ответ,
// который выглядит полным.
func (s *state) forEachStep(ctx context.Context, step Step) (bool, error) {
	fe := step.ForEach
	started := time.Now()
	// `in` резолвится как ЛЮБАЯ другая ссылка — включая поле (`parts.stdout`).
	// Прямой доступ к переменной этого не умел, и цикл по результату exec
	// обходил КОНВЕРТ {"exit_code":0,"stdout":"…"} вместо строк скрипта: одна
	// итерация вместо пяти. Ревью MR на 155 правок вышло пустым, и выглядело
	// это как «модель ничего не нашла» ().
	//
	// Коллекцию берём ЦЕЛИКОМ: в переменной лежит превью, а обрезанный список
	// дал бы частичный обход, который выглядит полным.
	items := splitCollection(s.fullValue(s.lookup(fe.In)))
	total := len(items)
	limit := fe.MaxIterations
	if limit <= 0 {
		limit = DefaultMaxIterations
	}
	if total > limit {
		items = items[:limit]
	}

	// Собирается значение переменной с именем collect: шаг внутри цикла пишет
	// в неё через save_as, и после каждой итерации значение забирается.
	var collected []string
	failed := 0
	for _, item := range items {
		s.set(fe.As, item)
		if _, err := s.run(ctx, fe.Steps); err != nil {
			if errors.Is(err, ErrExit) {
				return false, err
			}
			failed++
			if skipped, oerr := s.onError(step, err); oerr != nil {
				s.trace(step, outcomeFor(oerr), oerr.Error(), len(items), started)
				return skipped, oerr
			}
			continue
		}
		if fe.Collect != "" {
			collected = append(collected, s.vars[fe.Collect])
		}
	}
	if fe.Collect != "" {
		out := strings.Join(nonEmpty(collected), "\n\n")
		if total > limit {
			// Частичная обработка ГОВОРИТСЯ вслух: молча обработанная половина
			// даёт ответ, который выглядит полным.
			out += fmt.Sprintf("\n\n(обработано %d из %d — упёрлись в потолок итераций)", limit, total)
		}
		s.set(fe.Collect, out)
	}
	// Цикл — самый дорогой шаг после делегирования: N итераций, у каждой свои
	// вызовы. Без следа не видно ни сколько их было, ни сколько отвалилось.
	outcome, reason := "ok", fmt.Sprintf("итераций: %d", len(items))
	if total > limit {
		outcome = "degraded"
		reason = fmt.Sprintf("итераций: %d из %d — потолок", limit, total)
	}
	if failed > 0 {
		outcome = "degraded"
		reason += fmt.Sprintf(", отказали: %d", failed)
	}
	s.trace(step, outcome, reason, len(items), started)
	return false, nil
}

// outcomeFor различает отказ по правам и прочий сбой: на первое повторы
// бессмысленны, и в событиях это разные истории.
func outcomeFor(err error) string {
	if errors.Is(err, ErrDenied) {
		return "denied"
	}
	return "error"
}

// splitCollection разбирает значение переменной в список элементов: JSON-массив
// или строки. Второе — наблюдаемый случай: список, добытый инструментом.
func splitCollection(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		var arr []any
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, e := range arr {
				out = append(out, fmt.Sprint(e))
			}
			return out
		}
	}
	return nonEmpty(strings.Split(v, "\n"))
}

func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parallelStep исполняет ветки одновременно.
//
// Каждая получает КОПИЮ переменных и своё состояние: ветки не видят работы
// друг друга. Иначе исход зависел бы от того, кто финишировал первым, — а
// формат существует ради предсказуемости, и недетерминизм тут был бы вшит в
// конструкцию.
//
// Обратно в поток попадают только переменные, произведённые ветками; конфликт
// имён разрешается порядком объявления (позже — сильнее), и это осознанный
// выбор: альтернатива — запрещать одинаковые save_as, что мешает симметричным
// веткам вроде «поищи в двух источниках».
func (s *state) parallelStep(ctx context.Context, step Step) (bool, error) {
	p := step.Parallel
	started := time.Now()
	type result struct {
		vars    map[string]string
		skipped []string
		err     error
	}
	results := make([]result, len(p.Branches))

	var wg sync.WaitGroup
	for i, branch := range p.Branches {
		wg.Add(1)
		go func(i int, branch []Step) {
			defer wg.Done()
			sub := &state{
				vars:        maps.Clone(s.vars),
				seeded:      maps.Clone(s.seeded),
				tools:       s.tools,
				runner:      s.runner,
				caller:      s.caller,
				delegate:    s.delegate,
				onStep:      s.onStep,
				onStepStart: s.onStepStart,
			}
			for k := range sub.vars {
				sub.seeded[k] = true // всё, что было до развилки, — вход ветки
			}
			_, err := sub.run(ctx, branch)
			results[i] = result{vars: sub.produced(), skipped: sub.skipped, err: err}
		}(i, branch)
	}
	wg.Wait()

	var collected []string
	for _, r := range results {
		s.skipped = append(s.skipped, r.skipped...)
		if r.err != nil {
			// Выход из скилла — решение всего хода, а не одной ветки: его
			// нельзя проглотить политикой continue.
			if errors.Is(r.err, ErrExit) {
				return false, r.err
			}
			if skipped, err := s.onError(step, r.err); err != nil {
				return skipped, err
			}
			continue
		}
		for k, v := range r.vars {
			s.set(k, v)
			if strings.TrimSpace(v) != "" {
				collected = append(collected, v)
			}
		}
	}
	if p.Collect != "" {
		s.set(p.Collect, strings.Join(collected, "\n\n"))
		// Пропущенные ветки — отдельной переменной `<collect>.skipped`, тем же
		// приёмом, что и `<save_as>.mem`. Без неё шаг, формулирующий ответ,
		// видит только собранное и не отличает «источник ответил пусто» от
		// «в источник не ходили»: живой случай — скилл поиска написал «в
		// в трекере по теме пусто», не сделав туда ни одного запроса.
		var skippedNames []string
		for _, r := range results {
			skippedNames = append(skippedNames, r.skipped...)
		}
		if len(skippedNames) > 0 {
			s.set(p.Collect+SkippedSuffix, strings.Join(skippedNames, ", "))
		}
	}
	// Ветки трассируются каждая сама (state ветки несёт тот же колбэк), но сам
	// развилочный шаг — нет, и в следе пропадала бы граница: сколько веток
	// пошло и сколько из них отказало. Отказавшая ветка под политикой continue
	// иначе неотличима от не запускавшейся.
	failed, ran := 0, 0
	for _, r := range results {
		if r.err != nil {
			failed++
			continue
		}
		// Ветка, пропущенная по `when`, работы не сделала. Отличать её от
		// отработавшей нужно потому, что развилка, где пропущены ВСЕ ветки,
		// не собрала ничего — а следующий шаг всё равно сформулирует ответ,
		// и он будет выглядеть законченным (живой класс: скилл поиска без единой
		// выбранной пробы отвечал так же уверенно, как с двумя).
		if len(r.skipped) == 0 || len(r.vars) > 0 {
			ran++
		}
	}
	outcome, reason := "ok", fmt.Sprintf("веток: %d", len(p.Branches))
	switch {
	case failed > 0:
		outcome = "degraded"
		reason = fmt.Sprintf("веток: %d, отказали: %d", len(p.Branches), failed)
	case ran == 0 && len(p.Branches) > 0:
		outcome = "degraded"
		reason = fmt.Sprintf("веток: %d, не пошла ни одна — собирать нечего", len(p.Branches))
	}
	s.trace(step, outcome, reason, len(p.Branches), started)
	return false, nil
}

// allowServer держит вызов внутри набора потока.
//
// Без этой проверки описание обошло бы собственный запрет: поток осознанно
// убрал из набора источник, который за месяц дал 53 попытки и ни одного
// успешного чтения, — а прямой вызов дотянулся бы до него в обход. Ограничение,
// которое можно обойти изнутри, ничего не ограничивает.
func (s *state) allowServer(server string) error {
	// builtin — не MCP-сервер, а встроенные инструменты самого приложения. Их
	// радиус задаётся полем builtin_tools скилла и проверяется до исполнения
	// (линтер W7) плюс раздачей реестра исполнителю: набор потока про них
	// ничего не знает и знать не должен.
	if server == BuiltinServer {
		return nil
	}
	if len(s.tools) == 0 {
		// Пустой набор потока — НЕ «всё разрешено». Симметрично шагу, где
		// пустой список значит «инструментов не выдавать вовсе»; обратная
		// трактовка сделала бы скилл без `servers` (поле опционально и в
		// skill-writer, и в схеме) неограниченным — включая write-инструменты.
		return fmt.Errorf("сервер %q: у потока не объявлено ни одного сервера", server)
	}
	for _, t := range s.tools {
		if t == server {
			return nil
		}
	}
	return fmt.Errorf("сервер %q вне набора потока (%s)", server, strings.Join(s.tools, ", "))
}

// toolsFor вычисляет набор инструментов шага: nil → набор потока; заданный —
// ПЕРЕСЕЧЕНИЕ с набором потока (шаг может только сузить).
//
// Расширение запрещено намеренно: иначе шаг мог бы вернуть себе инструмент,
// который поток убрал осознанно, и ограничение перестало бы что-либо значить.
func (s *state) toolsFor(run *Run) []string {
	if run.Tools == nil {
		return s.tools
	}
	want := *run.Tools
	if len(want) == 0 {
		return []string{} // шагу инструменты не выдаются вовсе
	}
	if len(s.tools) == 0 {
		return want
	}
	allowed := make(map[string]bool, len(s.tools))
	for _, t := range s.tools {
		allowed[t] = true
	}
	out := make([]string, 0, len(want))
	for _, t := range want {
		if allowed[t] {
			out = append(out, t)
		}
	}
	return out
}

// onError применяет политику отказа шага.
func (s *state) onError(step Step, err error) (bool, error) {
	// Отказ по правам — самый частый класс в живых скиллах, и реакция на него
	// всегда одна: сказать честно и продолжить с тем, что есть. Обходные пути и
	// повторы бессмысленны, права от этого не появятся.
	policy, saveAs := PolicyAbort, ""
	switch {
	case step.Run != nil:
		policy, saveAs = step.Run.OnError, step.Run.SaveAs
	case step.Call != nil:
		policy, saveAs = step.Call.OnError, step.Call.SaveAs
	case step.Delegate != nil:
		policy, saveAs = step.Delegate.OnError, step.Delegate.SaveAs
	case step.Parallel != nil:
		policy = step.Parallel.OnError
	case step.ForEach != nil:
		// Без этой ветки поле парсилось и молча игнорировалось: цикл падал на
		// первом же отказе, хотя описание просило пометить элемент и идти
		// дальше. Ровно тот класс, ради которого из формата выпилили no_retry.
		policy = step.ForEach.OnError
	}
	if policy == "" {
		policy = PolicyAbort
	}
	switch policy {
	case PolicyContinue:
		if saveAs != "" {
			s.set(saveAs, errText(err))
		}
		return false, nil
	case PolicySkip:
		if saveAs != "" {
			s.set(saveAs, errText(err))
		}
		return true, nil
	default:
		return false, fmt.Errorf("шаг %q: %w", stepLabel(step), err)
	}
}

func errText(err error) string {
	if errors.Is(err, ErrDenied) {
		return "DENIED: " + err.Error()
	}
	return "ERROR: " + err.Error()
}

func stepLabel(step Step) string {
	if step.Name != "" {
		return step.Name
	}
	return "без имени"
}

// noteAnswerWriter запоминает, ЧЕМ записан ответ хода: текстом модели или
// выводом инструмента. Пишущих в переменную ответа может быть несколько (ветки
// switch/if), поэтому запоминается последний — он и остаётся ответом.
func (s *state) noteAnswerWriter(target, kind string) {
	if target == AnswerVar {
		s.answeredBy = kind
	}
}
