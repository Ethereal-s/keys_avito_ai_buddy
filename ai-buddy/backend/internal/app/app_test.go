package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// ---- Юнит-тесты без БД ----

func TestDetectIntent(t *testing.T) {
	cases := map[string]string{
		"Как оформить отгул?": "leave", "хочу в отпуск": "leave", "Что мне делать дальше?": "checklist",
		"мой прогресс": "checklist", "Привет": "greet", "привет, хочу отгул": "leave", "привет всем как дела сегодня": "kb", "Как получить доступ к трекеру?": "kb",
	}
	for in, want := range cases {
		if got := detectIntent(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

func TestSearch(t *testing.T) {
	d, sc := search(seedDocs, "Как получить доступ к трекеру?")
	if d.Title != "Доступы для новичка" || sc < minScore {
		t.Fatalf("got %q score %d", d.Title, sc)
	}
	if _, sc := search(seedDocs, "Какая погода на Марсе"); sc >= minScore {
		t.Fatalf("unexpected match, score %d", sc)
	}
}

func TestParseDates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"отгул 20.11", []string{"2026-11-20"}},
		{"с 20.11.2026 по 22.11.2026", []string{"2026-11-20", "2026-11-22"}},
		{"с 15 ноября по 16 ноября", []string{"2026-11-15", "2026-11-16"}},
		{"31.02 и 5 декабря", []string{"2026-12-05"}},
		{"без дат", []string{}},
	}
	for _, c := range cases {
		var got []string
		for _, d := range parseDates(c.in, fixedNow) {
			got = append(got, d.Format(isoLayout))
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestValidateRange(t *testing.T) {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	bad := [][2]string{{"x", "y"}, {"2026-11-01", "2026-11-01"}, {"2026-11-20", "2026-11-19"}, {"2026-11-20", "2026-11-25"}}
	for _, b := range bad {
		if validateRange(b[0], b[1], today) == nil {
			t.Errorf("%v must be invalid", b)
		}
	}
	if err := validateRange("2026-11-20", "2026-11-22", today); err != nil {
		t.Errorf("3 days must be valid: %v", err)
	}
	if period("2026-11-20", "2026-11-20") != "20.11.2026" || !strings.Contains(period("2026-11-20", "2026-11-21"), "—") {
		t.Error("period format")
	}
}

func TestProgress(t *testing.T) {
	d, n := progress([]Task{{Status: "done"}, {Title: "x", Status: "todo"}})
	if d != 1 || n == nil || n.Title != "x" {
		t.Fatal("progress")
	}
	if _, n := progress([]Task{{Status: "done"}}); n != nil {
		t.Fatal("no next expected")
	}
}

func TestLLMNilDisabled(t *testing.T) {
	var l *LLM
	if l.Enabled() || (&LLM{}).Enabled() {
		t.Fatal("must be disabled without key")
	}
}

// ---- Интеграционные тесты (нужна PostgreSQL: TEST_DATABASE_URL) ----

type env struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
}

func newEnv(t *testing.T) *env {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задан")
	}
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil, nil)
	srv.now = func() time.Time { return fixedNow }
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	srv.absence = &HTTPAbsence{BaseURL: ts.URL + "/mock/absence", HTTP: ts.Client()}
	return &env{t, srv, ts}
}

func (e *env) do(method, path, tok string, body any) (int, []byte) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func obj(b []byte) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (e *env) login(l, p string) string {
	code, b := e.do("POST", "/api/login", "", map[string]string{"login": l, "password": p})
	if code != 200 {
		e.t.Fatalf("login %s: %d %s", l, code, b)
	}
	return obj(b)["token"].(string)
}

