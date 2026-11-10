package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type authed func(http.ResponseWriter, *http.Request, User)

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("GET /api/me", s.auth(func(w http.ResponseWriter, _ *http.Request, u User) { writeJSON(w, 200, u) }))
	m.HandleFunc("GET /api/messages", s.auth(s.messages))
	m.HandleFunc("POST /api/chat", s.auth(s.chat))
	m.HandleFunc("GET /api/checklist", s.auth(s.checklist))
	m.HandleFunc("PATCH /api/checklist/{id}", s.auth(s.setTask))
	m.HandleFunc("GET /api/leaves", s.auth(s.leaves))
	m.HandleFunc("POST /api/leaves", s.auth(s.createLeave))
	m.HandleFunc("GET /api/approvals", s.auth(s.approvals))
	m.HandleFunc("POST /api/approvals/{id}", s.auth(s.decide))
	m.HandleFunc("GET /api/metrics", s.auth(s.metrics))
	m.HandleFunc("POST /mock/absence/requests", s.mockAbsence)
	return m
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var ue userError
	switch {
	case errors.As(err, &ue):
		writeJSON(w, 400, map[string]string{"error": ue.Error()})
	case errors.Is(err, ErrForbidden):
		writeJSON(w, 403, map[string]string{"error": "недостаточно прав"})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, 404, map[string]string{"error": "не найдено"})
	case errors.Is(err, ErrUpstream):
		log.Printf("upstream: %v", err)
		writeJSON(w, 502, map[string]string{"error": "сервис отсутствий временно недоступен, попробуйте позже"})
	default:
		log.Printf("internal: %v", err)
		writeJSON(w, 500, map[string]string{"error": "внутренняя ошибка"})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, userError("Некорректный запрос"))
		return false
	}
	return true
}

func (s *Server) auth(h authed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, err := s.st.UserByToken(r.Context(), tok)
		if err != nil {
			writeJSON(w, 401, map[string]string{"error": "требуется вход"})
			return
		}
		h(w, r, u)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	tok, u, err := s.st.Login(r.Context(), in.Login, in.Password)
	if errors.Is(err, ErrAuth) {
		writeJSON(w, 401, map[string]string{"error": "неверный логин или пароль"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok, "user": u})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, u User) {
	ms, err := s.st.Messages(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ms)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	rep, err := s.Chat(r.Context(), u, in.Text)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) checklist(w http.ResponseWriter, r *http.Request, u User) {
	ts, err := s.st.Tasks(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	done, next := progress(ts)
	writeJSON(w, 200, map[string]any{"tasks": ts, "done": done, "total": len(ts), "next": next})
}

func (s *Server) setTask(w http.ResponseWriter, r *http.Request, u User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, userError("Некорректный id"))
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Status != "todo" && in.Status != "in_progress" && in.Status != "done" {
		fail(w, userError("Недопустимый статус"))
		return
	}
	if err := s.st.SetTaskStatus(r.Context(), u.ID, id, in.Status); err != nil {
		fail(w, err)
		return
	}
	if err := s.st.AddEvent(r.Context(), u.ID, "task_"+in.Status); err != nil {
		log.Printf("event: %v", err)
	}
	writeJSON(w, 200, map[string]string{"status": in.Status})
}

func (s *Server) leaves(w http.ResponseWriter, r *http.Request, u User) {
	ls, err := s.st.Leaves(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ls)
}

func (s *Server) createLeave(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	l, err := s.CreateLeave(r.Context(), u, in.From, in.To)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, l)
}

// Метрики пользы доступны только наставнику (проверка роли на сервере).
func (s *Server) metrics(w http.ResponseWriter, r *http.Request, u User) {
	if u.Role != "mentor" {
		writeJSON(w, 403, map[string]string{"error": "недостаточно прав"})
		return
	}
	m, err := s.st.Metrics(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, m)
}

// Имитация внешнего сервиса отсутствий (в проде — корпоративный API/MCP).
func (s *Server) mockAbsence(w http.ResponseWriter, r *http.Request) {
	var in struct{ Employee, From, To, Type string }
	if !decode(w, r, &in) {
		return
	}
	if in.Employee == "" || in.From == "" || in.To == "" {
		fail(w, userError("employee, from, to обязательны"))
		return
	}
	writeJSON(w, http.StatusCreated, AbsenceResult{
		ExternalID: fmt.Sprintf("ABS-%04d", s.seq.Add(1)),
		Status:     "pending_approval",
		Approver:   "Руководитель команды (тест)",
	})
}

func (s *Server) approvals(w http.ResponseWriter, r *http.Request, u User) {
	if u.Role != "mentor" {
		fail(w, ErrForbidden)
		return
	}
	as, err := s.st.Approvals(r.Context(), u.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, as)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request, u User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, userError("Некорректный id"))
		return
	}
	var in struct {
		Decision string `json:"decision"`
	}
	if !decode(w, r, &in) {
		return
	}
	l, err := s.DecideLeave(r.Context(), u, id, in.Decision)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, l)
}
