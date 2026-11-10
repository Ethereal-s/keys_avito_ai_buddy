package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

type userError string

func (e userError) Error() string { return string(e) }

var (
	ErrUpstream  = errors.New("upstream error")
	ErrForbidden = errors.New("forbidden")
)

const minScore = 2

var roleRank = map[string]int{"intern": 1, "mentor": 2}

type Server struct {
	st      *Store
	absence AbsenceClient
	llm     *LLM
	now     func() time.Time
	seq     atomic.Int64
	leaveMu sync.Mutex
}

func New(st *Store, abs AbsenceClient, llm *LLM) *Server {
	return &Server{st: st, absence: abs, llm: llm, now: time.Now}
}

type Action struct {
	Type string `json:"type"`
	From string `json:"from"`
	To   string `json:"to"`
}
type ChatReply struct {
	Text   string  `json:"text"`
	Intent string  `json:"intent"`
	Source string  `json:"source,omitempty"`
	Action *Action `json:"action,omitempty"`
}

func (s *Server) today() time.Time {
	n := s.now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func detectIntent(t string) string {
	l := strings.ToLower(t)
	for _, k := range []string{"отгул", "отпуск", "отсутств"} {
		if strings.Contains(l, k) {
			return "leave"
		}
	}
	for _, k := range []string{"дальше", "следующ", "мои задач", "чек-лист", "чеклист", "прогресс", "этап"} {
		if strings.Contains(l, k) {
			return "checklist"
		}
	}
	if len(strings.Fields(l)) <= 3 {
		for _, k := range []string{"привет", "здравств", "добрый", "доброе", "спасибо"} {
			if strings.HasPrefix(l, k) {
				return "greet"
			}
		}
	}
	return "kb"
}

const capabilities = "Я помогу с вопросами о команде и доступах, оформлением отгула и этапами адаптации."

// Chat — единая точка входа: определяет сценарий, сохраняет историю и события.
func (s *Server) Chat(ctx context.Context, u User, text string) (ChatReply, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return ChatReply{}, userError("Введите вопрос")
	}
	if err := s.st.AddMessage(ctx, u.ID, "user", text); err != nil {
		return ChatReply{}, err
	}
	var rep ChatReply
	var err error
	switch detectIntent(text) {
	case "leave":
		rep, err = s.handleLeave(ctx, u, text)
	case "checklist":
		rep, err = s.handleChecklist(ctx, u)
	case "greet":
		rep = ChatReply{Intent: "greet", Text: "Привет, " + u.Name + "! " + capabilities + " Попробуйте: «Что мне делать дальше?»"}
	default:
		rep, err = s.handleKB(ctx, u, text)
	}
	if err != nil {
		return ChatReply{}, err
	}
	if err := s.st.AddMessage(ctx, u.ID, "assistant", rep.Text); err != nil {
		return ChatReply{}, err
	}
	if err := s.st.AddEvent(ctx, u.ID, "chat_"+rep.Intent); err != nil {
		log.Printf("event: %v", err)
	}
	return rep, nil
}

// ---- Сценарий 1: вопросы о команде и инструментах (с учётом прав) ----

func (s *Server) handleKB(ctx context.Context, u User, q string) (ChatReply, error) {
	docs, err := s.st.Docs(ctx)
	if err != nil {
		return ChatReply{}, err
	}
	mentor := u.Mentor
	if mentor == "" {
		mentor = "наставника"
	}
	d, sc := search(docs, q)
	if sc < minScore {
		return ChatReply{Intent: "escalate", Text: fmt.Sprintf("В базе знаний ответа не нашёл. Лучше уточнить у наставника: %s. "+capabilities, mentor)}, nil
	}
	if roleRank[u.Role] < roleRank[d.MinRole] {
		return ChatReply{Intent: "denied", Text: fmt.Sprintf("Эта информация недоступна для вашей роли, поэтому я её не раскрываю. Если она нужна для задачи — уточните у наставника: %s.", mentor)}, nil
	}
	ans := d.Body
	if s.llm.Enabled() {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if t, err := s.llm.Answer(c, q, d); err != nil {
			log.Printf("llm: %v", err)
		} else {
			ans = t
		}
	}
	return ChatReply{Intent: "kb", Source: d.Title, Text: ans}, nil
}

func stems(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(w)
		if len(r) < 4 {
			continue
		}
		if len(r) > 5 {
			r = r[:5]
		}
		out = append(out, string(r))
	}
	return out
}

