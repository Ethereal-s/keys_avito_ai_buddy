package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	_ "github.com/lib/pq"
)

var (
	ErrNotFound = errors.New("not found")
	ErrAuth     = errors.New("invalid credentials")
)

type User struct {
	ID     int64  `json:"id"`
	Login  string `json:"login"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Mentor string `json:"mentor"`
}
type Doc struct {
	ID                         int64
	Title, Body, Tags, MinRole string
}
type Task struct {
	ID     int64  `json:"id"`
	Stage  string `json:"stage"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type Leave struct {
	ID         int64  `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Status     string `json:"status"`
	ExternalID string `json:"external_id"`
	Approver   string `json:"approver"`
}
type Metrics struct {
	Events     map[string]int `json:"events"`
	TasksDone  int            `json:"tasks_done"`
	TasksTotal int            `json:"tasks_total"`
	Interns    int            `json:"interns"`
}

const schema = `
CREATE TABLE IF NOT EXISTS users(id SERIAL PRIMARY KEY, login TEXT UNIQUE NOT NULL, name TEXT NOT NULL, role TEXT NOT NULL, mentor TEXT NOT NULL DEFAULT '', pass_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, user_id INT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS docs(id SERIAL PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, tags TEXT NOT NULL DEFAULT '', min_role TEXT NOT NULL DEFAULT 'intern');
CREATE TABLE IF NOT EXISTS tasks(id SERIAL PRIMARY KEY, user_id INT NOT NULL REFERENCES users(id), stage TEXT NOT NULL, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'todo', pos INT NOT NULL);
CREATE TABLE IF NOT EXISTS messages(id SERIAL PRIMARY KEY, user_id INT NOT NULL REFERENCES users(id), role TEXT NOT NULL, text TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS leaves(id SERIAL PRIMARY KEY, user_id INT NOT NULL REFERENCES users(id), date_from TEXT NOT NULL, date_to TEXT NOT NULL, status TEXT NOT NULL, external_id TEXT NOT NULL, approver TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS events(id SERIAL PRIMARY KEY, user_id INT NOT NULL, kind TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());`

type Store struct{ db *sql.DB }

// Open подключается к PostgreSQL, ожидая готовности БД до 30 секунд.
func Open(url string) (*Store, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	var last error
	for i := 0; i < 30; i++ {
		if last = db.Ping(); last == nil {
			return &Store{db}, nil
		}
		time.Sleep(time.Second)
	}
	return nil, last
}

func (s *Store) Init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	return s.Seed(ctx)
}

// Reset очищает все таблицы и заново загружает тестовые данные.
func (s *Store) Reset(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `TRUNCATE users, sessions, docs, tasks, messages, leaves, events RESTART IDENTITY CASCADE`); err != nil {
		return err
	}
	return s.Seed(ctx)
}

func hashPass(p string) string {
	h := sha256.Sum256([]byte("aibuddy:" + p))
	return hex.EncodeToString(h[:])
}

func (s *Store) Seed(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, u := range seedUsers {
		var id int64
		err := s.db.QueryRowContext(ctx, `INSERT INTO users(login,name,role,mentor,pass_hash) VALUES($1,$2,$3,$4,$5) RETURNING id`,
			u.Login, u.Name, u.Role, u.Mentor, hashPass(u.Pass)).Scan(&id)
		if err != nil {
			return err
		}
		if u.Role != "intern" {
			continue
		}
		for i, t := range seedTasks {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO tasks(user_id,stage,title,status,pos) VALUES($1,$2,$3,$4,$5)`, id, t.Stage, t.Title, t.Status, i); err != nil {
				return err
			}
		}
	}
	for _, d := range seedDocs {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO docs(title,body,tags,min_role) VALUES($1,$2,$3,$4)`, d.Title, d.Body, d.Tags, d.MinRole); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Login(ctx context.Context, login, pass string) (string, User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id,login,name,role,mentor,pass_hash FROM users WHERE login=$1`, login).
		Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.Mentor, &hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hash != hashPass(pass)) {
		return "", User{}, ErrAuth
	}
	if err != nil {
		return "", User{}, err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", User{}, err
	}
	tok := hex.EncodeToString(b)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(token,user_id) VALUES($1,$2)`, tok, u.ID)
	return tok, u, err
}

func (s *Store) UserByToken(ctx context.Context, tok string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.login,u.name,u.role,u.mentor FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token=$1`, tok).
		Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.Mentor)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrAuth
	}
	return u, err
}

