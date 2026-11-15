// Package store keeps what outlives a run: the rules someone wrote, the window's
// shape, and the last sessions.
//
// 存什么、不存什么是这里唯一的判断。抓到的流量可能一次几十万条，全存下来库会长
// 到几个 G，而那不是用户要的 —— 重新启动后想看到的，是「上次我调到哪个接口、留
// 了哪些规则、窗口多大」，不是三天前的每一条 304。
//
// 所以：规则全存（那是人写的，丢了要重写），窗口状态全存（那是习惯），流量只留最
// 近的一小段（那是痕迹，用来接着上次继续看），并且按天和条数两头限。
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"github.com/jlvihv/qingqiuxia/internal/rule"
)

// DB is the persistent store.
type DB struct {
	db *sqlite.DB
}

// Open opens (and creates) the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	s := &DB{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *DB) Close() error { return s.db.Close() }

// schema is the shape of the store. Each statement runs once; the version row
// says which of them have.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS rules (
		id      INTEGER PRIMARY KEY,
		spec    TEXT NOT NULL,
		updated INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		id       INTEGER PRIMARY KEY,
		started  INTEGER NOT NULL,
		method   TEXT NOT NULL,
		url      TEXT NOT NULL,
		host     TEXT NOT NULL,
		status   INTEGER NOT NULL,
		phase    TEXT NOT NULL,
		source   TEXT NOT NULL,
		duration REAL NOT NULL,
		bytes    INTEGER NOT NULL,
		notes    TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_started ON sessions (started DESC)`,
}

func (s *DB) migrate(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := s.db.Execute(ctx, stmt); err != nil {
			return fmt.Errorf("建表: %w", err)
		}
	}
	return nil
}

// SaveRules replaces the stored rules.
//
// 整体替换而不是增量，因为规则的意义在于顺序：一条一条改，中途崩掉就会留下一个
// 半新半旧的顺序，而那是最难查的一类问题。
func (s *DB) SaveRules(ctx context.Context, specs []rule.Spec) error {
	now := time.Now().Unix()
	stmts := []sqlite.Statement{{SQL: `DELETE FROM rules`}}
	for _, spec := range specs {
		data, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		stmts = append(stmts, sqlite.Statement{
			SQL:  `INSERT INTO rules (id, spec, updated) VALUES (?, ?, ?)`,
			Args: []any{int64(spec.ID), string(data), now},
		})
	}
	_, err := s.db.Transaction(ctx, stmts)
	return err
}

// LoadRules returns the stored rules, in the order they were saved.
func (s *DB) LoadRules(ctx context.Context) ([]rule.Spec, error) {
	rows, err := s.db.Query(ctx, `SELECT spec FROM rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]rule.Spec, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		text, ok := row[0].(string)
		if !ok {
			continue
		}
		var spec rule.Spec
		if err := json.Unmarshal([]byte(text), &spec); err != nil {
			// 一条规则坏了不该让整个启动失败：跳过它，其余的照常加载。
			continue
		}
		out = append(out, spec)
	}
	return out, nil
}

// Window is the window's shape, which is a habit worth keeping.
type Window struct {
	Width, Height float32
	// SidebarWidth and MainWidth are the two splits' first panes.
	SidebarWidth, MainWidth float32
	// Detail is the tab the detail panel had open.
	Detail int
	// Search is what was in the filter box.
	Search string
}

// SaveWindow stores the window's shape.
func (s *DB) SaveWindow(ctx context.Context, w Window) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = s.db.Execute(ctx,
		`INSERT INTO meta (key, value) VALUES ('window', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		string(data))
	return err
}

// LoadWindow returns the window's shape, or the zero value if none was saved.
func (s *DB) LoadWindow(ctx context.Context) (Window, bool, error) {
	rows, err := s.db.Query(ctx, `SELECT value FROM meta WHERE key = 'window'`)
	if err != nil {
		return Window{}, false, err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return Window{}, false, nil
	}
	text, ok := rows.Values[0][0].(string)
	if !ok {
		return Window{}, false, nil
	}
	var w Window
	if err := json.Unmarshal([]byte(text), &w); err != nil {
		return Window{}, false, nil
	}
	return w, true, nil
}

// Record is one session as the store keeps it.
type Record struct {
	ID       uint64
	Started  time.Time
	Method   string
	URL      string
	Host     string
	Status   int
	Phase    string
	Source   string
	Duration float64
	Bytes    int64
	Notes    string
}

// SaveSessions writes a batch of sessions, replacing any with the same id.
func (s *DB) SaveSessions(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	stmts := make([]sqlite.Statement, 0, len(records))
	for _, r := range records {
		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO sessions
				(id, started, method, url, host, status, phase, source, duration, bytes, notes)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					status = excluded.status,
					phase = excluded.phase,
					duration = excluded.duration,
					bytes = excluded.bytes,
					notes = excluded.notes`,
			Args: []any{
				int64(r.ID), r.Started.Unix(), r.Method, r.URL, r.Host,
				r.Status, r.Phase, r.Source, r.Duration, r.Bytes, r.Notes,
			},
		})
	}
	_, err := s.db.Transaction(ctx, stmts)
	return err
}

// LoadSessions returns the most recent sessions, newest first, up to limit.
func (s *DB) LoadSessions(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, started, method, url, host, status, phase, source, duration, bytes,
		       COALESCE(notes, '')
		FROM sessions ORDER BY started DESC, id DESC LIMIT ?`, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, recordOf(row))
	}
	return out, nil
}

// recordOf turns one row into a Record.
func recordOf(row []any) Record {
	get := func(i int) any {
		if i < len(row) {
			return row[i]
		}
		return nil
	}
	num := func(i int) int64 {
		switch v := get(i).(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		}
		return 0
	}
	str := func(i int) string {
		if v, ok := get(i).(string); ok {
			return v
		}
		return ""
	}
	real := func(i int) float64 {
		switch v := get(i).(type) {
		case float64:
			return v
		case int64:
			return float64(v)
		}
		return 0
	}
	return Record{
		ID:       uint64(num(0)),
		Started:  time.Unix(num(1), 0),
		Method:   str(2),
		URL:      str(3),
		Host:     str(4),
		Status:   int(num(5)),
		Phase:    str(6),
		Source:   str(7),
		Duration: real(8),
		Bytes:    num(9),
		Notes:    str(10),
	}
}

// Prune enforces the two limits that keep the database from growing without
// end: how many sessions to keep, and how old they may be.
//
// 两头都限，因为它们防的是不同的事：条数防一次抓爆（几十万条），天数防慢慢长
// （每天几千条，一年就是百万）。
func (s *DB) Prune(ctx context.Context, keep int, maxAge time.Duration) error {
	if keep > 0 {
		if _, err := s.db.Execute(ctx, `
			DELETE FROM sessions WHERE id NOT IN (
				SELECT id FROM sessions ORDER BY started DESC, id DESC LIMIT ?
			)`, int64(keep)); err != nil {
			return err
		}
	}
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge).Unix()
		if _, err := s.db.Execute(ctx,
			`DELETE FROM sessions WHERE started < ?`, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// CountSessions reports how many sessions the store holds.
func (s *DB) CountSessions(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT COUNT(*) FROM sessions`)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return 0, nil
	}
	if n, ok := rows.Values[0][0].(int64); ok {
		return int(n), nil
	}
	return 0, nil
}
