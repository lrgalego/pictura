package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/lrgalego/pictura/internal/usage"
)

// ---------- model service calls ----------

func (s *Store) migrateAdmin() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS api_calls (
	id INTEGER PRIMARY KEY,
	at INTEGER NOT NULL, -- unix milliseconds
	provider TEXT NOT NULL,
	kind TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	op TEXT NOT NULL DEFAULT '',
	story_id INTEGER NOT NULL DEFAULT 0,
	job TEXT NOT NULL DEFAULT '',
	ok INTEGER NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	chars INTEGER NOT NULL DEFAULT 0,
	seconds REAL NOT NULL DEFAULT 0,
	credits INTEGER NOT NULL DEFAULT 0,
	ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS api_calls_at ON api_calls(at);
CREATE INDEX IF NOT EXISTS api_calls_agent ON api_calls(provider, kind, op, id);
`); err != nil {
		return err
	}
	return s.addColumn("users", "admin", "INTEGER NOT NULL DEFAULT 0")
}

// RecordCall keeps one call to a model service.
func (s *Store) RecordCall(ctx context.Context, c usage.Call) error {
	if c.At.IsZero() {
		c.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_calls (at, provider, kind, model, op, story_id, job, ok, error, input_tokens, output_tokens, reasoning_tokens, cached_tokens, chars, seconds, credits, ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.At.UnixMilli(), c.Provider, c.Kind, c.Model, c.Op, c.StoryID, c.Job, c.OK, c.Error,
		c.InputTokens, c.OutputTokens, c.ReasoningTokens, c.CachedTokens, c.Chars, c.Seconds, c.Credits, c.Duration.Milliseconds())
	return err
}

// AgentUsage is what one agent (a provider, kind and op) did over a window.
type AgentUsage struct {
	Provider, Kind, Op, Model string
	Calls, Failures           int
	InputTokens               int
	OutputTokens              int
	ReasoningTokens           int
	CachedTokens              int
	Chars                     int
	Seconds                   float64
	Credits                   int
	Busy                      time.Duration // time spent waiting on answers
}

// Usage sums the calls since a time, per agent, the busiest first.
func (s *Store) Usage(ctx context.Context, since time.Time) ([]AgentUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, kind, op, MAX(model), COUNT(*), SUM(ok = 0),
		SUM(input_tokens), SUM(output_tokens), SUM(reasoning_tokens), SUM(cached_tokens), SUM(chars), SUM(seconds), SUM(credits), SUM(ms)
		FROM api_calls WHERE at >= ? GROUP BY provider, kind, op ORDER BY COUNT(*) DESC, provider, kind, op`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentUsage
	for rows.Next() {
		var u AgentUsage
		var ms int64
		if err := rows.Scan(&u.Provider, &u.Kind, &u.Op, &u.Model, &u.Calls, &u.Failures,
			&u.InputTokens, &u.OutputTokens, &u.ReasoningTokens, &u.CachedTokens, &u.Chars, &u.Seconds, &u.Credits, &ms); err != nil {
			return nil, err
		}
		u.Busy = time.Duration(ms) * time.Millisecond
		out = append(out, u)
	}
	return out, rows.Err()
}

// AgentState is how an agent's calls have gone, all time.
type AgentState struct {
	Provider, Kind, Op, Model string
	LastAt                    time.Time // the latest call
	LastOK                    bool
	LastOKAt                  time.Time // the latest call that worked
	LastError                 string    // the latest failure
	LastErrorAt               time.Time
}

