package store

import (
	"context"
	"database/sql"
	"errors"
)

// ---------- the world: places and props ----------

// Kinds of world element.
const (
	KindPlace = "place"
	KindProp  = "prop"
)

// Element is a place or a prop the story keeps coming back to, with the
// concept art that keeps it the same from page to page, as a character
// sheet keeps a character.
type Element struct {
	ID       int64
	StoryID  int64
	Position int
	Kind     string // KindPlace or KindProp
	Name     string
	Visual   string // what it looks like: shape, materials, colors
	Layout   string // a place's geography; a prop's size and how it is used
	// ArtImage is the concept art; like a character's sheet it keeps the
	// last drawing while a new one is made or fails.
	ArtImage  string
	ArtStatus string
	ArtError  string
}

// IsPlace reports whether the element is a place (a prop otherwise).
func (e *Element) IsPlace() bool { return e.Kind == KindPlace }

func (s *Store) migrateWorld() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS elements (
	id INTEGER PRIMARY KEY,
	story_id INTEGER NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL,
	visual TEXT NOT NULL DEFAULT '',
	layout TEXT NOT NULL DEFAULT '',
	art_image TEXT NOT NULL DEFAULT '',
	art_status TEXT NOT NULL DEFAULT 'pending',
	art_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS elements_story ON elements(story_id, position);
`)
	return err
}

const elementCols = `id, story_id, position, kind, name, visual, layout, art_image, art_status, art_error`

func scanElement(row scanner) (*Element, error) {
	var e Element
	if err := row.Scan(&e.ID, &e.StoryID, &e.Position, &e.Kind, &e.Name, &e.Visual, &e.Layout, &e.ArtImage, &e.ArtStatus, &e.ArtError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

// Elements lists a story's places then its props, each in their order.
func (s *Store) Elements(ctx context.Context, storyID int64) ([]*Element, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+elementCols+` FROM elements WHERE story_id = ? ORDER BY kind = 'prop', position, id`, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Element
	for rows.Next() {
		e, err := scanElement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Element(ctx context.Context, id int64) (*Element, error) {
	return scanElement(s.db.QueryRowContext(ctx, `SELECT `+elementCols+` FROM elements WHERE id = ?`, id))
}

func (s *Store) InsertElement(ctx context.Context, e *Element) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO elements (story_id, position, kind, name, visual, layout, art_image, art_status, art_error) VALUES (?,?,?,?,?,?,?,?,?)`,
		e.StoryID, e.Position, e.Kind, e.Name, e.Visual, e.Layout, e.ArtImage, orDefault(e.ArtStatus, ImagePending), e.ArtError)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

// UpdateElement saves the words; the art is written by SetElementArt.
func (s *Store) UpdateElement(ctx context.Context, e *Element) error {
	_, err := s.db.ExecContext(ctx, `UPDATE elements SET position=?, kind=?, name=?, visual=?, layout=? WHERE id=?`,
		e.Position, e.Kind, e.Name, e.Visual, e.Layout, e.ID)
	return err
}

func (s *Store) SetElementArt(ctx context.Context, id int64, image, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE elements SET art_image=?, art_status=?, art_error=? WHERE id=?`, image, status, errMsg, id)
	return err
}

// DeleteElement removes one element; its art is reclaimed by the next Sweep.
func (s *Store) DeleteElement(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM elements WHERE id = ?`, id)
	return err
}

// DeleteElements removes a story's whole world.
func (s *Store) DeleteElements(ctx context.Context, storyID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM elements WHERE story_id = ?`, storyID)
	return err
}
