// Package store is the SQLite persistence layer: users, sessions, stories
// and everything a story owns (characters, pages, jobs, images).
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	"image/png"
	_ "modernc.org/sqlite"

	"github.com/lrgalego/pictura/internal/blob"
)

var ErrNotFound = errors.New("not found")

// Store wraps the database and the blob store holding the image bytes.
type Store struct {
	db    *sql.DB
	blobs blob.Store
}

// Open opens (or creates) the database under dataDir. blobs is where image
// bytes go; nil means files under dataDir/images.
func Open(dataDir string, blobs blob.Store) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if blobs == nil {
		fs, err := blob.NewFS(filepath.Join(dataDir, "images"))
		if err != nil {
			return nil, err
		}
		blobs = fs
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.Join(dataDir, "pictura.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, blobs: blobs}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Blobs is the byte store behind the image names.
func (s *Store) Blobs() blob.Store { return s.blobs }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS stories (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title TEXT NOT NULL,
	logline TEXT NOT NULL DEFAULT '',
	script TEXT NOT NULL,
	style TEXT NOT NULL,
	world TEXT NOT NULL DEFAULT '',
	step INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS characters (
	id INTEGER PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	name TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT '',
	age TEXT NOT NULL DEFAULT '',
	visual TEXT NOT NULL DEFAULT '',
	wardrobe TEXT NOT NULL DEFAULT '',
	items TEXT NOT NULL DEFAULT '',
	personality TEXT NOT NULL DEFAULT '',
	sheet_image TEXT NOT NULL DEFAULT '',
	sheet_status TEXT NOT NULL DEFAULT 'pending',
	sheet_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS pages (
	id INTEGER PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
	number INTEGER NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	panels_json TEXT NOT NULL DEFAULT '[]',
	image TEXT NOT NULL DEFAULT '',
	image_status TEXT NOT NULL DEFAULT 'pending',
	image_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS jobs (
	id INTEGER PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	status TEXT NOT NULL,
	progress INTEGER NOT NULL DEFAULT 0,
	total INTEGER NOT NULL DEFAULT 0,
	message TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS jobs_story ON jobs(story_id, id);
CREATE TABLE IF NOT EXISTS images (
	name TEXT PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS refs (
	id INTEGER PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
	character_id INTEGER REFERENCES characters(id) ON DELETE SET NULL,
	image TEXT NOT NULL,
	filename TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS refs_story ON refs(story_id, id);
`)
	if err != nil {
		return err
	}
	if err := s.addColumn("characters", "origin_id", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumn("jobs", "character_id", "INTEGER"); err != nil {
		return err
	}
	return s.addColumn("users", "enabled", "INTEGER NOT NULL DEFAULT 0")
}

// addColumn adds a column when it is missing (SQLite has no IF NOT EXISTS
// for ALTER TABLE ADD COLUMN).
func (s *Store) addColumn(table, column, typ string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, typ))
	return err
}

const timeFmt = time.RFC3339Nano

func now() string { return time.Now().UTC().Format(timeFmt) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFmt, s)
	return t
}

// ---------- users & sessions ----------

type User struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string
	Enabled      bool // signing up does not enable an account; an operator does
	CreatedAt    time.Time
}

func (s *Store) CreateUser(ctx context.Context, username, displayName, hash string) (*User, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, display_name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		strings.ToLower(username), displayName, hash, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("username taken")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(ctx, id)
}

const userCols = `id, username, display_name, password_hash, enabled, created_at`

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, strings.ToLower(username)))
}

// SetUserEnabled flips the account flag by username.
func (s *Store) SetUserEnabled(ctx context.Context, username string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET enabled = ? WHERE username = ?`, enabled, strings.ToLower(username))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Users lists every account, oldest first.
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		var created string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Enabled, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = parseTime(created)
		out = append(out, &u)
	}
	return out, rows.Err()
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	var created string
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Enabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = parseTime(created)
	return &u, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	token := randomHex(32)
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, time.Now().Add(ttl).UTC().Format(timeFmt))
	return token, err
}

func (s *Store) UserBySession(ctx context.Context, token string) (*User, error) {
	var userID int64
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).Scan(&userID, &expires)
	if err != nil {
		return nil, ErrNotFound
	}
	if parseTime(expires).Before(time.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return nil, ErrNotFound
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- stories ----------

// Steps of the workflow. Step is the furthest step the story has reached.
const (
	StepScript     = 1
	StepCharacters = 2
	StepPages      = 3
	StepBook       = 4
)

type Story struct {
	ID        int64
	UserID    int64
	Title     string
	Logline   string
	Script    string
	Style     string
	World     string
	Step      int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) CreateStory(ctx context.Context, userID int64, title, script, style string) (*Story, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO stories (user_id, title, script, style, step, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, title, script, style, StepScript, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Story(ctx, id)
}

func (s *Store) Story(ctx context.Context, id int64) (*Story, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, user_id, title, logline, script, style, world, step, created_at, updated_at FROM stories WHERE id = ?`, id)
	return scanStory(row)
}

type scanner interface{ Scan(dest ...any) error }

func scanStory(row scanner) (*Story, error) {
	var st Story
	var c, u string
	if err := row.Scan(&st.ID, &st.UserID, &st.Title, &st.Logline, &st.Script, &st.Style, &st.World, &st.Step, &c, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st.CreatedAt, st.UpdatedAt = parseTime(c), parseTime(u)
	return &st, nil
}

func (s *Store) StoriesByUser(ctx context.Context, userID int64) ([]*Story, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, title, logline, script, style, world, step, created_at, updated_at FROM stories WHERE user_id = ? ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Story
	for rows.Next() {
		st, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) UpdateStory(ctx context.Context, st *Story) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET title=?, logline=?, script=?, style=?, world=?, step=?, updated_at=? WHERE id=?`,
		st.Title, st.Logline, st.Script, st.Style, st.World, st.Step, now(), st.ID)
	return err
}

// SetStep raises the story's step (never lowers it) and touches updated_at.
func (s *Store) SetStep(ctx context.Context, storyID int64, step int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET step = MAX(step, ?), updated_at = ? WHERE id = ?`, step, now(), storyID)
	return err
}

// ResetStep lowers the step, for flows that invalidate later work.
func (s *Store) ResetStep(ctx context.Context, storyID int64, step int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET step = ?, updated_at = ? WHERE id = ?`, step, now(), storyID)
	return err
}

func (s *Store) Touch(ctx context.Context, storyID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET updated_at = ? WHERE id = ?`, now(), storyID)
	return err
}

func (s *Store) DeleteStory(ctx context.Context, id int64) error {
	names, _ := s.imageNames(ctx, id)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM stories WHERE id = ?`, id); err != nil {
		return err
	}
	for _, n := range names {
		s.dropBlob(ctx, n)
	}
	return nil
}

// dropBlob removes an image and its thumbnail from the blob store; a
// missing object is not an error.
func (s *Store) dropBlob(ctx context.Context, name string) {
	_ = s.blobs.Delete(ctx, name)
	_ = s.blobs.Delete(ctx, ThumbName(name))
}

// ---------- characters ----------

const (
	ImagePending    = "pending"
	ImageGenerating = "generating"
	ImageReady      = "ready"
	ImageError      = "error"
)

type Character struct {
	ID          int64
	StoryID     int64
	Position    int
	Name        string
	Role        string
	Age         string
	Visual      string
	Wardrobe    string
	Items       string
	Personality string
	SheetImage  string
	SheetStatus string
	SheetError  string
	OriginID    int64 // the character this one was copied from (0 = an original); the registry groups by Origin()
}

// Origin is the lineage key: the id of the first appearance of this character.
func (c *Character) Origin() int64 {
	if c.OriginID != 0 {
		return c.OriginID
	}
	return c.ID
}

const charCols = `id, story_id, position, name, role, age, visual, wardrobe, items, personality, sheet_image, sheet_status, sheet_error, COALESCE(origin_id, 0)`

func scanChar(row scanner) (*Character, error) {
	var c Character
	if err := row.Scan(&c.ID, &c.StoryID, &c.Position, &c.Name, &c.Role, &c.Age, &c.Visual, &c.Wardrobe, &c.Items, &c.Personality, &c.SheetImage, &c.SheetStatus, &c.SheetError, &c.OriginID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (s *Store) Characters(ctx context.Context, storyID int64) ([]*Character, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+charCols+` FROM characters WHERE story_id = ? ORDER BY position, id`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Character
	for rows.Next() {
		c, err := scanChar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Character(ctx context.Context, id int64) (*Character, error) {
	return scanChar(s.db.QueryRowContext(ctx, `SELECT `+charCols+` FROM characters WHERE id = ?`, id))
}

func (s *Store) InsertCharacter(ctx context.Context, c *Character) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO characters (story_id, position, name, role, age, visual, wardrobe, items, personality, sheet_image, sheet_status, sheet_error, origin_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.StoryID, c.Position, c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality, c.SheetImage, orDefault(c.SheetStatus, ImagePending), c.SheetError, nullable(c.OriginID))
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateCharacter(ctx context.Context, c *Character) error {
	_, err := s.db.ExecContext(ctx, `UPDATE characters SET position=?, name=?, role=?, age=?, visual=?, wardrobe=?, items=?, personality=?, sheet_image=?, sheet_status=?, sheet_error=?, origin_id=? WHERE id=?`,
		c.Position, c.Name, c.Role, c.Age, c.Visual, c.Wardrobe, c.Items, c.Personality, c.SheetImage, c.SheetStatus, c.SheetError, nullable(c.OriginID), c.ID)
	return err
}

func (s *Store) SetCharacterSheet(ctx context.Context, id int64, image, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE characters SET sheet_image=?, sheet_status=?, sheet_error=? WHERE id=?`, image, status, errMsg, id)
	return err
}

func (s *Store) DeleteCharacters(ctx context.Context, storyID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM refs WHERE story_id = ?`, storyID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM characters WHERE story_id = ?`, storyID)
	return err
}

// DeleteCharacter removes a character and its references; the blobs are
// reclaimed by the next Sweep.
func (s *Store) DeleteCharacter(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM refs WHERE character_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM characters WHERE id = ?`, id)
	return err
}

// ---------- pages ----------

type Line struct {
	Character string `json:"character"`
	Text      string `json:"text"`
}

type Panel struct {
	Number      int      `json:"number"`
	Shot        string   `json:"shot"`
	Description string   `json:"description"`
	Characters  []string `json:"characters"`
	Dialogue    []Line   `json:"dialogue"`
	Caption     string   `json:"caption"`
}

type Page struct {
	ID          int64
	StoryID     int64
	Number      int
	Title       string
	Summary     string
	Panels      []Panel
	Image       string
	ImageStatus string
	ImageError  string
}

const pageCols = `id, story_id, number, title, summary, panels_json, image, image_status, image_error`

func scanPage(row scanner) (*Page, error) {
	var p Page
	var panels string
	if err := row.Scan(&p.ID, &p.StoryID, &p.Number, &p.Title, &p.Summary, &panels, &p.Image, &p.ImageStatus, &p.ImageError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(panels), &p.Panels)
	return &p, nil
}

func (s *Store) Pages(ctx context.Context, storyID int64) ([]*Page, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pageCols+` FROM pages WHERE story_id = ? ORDER BY number, id`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Page(ctx context.Context, id int64) (*Page, error) {
	return scanPage(s.db.QueryRowContext(ctx, `SELECT `+pageCols+` FROM pages WHERE id = ?`, id))
}

func (s *Store) InsertPage(ctx context.Context, p *Page) error {
	panels, _ := json.Marshal(orPanels(p.Panels))
	res, err := s.db.ExecContext(ctx, `INSERT INTO pages (story_id, number, title, summary, panels_json, image, image_status, image_error) VALUES (?,?,?,?,?,?,?,?)`,
		p.StoryID, p.Number, p.Title, p.Summary, string(panels), p.Image, orDefault(p.ImageStatus, ImagePending), p.ImageError)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdatePage(ctx context.Context, p *Page) error {
	panels, _ := json.Marshal(orPanels(p.Panels))
	_, err := s.db.ExecContext(ctx, `UPDATE pages SET number=?, title=?, summary=?, panels_json=?, image=?, image_status=?, image_error=? WHERE id=?`,
		p.Number, p.Title, p.Summary, string(panels), p.Image, p.ImageStatus, p.ImageError, p.ID)
	return err
}

func (s *Store) SetPageImage(ctx context.Context, id int64, image, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pages SET image=?, image_status=?, image_error=? WHERE id=?`, image, status, errMsg, id)
	return err
}

func (s *Store) DeletePages(ctx context.Context, storyID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pages WHERE story_id = ?`, storyID)
	return err
}

func orPanels(p []Panel) []Panel {
	if p == nil {
		return []Panel{}
	}
	return p
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// ---------- jobs ----------

const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobError   = "error"
)

type Job struct {
	ID          int64
	StoryID     int64
	CharacterID int64 // 0 = a story-level job; otherwise the one character it works on
	Kind        string
	Status      string
	Progress    int
	Total       int
	Message     string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (j *Job) Running() bool { return j != nil && j.Status == JobRunning }

// Queued reports whether the job is waiting for its turn.
func (j *Job) Queued() bool { return j != nil && j.Status == JobQueued }

// Active is queued or running: the UI keeps polling while any job is active.
func (j *Job) Active() bool { return j.Queued() || j.Running() }

const jobCols = `id, story_id, COALESCE(character_id, 0), kind, status, progress, total, message, error, created_at, updated_at`

func scanJob(row scanner) (*Job, error) {
	var j Job
	var c, u string
	if err := row.Scan(&j.ID, &j.StoryID, &j.CharacterID, &j.Kind, &j.Status, &j.Progress, &j.Total, &j.Message, &j.Error, &c, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.CreatedAt, j.UpdatedAt = parseTime(c), parseTime(u)
	return &j, nil
}

// CreateJob records a queued job; characterID 0 means story-level. StartJob
// flips it to running when the scheduler picks it up.
func (s *Store) CreateJob(ctx context.Context, storyID, characterID int64, kind, message string, total int) (*Job, error) {
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs (story_id, character_id, kind, status, progress, total, message, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		storyID, nullable(characterID), kind, JobQueued, 0, total, message, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Job(ctx, id)
}

func (s *Store) Job(ctx context.Context, id int64) (*Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
}

// LatestJob returns the most recent story-level job for a story, or nil.
func (s *Store) LatestJob(ctx context.Context, storyID int64) (*Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE story_id = ? AND character_id IS NULL ORDER BY id DESC LIMIT 1`, storyID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return j, err
}

// LatestCharacterJobs returns one job per character for the cards to show:
// the running one when there is one (a queued change behind it must not hide
// it), otherwise the character's most recent job (queued, done or failed).
func (s *Store) LatestCharacterJobs(ctx context.Context, storyID int64) (map[int64]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE story_id = ? AND character_id IS NOT NULL ORDER BY id DESC`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		cur, seen := out[j.CharacterID]
		if !seen || (j.Running() && !cur.Running()) {
			out[j.CharacterID] = j
		}
	}
	return out, rows.Err()
}

// AnyRunning reports whether any job (story-level or per character) is
// queued or running for a story — what decides whether the step panel
// keeps polling.
func (s *Store) AnyRunning(ctx context.Context, storyID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE story_id = ? AND status IN (?, ?)`, storyID, JobRunning, JobQueued).Scan(&n)
	return n > 0, err
}

// StartJob marks a queued job running and restarts its clock.
func (s *Store) StartJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, created_at=?, updated_at=? WHERE id=?`, JobRunning, now(), now(), id)
	return err
}

func (s *Store) UpdateJob(ctx context.Context, id int64, progress, total int, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET progress=?, total=?, message=?, updated_at=? WHERE id=?`, progress, total, message, now(), id)
	return err
}

func (s *Store) FinishJob(ctx context.Context, id int64, errMsg string) error {
	status := JobDone
	if errMsg != "" {
		status = JobError
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, error=?, updated_at=? WHERE id=?`, status, errMsg, now(), id)
	return err
}

// FailRunningJobs marks jobs left queued or running by a previous process
// as failed: the queue lives in memory and did not survive.
func (s *Store) FailRunningJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, error=?, updated_at=? WHERE status IN (?, ?)`, JobError, "interrupted by a server restart", now(), JobRunning, JobQueued)
	return err
}

// ---------- images ----------

// ThumbWidth is the width of the JPEG thumbnail written beside every image;
// grids and covers load that instead of the multi-megabyte original.
const ThumbWidth = 640

// ThumbName is the blob name of an image's thumbnail.
func ThumbName(name string) string { return name + ".thumb.jpg" }

// SaveImage stores an image (and its thumbnail) owned by a story and
// returns its name.
func (s *Store) SaveImage(ctx context.Context, storyID int64, ext string, data []byte) (string, error) {
	name := randomHex(12) + "." + ext
	if err := s.blobs.Put(ctx, name, data); err != nil {
		return "", err
	}
	if thumb, err := makeThumb(data); err == nil {
		_ = s.blobs.Put(ctx, ThumbName(name), thumb)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO images (name, story_id) VALUES (?, ?)`, name, storyID); err != nil {
		return "", err
	}
	return name, nil
}

// ImageOwner returns the story that owns an image name, or ErrNotFound.
func (s *Store) ImageOwner(ctx context.Context, name string) (int64, error) {
	if strings.ContainsAny(name, "/\\") || name == "" {
		return 0, ErrNotFound
	}
	var storyID int64
	if err := s.db.QueryRowContext(ctx, `SELECT story_id FROM images WHERE name = ?`, name).Scan(&storyID); err != nil {
		return 0, ErrNotFound
	}
	return storyID, nil
}

func makeThumb(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if b.Dx() <= ThumbWidth {
		var buf bytes.Buffer
		err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 82})
		return buf.Bytes(), err
	}
	h := b.Dy() * ThumbWidth / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, ThumbWidth, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadImage returns the bytes of an image by name.
func (s *Store) ReadImage(ctx context.Context, name string) ([]byte, error) {
	if _, err := s.ImageOwner(ctx, name); err != nil {
		return nil, err
	}
	return s.blobs.Get(ctx, name)
}

// ReadThumb returns the thumbnail's bytes, falling back to the original.
func (s *Store) ReadThumb(ctx context.Context, name string) ([]byte, string, error) {
	if _, err := s.ImageOwner(ctx, name); err != nil {
		return nil, "", err
	}
	if b, err := s.blobs.Get(ctx, ThumbName(name)); err == nil {
		return b, ThumbName(name), nil
	}
	b, err := s.blobs.Get(ctx, name)
	return b, name, err
}

// ImageNames lists every stored image name (for migrations).
func (s *Store) ImageNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM images ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Sweep is the garbage collector: it deletes every image the story owns
// that no character sheet, page or reference points at any more (art that
// was redrawn, references removed with their character), and returns how
// many were reclaimed. storyID 0 sweeps every story.
func (s *Store) Sweep(ctx context.Context, storyID int64) (int, error) {
	q := `SELECT name FROM images i
		WHERE NOT EXISTS (SELECT 1 FROM characters c WHERE c.sheet_image = i.name)
		  AND NOT EXISTS (SELECT 1 FROM pages p WHERE p.image = i.name)
		  AND NOT EXISTS (SELECT 1 FROM refs r WHERE r.image = i.name)`
	args := []any{}
	if storyID != 0 {
		q += ` AND i.story_id = ?`
		args = append(args, storyID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var orphans []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		orphans = append(orphans, n)
	}
	rows.Close()
	for _, n := range orphans {
		s.dropBlob(ctx, n)
		if _, err := s.db.ExecContext(ctx, `DELETE FROM images WHERE name = ?`, n); err != nil {
			return 0, err
		}
	}
	return len(orphans), nil
}

func (s *Store) imageNames(ctx context.Context, storyID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM images WHERE story_id = ?`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---------- reference images ----------

// Ref is a writer-supplied reference image: a photo, sketch, outfit or prop
// the character (or the whole cast, while unassigned) should look like.
type Ref struct {
	ID          int64
	StoryID     int64
	CharacterID int64 // 0 = not assigned to a character yet
	Image       string
	Filename    string
	Note        string
	CreatedAt   time.Time
}

const refCols = `id, story_id, COALESCE(character_id, 0), image, filename, note, created_at`

func scanRef(row scanner) (*Ref, error) {
	var r Ref
	var c string
	if err := row.Scan(&r.ID, &r.StoryID, &r.CharacterID, &r.Image, &r.Filename, &r.Note, &c); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.CreatedAt = parseTime(c)
	return &r, nil
}

// InsertRef stores an uploaded reference. The bytes are normalized to a
// PNG no wider than 1600px so every downstream call speaks one format.
func (s *Store) InsertRef(ctx context.Context, storyID, characterID int64, filename, note string, data []byte) (*Ref, error) {
	normalized, err := NormalizeImage(data, 1600)
	if err != nil {
		return nil, fmt.Errorf("%s: not a readable image (PNG, JPEG, GIF or WebP)", filename)
	}
	name, err := s.SaveImage(ctx, storyID, "png", normalized)
	if err != nil {
		return nil, err
	}
	var cid any
	if characterID != 0 {
		cid = characterID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO refs (story_id, character_id, image, filename, note, created_at) VALUES (?,?,?,?,?,?)`,
		storyID, cid, name, filename, note, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Ref(ctx, id)
}

func (s *Store) Ref(ctx context.Context, id int64) (*Ref, error) {
	return scanRef(s.db.QueryRowContext(ctx, `SELECT `+refCols+` FROM refs WHERE id = ?`, id))
}

// Refs lists a story's references, unassigned first, oldest first.
func (s *Store) Refs(ctx context.Context, storyID int64) ([]*Ref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+refCols+` FROM refs WHERE story_id = ? ORDER BY id`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Ref
	for rows.Next() {
		r, err := scanRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AssignRef attaches a reference to a character (0 detaches it).
func (s *Store) AssignRef(ctx context.Context, refID, characterID int64) error {
	var cid any
	if characterID != 0 {
		cid = characterID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE refs SET character_id = ? WHERE id = ?`, cid, refID)
	return err
}

func (s *Store) DeleteRef(ctx context.Context, id int64) error {
	r, err := s.Ref(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM refs WHERE id = ?`, id); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM images WHERE name = ?`, r.Image)
	s.dropBlob(ctx, r.Image)
	return nil
}

// NormalizeImage decodes any supported image and re-encodes it as PNG,
// downscaling so the longer side is at most maxSide pixels.
func NormalizeImage(data []byte, maxSide int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("empty image")
	}
	if w > maxSide || h > maxSide {
		if w >= h {
			h = h * maxSide / w
			w = maxSide
		} else {
			w = w * maxSide / h
			h = maxSide
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- cast registry ----------

// LibraryEntry is a character together with the story it appears in.
type LibraryEntry struct {
	Character *Character
	Story     *Story
}

// Library lists every character across a user's stories, newest first.
// The registry view groups them by Character.Origin().
func (s *Store) Library(ctx context.Context, userID int64) ([]LibraryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.story_id, c.position, c.name, c.role, c.age, c.visual, c.wardrobe, c.items, c.personality, c.sheet_image, c.sheet_status, c.sheet_error, COALESCE(c.origin_id, 0),
		s.id, s.user_id, s.title, s.logline, s.script, s.style, s.world, s.step, s.created_at, s.updated_at
		FROM characters c JOIN stories s ON s.id = c.story_id
		WHERE s.user_id = ? ORDER BY c.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LibraryEntry
	for rows.Next() {
		var c Character
		var st Story
		var created, updated string
		if err := rows.Scan(&c.ID, &c.StoryID, &c.Position, &c.Name, &c.Role, &c.Age, &c.Visual, &c.Wardrobe, &c.Items, &c.Personality, &c.SheetImage, &c.SheetStatus, &c.SheetError, &c.OriginID,
			&st.ID, &st.UserID, &st.Title, &st.Logline, &st.Script, &st.Style, &st.World, &st.Step, &created, &updated); err != nil {
			return nil, err
		}
		st.CreatedAt, st.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, LibraryEntry{Character: &c, Story: &st})
	}
	return out, rows.Err()
}

// LinkCharacter makes dst the same character as src: it copies the look,
// the references and the finished sheet (files are duplicated so deleting
// either story leaves the other whole) and records the lineage.
func (s *Store) LinkCharacter(ctx context.Context, dst, src *Character) error {
	dst.Age, dst.Visual, dst.Wardrobe, dst.Items, dst.Personality = src.Age, src.Visual, src.Wardrobe, src.Items, src.Personality
	if dst.Role == "" {
		dst.Role = src.Role
	}
	dst.OriginID = src.Origin()
	dst.SheetImage, dst.SheetStatus, dst.SheetError = "", ImagePending, ""
	if src.SheetStatus == ImageReady && src.SheetImage != "" {
		name, err := s.copyImage(ctx, src.SheetImage, dst.StoryID)
		if err != nil {
			return err
		}
		dst.SheetImage, dst.SheetStatus = name, ImageReady
	}
	if err := s.UpdateCharacter(ctx, dst); err != nil {
		return err
	}
	refs, err := s.Refs(ctx, src.StoryID)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if r.CharacterID != src.ID {
			continue
		}
		name, err := s.copyImage(ctx, r.Image, dst.StoryID)
		if err != nil {
			continue
		}
		_, _ = s.db.ExecContext(ctx, `INSERT INTO refs (story_id, character_id, image, filename, note, created_at) VALUES (?,?,?,?,?,?)`,
			dst.StoryID, dst.ID, name, r.Filename, r.Note, now())
	}
	return nil
}

// copyImage duplicates a stored image (thumbnail regenerated) under a new
// name owned by another story.
func (s *Store) copyImage(ctx context.Context, name string, storyID int64) (string, error) {
	data, err := s.ReadImage(ctx, name)
	if err != nil {
		return "", err
	}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if ext == "" {
		ext = "png"
	}
	return s.SaveImage(ctx, storyID, ext, data)
}