// AgentStates reports, per agent, its latest call, latest success and
// latest failure.
func (s *Store) AgentStates(ctx context.Context) ([]AgentState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.provider, c.kind, c.op, c.model, c.at, c.ok,
		COALESCE((SELECT MAX(at) FROM api_calls o WHERE o.provider = c.provider AND o.kind = c.kind AND o.op = c.op AND o.ok = 1), 0),
		COALESCE((SELECT e.error || char(31) || e.at FROM api_calls e WHERE e.provider = c.provider AND e.kind = c.kind AND e.op = c.op AND e.ok = 0 ORDER BY e.id DESC LIMIT 1), '')
		FROM api_calls c WHERE c.id IN (SELECT MAX(id) FROM api_calls GROUP BY provider, kind, op)
		ORDER BY c.provider, c.kind, c.op`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentState
	for rows.Next() {
		var a AgentState
		var at, okAt int64
		var lastErr string
		if err := rows.Scan(&a.Provider, &a.Kind, &a.Op, &a.Model, &at, &a.LastOK, &okAt, &lastErr); err != nil {
			return nil, err
		}
		a.LastAt = time.UnixMilli(at)
		if okAt > 0 {
			a.LastOKAt = time.UnixMilli(okAt)
		}
		if msg, when, ok := strings.Cut(lastErr, "\x1f"); ok {
			a.LastError = msg
			ms, _ := strconv.ParseInt(when, 10, 64)
			a.LastErrorAt = time.UnixMilli(ms)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// StoryUsage is what one story's jobs spent over a window.
type StoryUsage struct {
	StoryID      int64
	Title        string
	Username     string
	Calls        int
	InputTokens  int
	OutputTokens int
	Images       int
	Chars        int
	Credits      int
}

// UsageByStory sums the calls since a time per story, the costliest first.
func (s *Store) UsageByStory(ctx context.Context, since time.Time, limit int) ([]StoryUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.story_id, COALESCE(st.title, ''), COALESCE(u.username, ''), COUNT(*),
		SUM(c.input_tokens), SUM(c.output_tokens), SUM(c.kind = 'image' AND c.ok = 1), SUM(c.chars), SUM(c.credits)
		FROM api_calls c LEFT JOIN stories st ON st.id = c.story_id LEFT JOIN users u ON u.id = st.user_id
		WHERE c.at >= ? AND c.story_id <> 0 GROUP BY c.story_id
		ORDER BY SUM(c.kind = 'image' AND c.ok = 1) DESC, SUM(c.output_tokens) DESC LIMIT ?`, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoryUsage
	for rows.Next() {
		var u StoryUsage
		if err := rows.Scan(&u.StoryID, &u.Title, &u.Username, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.Images, &u.Chars, &u.Credits); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// LastErrorLike is the newest failed call of a provider whose error holds
// the given text, and when it happened; empty when there is none.
func (s *Store) LastErrorLike(ctx context.Context, provider, text string) (string, time.Time, error) {
	var msg string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT error, at FROM api_calls WHERE provider = ? AND ok = 0 AND instr(error, ?) > 0 ORDER BY id DESC LIMIT 1`, provider, text).Scan(&msg, &at)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	return msg, time.UnixMilli(at), nil
}

// LastOKAt is when a provider last answered a call of a kind.
func (s *Store) LastOKAt(ctx context.Context, provider, kind string) (time.Time, error) {
	var at int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), 0) FROM api_calls WHERE provider = ? AND kind = ? AND ok = 1`, provider, kind).Scan(&at); err != nil || at == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(at), nil
}

// ---------- admins ----------

// SetUserAdmin flips the admin flag by username.
func (s *Store) SetUserAdmin(ctx context.Context, username string, admin bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET admin = ? WHERE username = ?`, admin, strings.ToLower(username))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- jobs across every story ----------

// JobRow is a job with the story and owner it belongs to.
type JobRow struct {
	Job
	Title    string
	Username string
}

// ActiveJobs lists every queued or running job, oldest first.
func (s *Store) ActiveJobs(ctx context.Context) ([]*JobRow, error) {
	return s.jobRows(ctx, `j.status IN ('queued', 'running') ORDER BY j.id`)
}

// FailedJobs lists the latest failed jobs, newest first.
func (s *Store) FailedJobs(ctx context.Context, limit int) ([]*JobRow, error) {
	return s.jobRows(ctx, `j.status = 'error' ORDER BY j.id DESC LIMIT ?`, limit)
}

func (s *Store) jobRows(ctx context.Context, where string, args ...any) ([]*JobRow, error) {
	const cols = `j.id, j.story_id, COALESCE(j.character_id, 0), COALESCE(j.page_id, 0), j.kind, j.status, j.progress, j.total, j.message, j.error, j.created_at, j.updated_at`
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+`, st.title, u.username FROM jobs j JOIN stories st ON st.id = j.story_id JOIN users u ON u.id = st.user_id WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobRow
	for rows.Next() {
		var r JobRow
		var created, updated string
		if err := rows.Scan(&r.ID, &r.StoryID, &r.CharacterID, &r.PageID, &r.Kind, &r.Status, &r.Progress, &r.Total, &r.Message, &r.Error, &created, &updated, &r.Title, &r.Username); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, &r)
	}
	return out, rows.Err()
}