func (e *env) chat(tok, text string) map[string]any {
	code, b := e.do("POST", "/api/chat", tok, map[string]string{"text": text})
	if code != 200 {
		e.t.Fatalf("chat %q: %d %s", text, code, b)
	}
	return obj(b)
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("POST", "/api/login", "", map[string]string{"login": "anna", "password": "bad"}); code != 401 {
		t.Errorf("bad password: %d", code)
	}
	if code, _ := e.do("POST", "/api/login", "", map[string]string{"login": "nobody", "password": "x"}); code != 401 {
		t.Errorf("unknown user: %d", code)
	}
	if code, _ := e.do("POST", "/api/login", "", nil); code != 400 {
		t.Errorf("empty body: %d", code)
	}
	if code, _ := e.do("GET", "/api/me", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	tok := e.login("anna", "intern123")
	code, b := e.do("GET", "/api/me", tok, nil)
	if code != 200 || obj(b)["role"] != "intern" {
		t.Errorf("me: %d %s", code, b)
	}
	if code, _ := e.do("GET", "/healthz", "", nil); code != 200 {
		t.Error("healthz")
	}
}

func TestKnowledgeScenario(t *testing.T) {
	e := newEnv(t)
	anna, boris := e.login("anna", "intern123"), e.login("boris", "mentor123")
	r := e.chat(anna, "Как получить доступ к трекеру?")
	if r["intent"] != "kb" || r["source"] != "Доступы для новичка" {
		t.Errorf("kb: %v", r)
	}
	q := "Как проходит экватор и оценка компетенций стажёра?"
	if r := e.chat(anna, q); r["intent"] != "denied" || strings.Contains(r["text"].(string), "форму оценки") {
		t.Errorf("intern must be denied without leaking: %v", r)
	}
	if r := e.chat(boris, q); r["intent"] != "kb" {
		t.Errorf("mentor must get answer: %v", r)
	}
	if r := e.chat(anna, "Какая погода на Марсе"); r["intent"] != "escalate" || !strings.Contains(r["text"].(string), "Борис") {
		t.Errorf("escalate: %v", r)
	}
	if code, _ := e.do("POST", "/api/chat", anna, map[string]string{"text": "  "}); code != 400 {
		t.Errorf("empty chat: %d", code)
	}
	code, b := e.do("GET", "/api/messages", anna, nil)
	var ms []Message
	_ = json.Unmarshal(b, &ms)
	if code != 200 || len(ms) < 6 {
		t.Errorf("history: %d %d", code, len(ms))
	}
}

func TestLeaveScenario(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna", "intern123")
	if r := e.chat(tok, "Как оформить отгул?"); r["intent"] != "leave" || r["action"] != nil || !strings.Contains(r["text"].(string), "ТЕСТОВАЯ") {
		t.Errorf("explain: %v", r)
	}
	if r := e.chat(tok, "Хочу отгул 5.11"); !strings.Contains(r["text"].(string), "прошл") {
		t.Errorf("past: %v", r)
	}
	if r := e.chat(tok, "отгул с 20.11 по 25.11"); !strings.Contains(r["text"].(string), "не более 3") {
		t.Errorf("too long: %v", r)
	}
	r := e.chat(tok, "Хочу отгул с 22.11 по 20.11")
	act, _ := r["action"].(map[string]any)
	if act == nil || act["type"] != "confirm_leave" || act["from"] != "2026-11-20" || act["to"] != "2026-11-22" {
		t.Fatalf("action: %v", r)
	}
	code, b := e.do("POST", "/api/leaves", tok, map[string]string{"from": "2026-11-20", "to": "2026-11-22"})
	if code != 200 || obj(b)["status"] != "pending_approval" || !strings.HasPrefix(obj(b)["external_id"].(string), "ABS-") {
		t.Fatalf("create: %d %s", code, b)
	}
	_, b = e.do("GET", "/api/leaves", tok, nil)
	var ls []Leave
	_ = json.Unmarshal(b, &ls)
	if len(ls) != 1 {
		t.Fatalf("leaves: %s", b)
	}
	_, b = e.do("GET", "/api/messages", tok, nil)
	if !strings.Contains(string(b), "заявка ABS-") {
		t.Error("confirmation message must be saved")
	}
	if code, _ := e.do("POST", "/api/leaves", tok, map[string]string{"from": "2026-11-20", "to": "2026-11-19"}); code != 400 {
		t.Errorf("bad range: %d", code)
	}
	if code, _ := e.do("POST", "/api/leaves", "", map[string]string{}); code != 401 {
		t.Errorf("leaves auth: %d", code)
	}
	// сервис отсутствий недоступен — понятная ошибка 502, без паники
	e.srv.absence = &HTTPAbsence{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	if code, _ := e.do("POST", "/api/leaves", tok, map[string]string{"from": "2026-12-01", "to": "2026-12-01"}); code != 502 {
		t.Errorf("upstream down: %d", code)
	}
}

func TestMockAbsenceValidation(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("POST", "/mock/absence/requests", "", map[string]string{}); code != 400 {
		t.Errorf("mock validation: %d", code)
	}
}

func TestChecklistScenario(t *testing.T) {
	e := newEnv(t)
	anna, boris := e.login("anna", "intern123"), e.login("boris", "mentor123")
	code, b := e.do("GET", "/api/checklist", anna, nil)
	m := obj(b)
	if code != 200 || int(m["total"].(float64)) != len(seedTasks) || int(m["done"].(float64)) != 1 {
		t.Fatalf("checklist: %d %s", code, b)
	}
	next := m["next"].(map[string]any)
	id := int(next["id"].(float64))
	if r := e.chat(anna, "Что мне делать дальше?"); !strings.Contains(r["text"].(string), next["title"].(string)) {
		t.Errorf("next step: %v", r)
	}
	path := "/api/checklist/" + itoa(id)
	if code, _ := e.do("PATCH", path, anna, map[string]string{"status": "done"}); code != 200 {
		t.Errorf("patch: %d", code)
	}
	if code, _ := e.do("PATCH", path, anna, map[string]string{"status": "weird"}); code != 400 {
		t.Errorf("bad status: %d", code)
	}
	if code, _ := e.do("PATCH", "/api/checklist/abc", anna, map[string]string{"status": "done"}); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := e.do("PATCH", path, boris, map[string]string{"status": "done"}); code != 404 {
		t.Errorf("foreign task must be 404: %d", code)
	}
	if r := e.chat(boris, "что дальше"); !strings.Contains(r["text"].(string), "для стажёров") {
		t.Errorf("mentor: %v", r)
	}
	for _, task := range e.tasks(anna) {
		_, _ = e.do("PATCH", "/api/checklist/"+itoa(int(task.ID)), anna, map[string]string{"status": "done"})
	}
	if r := e.chat(anna, "прогресс"); !strings.Contains(r["text"].(string), "Все шаги выполнены") {
		t.Errorf("all done: %v", r)
	}
}

func (e *env) tasks(tok string) []Task {
	_, b := e.do("GET", "/api/checklist", tok, nil)
	var out struct{ Tasks []Task }
	_ = json.Unmarshal(b, &out)
	return out.Tasks
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestMetricsPermissions(t *testing.T) {
	e := newEnv(t)
	anna, boris := e.login("anna", "intern123"), e.login("boris", "mentor123")
	e.chat(anna, "Как получить доступ к трекеру?")
	if code, _ := e.do("GET", "/api/metrics", anna, nil); code != 403 {
		t.Errorf("intern metrics: %d", code)
	}
	code, b := e.do("GET", "/api/metrics", boris, nil)
	ev, _ := obj(b)["events"].(map[string]any)
	if code != 200 || ev["chat_kb"] == nil {
		t.Errorf("mentor metrics: %d %s", code, b)
	}
}

func TestLLMAnswerAndFallback(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna", "intern123")
	ok := true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" ответ от модели "}}]}`))
	}))
	defer fake.Close()
	e.srv.llm = &LLM{BaseURL: fake.URL, Key: "k", Model: "m", HTTP: fake.Client()}
	if r := e.chat(tok, "Как получить доступ к трекеру?"); r["text"] != "ответ от модели" {
		t.Errorf("llm: %v", r)
	}
	ok = false
	if r := e.chat(tok, "Как получить доступ к трекеру?"); !strings.Contains(r["text"].(string), "ТЕСТОВАЯ") {
		t.Errorf("fallback: %v", r)
	}
	if _, err := (&LLM{BaseURL: "http://127.0.0.1:1", Key: "k", HTTP: &http.Client{Timeout: time.Second}}).Answer(context.Background(), "q", Doc{}); err == nil {
		t.Error("expected network error")
	}
}

func TestEmptyLLMAnswer(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer fake.Close()
	if _, err := (&LLM{BaseURL: fake.URL, Key: "k", HTTP: fake.Client()}).Answer(context.Background(), "q", Doc{}); err == nil {
		t.Error("empty answer must be an error")
	}
}

func TestCheckConflictsUnit(t *testing.T) {
	ex := []Leave{{ExternalID: "ABS-1", From: "2026-11-20", To: "2026-11-20", Status: "pending_approval"}, {ExternalID: "ABS-2", From: "2026-11-25", To: "2026-11-25", Status: "rejected"}}
	for _, c := range [][2]string{{"2026-11-20", "2026-11-20"}, {"2026-11-19", "2026-11-20"}, {"2026-11-21", "2026-11-23"}, {"2026-11-18", "2026-11-19"}} {
		if checkConflicts(ex, c[0], c[1]) == nil {
			t.Errorf("%v must conflict", c)
		}
	}
	for _, c := range [][2]string{{"2026-11-21", "2026-11-22"}, {"2026-11-25", "2026-11-25"}, {"2026-12-01", "2026-12-03"}} {
		if err := checkConflicts(ex, c[0], c[1]); err != nil {
			t.Errorf("%v must be ok: %v", c, err)
		}
	}
}

func TestLeaveConflictsAPI(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna", "intern123")
	mk := func(f, to string) int {
		code, _ := e.do("POST", "/api/leaves", tok, map[string]string{"from": f, "to": to})
		return code
	}
	if mk("2026-11-20", "2026-11-20") != 200 {
		t.Fatal("first leave")
	}
	if mk("2026-11-20", "2026-11-20") != 400 || mk("2026-11-20", "2026-11-21") != 400 {
		t.Error("duplicates/overlaps must be rejected")
	}
	if mk("2026-11-21", "2026-11-23") != 400 {
		t.Error("20 + 21-23 = 4 days in a row must be rejected")
	}
	if r := e.chat(tok, "хочу отгул 21.11 по 23.11"); r["action"] != nil || !strings.Contains(r["text"].(string), "подряд") {
		t.Errorf("chat must warn early: %v", r)
	}
	if r := e.chat(tok, "хочу отгул 20.11"); r["action"] != nil || !strings.Contains(r["text"].(string), "уже есть заявка") {
		t.Errorf("chat duplicate: %v", r)
	}
	if mk("2026-11-21", "2026-11-22") != 200 {
		t.Error("3 days in a row must be allowed")
	}
	if mk("2026-11-23", "2026-11-23") != 400 {
		t.Error("4th day in a row must be rejected")
	}
	if mk("2026-11-25", "2026-11-25") != 200 {
		t.Error("separate day must be allowed")
	}
}

func TestApprovals(t *testing.T) {
	e := newEnv(t)
	anna, boris := e.login("anna", "intern123"), e.login("boris", "mentor123")
	_, _ = e.do("POST", "/api/leaves", anna, map[string]string{"from": "2026-11-25", "to": "2026-11-25"})
	if code, _ := e.do("GET", "/api/approvals", anna, nil); code != 403 {
		t.Errorf("intern approvals: %d", code)
	}
	code, b := e.do("GET", "/api/approvals", boris, nil)
	var as []Approval
	_ = json.Unmarshal(b, &as)
	if code != 200 || len(as) != 1 || as[0].Intern != "Анна Тестова" || as[0].Status != "pending_approval" || as[0].Approver != "Борис Наставников" {
		t.Fatalf("approvals: %d %s", code, b)
	}
	path := "/api/approvals/" + itoa(int(as[0].ID))
	if code, _ := e.do("POST", path, anna, map[string]string{"decision": "approve"}); code != 403 {
		t.Errorf("intern must not decide: %d", code)
	}
	if code, _ := e.do("POST", path, boris, map[string]string{"decision": "maybe"}); code != 400 {
		t.Errorf("bad decision: %d", code)
	}
	if code, _ := e.do("POST", "/api/approvals/x", boris, map[string]string{"decision": "approve"}); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	if code, b := e.do("POST", path, boris, map[string]string{"decision": "approve"}); code != 200 || obj(b)["status"] != "approved" {
		t.Errorf("approve: %d %s", code, b)
	}
	if code, _ := e.do("POST", path, boris, map[string]string{"decision": "reject"}); code != 404 {
		t.Errorf("repeat decision: %d", code)
	}
	_, b = e.do("GET", "/api/leaves", anna, nil)
	_, mb := e.do("GET", "/api/messages", anna, nil)
	if !strings.Contains(string(b), "approved") || !strings.Contains(string(mb), "согласована") {
		t.Error("intern must see the decision")
	}
	// отклонённая заявка освобождает даты
	_, _ = e.do("POST", "/api/leaves", anna, map[string]string{"from": "2026-12-01", "to": "2026-12-01"})
	_, b = e.do("GET", "/api/approvals", boris, nil)
	_ = json.Unmarshal(b, &as)
	if code, _ := e.do("POST", "/api/approvals/"+itoa(int(as[0].ID)), boris, map[string]string{"decision": "reject"}); code != 200 {
		t.Fatalf("reject: %d", code)
	}
	if code, _ := e.do("POST", "/api/leaves", anna, map[string]string{"from": "2026-12-01", "to": "2026-12-01"}); code != 200 {
		t.Errorf("rejected dates must be free again: %d", code)
	}
}