func (s *Store) Docs(ctx context.Context) ([]Doc, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,body,tags,min_role FROM docs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Doc{}
	for rows.Next() {
		var d Doc
		if err := rows.Scan(&d.ID, &d.Title, &d.Body, &d.Tags, &d.MinRole); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Tasks(ctx context.Context, uid int64) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,stage,title,status FROM tasks WHERE user_id=$1 ORDER BY pos`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Stage, &t.Title, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SetTaskStatus(ctx context.Context, uid, id int64, status string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=$1 WHERE id=$2 AND user_id=$3`, status, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddMessage(ctx context.Context, uid int64, role, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages(user_id,role,text) VALUES($1,$2,$3)`, uid, role, text)
	return err
}

func (s *Store) Messages(ctx context.Context, uid int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role,text FROM (SELECT id,role,text FROM messages WHERE user_id=$1 ORDER BY id DESC LIMIT 50) t ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Text); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddLeave(ctx context.Context, uid int64, l Leave) (Leave, error) {
	err := s.db.QueryRowContext(ctx, `INSERT INTO leaves(user_id,date_from,date_to,status,external_id,approver) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		uid, l.From, l.To, l.Status, l.ExternalID, l.Approver).Scan(&l.ID)
	return l, err
}

func (s *Store) Leaves(ctx context.Context, uid int64) ([]Leave, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,date_from,date_to,status,external_id,approver FROM leaves WHERE user_id=$1 ORDER BY id DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Leave{}
	for rows.Next() {
		var l Leave
		if err := rows.Scan(&l.ID, &l.From, &l.To, &l.Status, &l.ExternalID, &l.Approver); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) AddEvent(ctx context.Context, uid int64, kind string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events(user_id,kind) VALUES($1,$2)`, uid, kind)
	return err
}

func (s *Store) Metrics(ctx context.Context) (Metrics, error) {
	m := Metrics{Events: map[string]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,count(*) FROM events GROUP BY kind`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return m, err
		}
		m.Events[k] = c
	}
	if err := rows.Err(); err != nil {
		return m, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status='done'), count(*), count(DISTINCT user_id) FROM tasks`).
		Scan(&m.TasksDone, &m.TasksTotal, &m.Interns)
	return m, err
}

type Approval struct {
	Leave
	Intern string `json:"intern"`
}

// Approvals — заявки стажёров данного наставника (сначала ожидающие).
func (s *Store) Approvals(ctx context.Context, mentor string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.date_from,l.date_to,l.status,l.external_id,l.approver,u.name
		FROM leaves l JOIN users u ON u.id=l.user_id WHERE u.mentor=$1
		ORDER BY (l.status='pending_approval') DESC, l.id DESC`, mentor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.From, &a.To, &a.Status, &a.ExternalID, &a.Approver, &a.Intern); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideLeave меняет статус только ожидающей заявки стажёра этого наставника.
func (s *Store) DecideLeave(ctx context.Context, mentor string, id int64, status string) (Leave, int64, error) {
	var l Leave
	var uid int64
	err := s.db.QueryRowContext(ctx, `UPDATE leaves l SET status=$1 FROM users u
		WHERE l.id=$2 AND u.id=l.user_id AND u.mentor=$3 AND l.status='pending_approval'
		RETURNING l.id,l.user_id,l.date_from,l.date_to,l.status,l.external_id,l.approver`, status, id, mentor).
		Scan(&l.ID, &uid, &l.From, &l.To, &l.Status, &l.ExternalID, &l.Approver)
	if errors.Is(err, sql.ErrNoRows) {
		return Leave{}, 0, ErrNotFound
	}
	return l, uid, err
}