func toSet(ws []string) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// search — простой лексический поиск по основам слов: заголовок и теги весят 2, текст — 1.
func search(docs []Doc, q string) (Doc, int) {
	qs := toSet(stems(q))
	best, bs := Doc{}, 0
	for _, d := range docs {
		head, body := toSet(stems(d.Title+" "+d.Tags)), toSet(stems(d.Body))
		sc := 0
		for w := range qs {
			if head[w] {
				sc += 2
			} else if body[w] {
				sc++
			}
		}
		if sc > bs {
			best, bs = d, sc
		}
	}
	return best, bs
}

// ---- Сценарий 2: отгул через сервис отсутствий (действие с подтверждением) ----

var months = map[string]int{"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12}
var (
	reNum  = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})(?:\.(\d{4}))?`)
	reWord = regexp.MustCompile(`(\d{1,2})\s+(января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)`)
)

func parseDates(text string, now time.Time) []time.Time {
	type hit struct {
		pos int
		t   time.Time
	}
	var hits []hit
	l := strings.ToLower(text)
	add := func(pos, d, m, y int) {
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if t.Day() == d && int(t.Month()) == m {
			hits = append(hits, hit{pos, t})
		}
	}
	for _, m := range reNum.FindAllStringSubmatchIndex(l, -1) {
		d, _ := strconv.Atoi(l[m[2]:m[3]])
		mo, _ := strconv.Atoi(l[m[4]:m[5]])
		y := now.Year()
		if m[6] >= 0 {
			y, _ = strconv.Atoi(l[m[6]:m[7]])
		}
		add(m[0], d, mo, y)
	}
	for _, m := range reWord.FindAllStringSubmatchIndex(l, -1) {
		d, _ := strconv.Atoi(l[m[2]:m[3]])
		add(m[0], d, months[l[m[4]:m[5]]], now.Year())
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := []time.Time{}
	for _, h := range hits {
		out = append(out, h.t)
	}
	return out
}

const isoLayout = "2006-01-02"

func validateRange(from, to string, today time.Time) error {
	f, e1 := time.Parse(isoLayout, from)
	t, e2 := time.Parse(isoLayout, to)
	switch {
	case e1 != nil || e2 != nil:
		return userError("Некорректные даты, ожидается формат ГГГГ-ММ-ДД")
	case f.Before(today):
		return userError("Дата начала уже в прошлом — укажите будущие даты")
	case t.Before(f):
		return userError("Дата окончания раньше даты начала")
	case t.Sub(f) > 2*24*time.Hour:
		return userError("Отгул — не более 3 дней подряд (тестовое правило)")
	}
	return nil
}

func period(from, to string) string {
	f, _ := time.Parse(isoLayout, from)
	t, _ := time.Parse(isoLayout, to)
	if from == to {
		return f.Format("02.01.2006")
	}
	return f.Format("02.01.2006") + " — " + t.Format("02.01.2006")
}

func (s *Server) handleLeave(ctx context.Context, u User, text string) (ChatReply, error) {
	ds := parseDates(text, s.today())
	if len(ds) == 0 {
		hint := "Напишите даты отгула, например: «хочу отгул 20.11»."
		docs, err := s.st.Docs(ctx)
		if err != nil {
			return ChatReply{}, err
		}
		if d, sc := search(docs, "отгул оформить"); sc >= minScore {
			return ChatReply{Intent: "leave", Source: d.Title, Text: d.Body + "\n\n" + hint}, nil
		}
		return ChatReply{Intent: "leave", Text: hint}, nil
	}
	from, to := ds[0], ds[len(ds)-1]
	if to.Before(from) {
		from, to = to, from
	}
	f, t := from.Format(isoLayout), to.Format(isoLayout)
	if err := validateRange(f, t, s.today()); err != nil {
		return ChatReply{Intent: "leave", Text: err.Error()}, nil
	}
	existing, err := s.st.Leaves(ctx, u.ID)
	if err != nil {
		return ChatReply{}, err
	}
	if err := checkConflicts(existing, f, t); err != nil {
		return ChatReply{Intent: "leave", Text: err.Error()}, nil
	}
	return ChatReply{Intent: "leave", Action: &Action{Type: "confirm_leave", From: f, To: t},
		Text: fmt.Sprintf("Могу создать заявку на отгул (%s) в системе отсутствий. Подтвердите — и я отправлю её на согласование руководителю.", period(f, t))}, nil
}

// CreateLeave вызывает (имитацию) сервиса отсутствий, сохраняет и показывает результат.
func (s *Server) CreateLeave(ctx context.Context, u User, from, to string) (Leave, error) {
	if err := validateRange(from, to, s.today()); err != nil {
		return Leave{}, err
	}
	s.leaveMu.Lock() // проверка конфликтов и создание — атомарно (один инстанс сервиса)
	defer s.leaveMu.Unlock()
	existing, err := s.st.Leaves(ctx, u.ID)
	if err != nil {
		return Leave{}, err
	}
	if err := checkConflicts(existing, from, to); err != nil {
		return Leave{}, err
	}
	res, err := s.absence.Create(ctx, u.Login, from, to)
	if err != nil {
		return Leave{}, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	if u.Mentor != "" {
		res.Approver = u.Mentor // согласует наставник стажёра
	}
	l, err := s.st.AddLeave(ctx, u.ID, Leave{From: from, To: to, Status: res.Status, ExternalID: res.ExternalID, Approver: res.Approver})
	if err != nil {
		return Leave{}, err
	}
	msg := fmt.Sprintf("Готово: заявка %s на отгул (%s) создана, статус — «%s», согласующий — %s.", l.ExternalID, period(from, to), l.Status, l.Approver)
	if err := s.st.AddMessage(ctx, u.ID, "assistant", msg); err != nil {
		return Leave{}, err
	}
	if err := s.st.AddEvent(ctx, u.ID, "leave_created"); err != nil {
		log.Printf("event: %v", err)
	}
	return l, nil
}

// ---- Сценарий 3: этапы адаптации, статусы и следующий шаг ----

func progress(ts []Task) (int, *Task) {
	done := 0
	var next *Task
	for i := range ts {
		if ts[i].Status == "done" {
			done++
		} else if next == nil {
			next = &ts[i]
		}
	}
	return done, next
}

func (s *Server) handleChecklist(ctx context.Context, u User) (ChatReply, error) {
	ts, err := s.st.Tasks(ctx, u.ID)
	if err != nil {
		return ChatReply{}, err
	}
	if len(ts) == 0 {
		return ChatReply{Intent: "checklist", Text: "Чек-лист адаптации создаётся для стажёров, у вас его нет."}, nil
	}
	done, next := progress(ts)
	text := fmt.Sprintf("Выполнено %d из %d шагов.", done, len(ts))
	if next != nil {
		text += fmt.Sprintf(" Следующий шаг — «%s» (этап: %s).", next.Title, next.Stage)
	} else {
		text += " Все шаги выполнены — обсудите итоги с наставником."
	}
	return ChatReply{Intent: "checklist", Text: text}, nil
}

// checkConflicts: нет пересечений с активными заявками и не более 3 дней подряд
// с учётом соседних заявок (отклонённые не учитываются).
func checkConflicts(existing []Leave, from, to string) error {
	f, _ := time.Parse(isoLayout, from)
	t, _ := time.Parse(isoLayout, to)
	days := map[string]bool{}
	for _, l := range existing {
		if l.Status == "rejected" {
			continue
		}
		lf, e1 := time.Parse(isoLayout, l.From)
		lt, e2 := time.Parse(isoLayout, l.To)
		if e1 != nil || e2 != nil {
			continue
		}
		if !f.After(lt) && !t.Before(lf) {
			return userError(fmt.Sprintf("На эти даты уже есть заявка %s (%s)", l.ExternalID, period(l.From, l.To)))
		}
		for d := lf; !d.After(lt); d = d.AddDate(0, 0, 1) {
			days[d.Format(isoLayout)] = true
		}
	}
	start, end := f, t
	for days[start.AddDate(0, 0, -1).Format(isoLayout)] {
		start = start.AddDate(0, 0, -1)
	}
	for days[end.AddDate(0, 0, 1).Format(isoLayout)] {
		end = end.AddDate(0, 0, 1)
	}
	if n := int(end.Sub(start).Hours()/24) + 1; n > 3 {
		return userError(fmt.Sprintf("Вместе с соседними заявками получится %d дн. подряд (%s), а максимум — 3 (тестовое правило)", n, period(start.Format(isoLayout), end.Format(isoLayout))))
	}
	return nil
}

// DecideLeave — согласование или отклонение заявки наставником; стажёр получает сообщение в чат.
func (s *Server) DecideLeave(ctx context.Context, u User, id int64, decision string) (Leave, error) {
	if u.Role != "mentor" {
		return Leave{}, ErrForbidden
	}
	status, word := "approved", "согласована"
	switch decision {
	case "approve":
	case "reject":
		status, word = "rejected", "отклонена"
	default:
		return Leave{}, userError("decision: approve или reject")
	}
	l, uid, err := s.st.DecideLeave(ctx, u.Name, id, status)
	if err != nil {
		return Leave{}, err
	}
	msg := fmt.Sprintf("Заявка %s на отгул (%s) %s: %s.", l.ExternalID, period(l.From, l.To), word, u.Name)
	if err := s.st.AddMessage(ctx, uid, "assistant", msg); err != nil {
		return Leave{}, err
	}
	if err := s.st.AddEvent(ctx, u.ID, "leave_"+status); err != nil {
		log.Printf("event: %v", err)
	}
	return l, nil
}
