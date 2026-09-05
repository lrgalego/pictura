package store

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func testJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func seed(t *testing.T, s *Store) (*User, *Story) {
	t.Helper()
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "Writer", "The Writer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.CreateStory(ctx, u.ID, "Title", "script", "comic")
	if err != nil {
		t.Fatal(err)
	}
	return u, st
}

func TestOpenIsIdempotentAndMigrates(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopening runs the migration again, including addColumn's no-op path.
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(filepath.Join(dir, "missing", "\x00bad")); err == nil {
		t.Fatal("expected an error for an unusable data dir")
	}
}

func TestUsersAndSessions(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "Lucas", "Lucas G", "h")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "lucas" || u.CreatedAt.IsZero() {
		t.Fatalf("user not normalized: %+v", u)
	}
	if _, err := s.CreateUser(ctx, "LUCAS", "dup", "h"); err == nil || err.Error() != "username taken" {
		t.Fatalf("duplicate username should be refused, got %v", err)
	}
	if _, err := s.UserByUsername(ctx, "nobody"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.UserByID(ctx, 999); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	tok, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.UserBySession(ctx, tok)
	if err != nil || got.ID != u.ID {
		t.Fatalf("session lookup: %v %v", got, err)
	}
	if _, err := s.UserBySession(ctx, "nope"); err != ErrNotFound {
		t.Fatal("unknown token should be ErrNotFound")
	}
	expired, _ := s.CreateSession(ctx, u.ID, -time.Minute)
	if _, err := s.UserBySession(ctx, expired); err != ErrNotFound {
		t.Fatal("expired session should be ErrNotFound")
	}
	if err := s.DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, tok); err != ErrNotFound {
		t.Fatal("deleted session should be gone")
	}
}

func TestStoriesLifecycle(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	u, st := seed(t, s)
	if st.Step != StepScript {
		t.Fatalf("new story should be at step 1, got %d", st.Step)
	}
	st.Title, st.Logline, st.World = "New", "log", "world"
	if err := s.UpdateStory(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStep(ctx, st.ID, StepPages); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStep(ctx, st.ID, StepCharacters); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Story(ctx, st.ID)
	if got.Step != StepPages || got.Title != "New" {
		t.Fatalf("SetStep must never lower the step: %+v", got)
	}
	if err := s.ResetStep(ctx, st.ID, StepScript); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Story(ctx, st.ID)
	if got.Step != StepScript {
		t.Fatal("ResetStep should lower the step")
	}
	if err := s.Touch(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.StoriesByUser(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("stories: %v %v", list, err)
	}
	if _, err := s.Story(ctx, 12345); err != ErrNotFound {
		t.Fatal("missing story should be ErrNotFound")
	}
}

func TestCharactersPagesAndImages(t *testing.T) {
	s, dir := open(t)
	ctx := context.Background()
	_, st := seed(t, s)

	c := &Character{StoryID: st.ID, Name: "Mara", Role: "hero", Visual: "small"}
	if err := s.InsertCharacter(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 {
		t.Fatal("insert should set the id")
	}
	if got, _ := s.Character(ctx, c.ID); got.SheetStatus != ImagePending || got.Origin() != c.ID {
		t.Fatalf("defaults: %+v", got)
	}
	name, err := s.SaveImage(ctx, st.ID, "png", testPNG(1200, 800))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCharacterSheet(ctx, c.ID, name, ImageReady, ""); err != nil {
		t.Fatal(err)
	}
	thumb, storyID, err := s.ThumbPath(ctx, name)
	if err != nil || storyID != st.ID || filepath.Ext(thumb) != ".jpg" {
		t.Fatalf("thumb: %s %d %v", thumb, storyID, err)
	}
	if _, _, err := s.ImagePath(ctx, "../etc/passwd"); err != ErrNotFound {
		t.Fatal("path traversal should be ErrNotFound")
	}
	if _, _, err := s.ImagePath(ctx, "unknown.png"); err != ErrNotFound {
		t.Fatal("unknown image should be ErrNotFound")
	}
	if _, err := s.ReadImage(ctx, "unknown.png"); err == nil {
		t.Fatal("reading an unknown image should fail")
	}
	// A small image gets a thumbnail without upscaling.
	small, _ := s.SaveImage(ctx, st.ID, "png", testPNG(100, 50))
	if p, _, _ := s.ThumbPath(ctx, small); filepath.Ext(p) != ".jpg" {
		t.Fatal("small images still get a thumbnail")
	}
	// A non-image payload is stored but gets no thumbnail: ThumbPath falls back.
	raw, _ := s.SaveImage(ctx, st.ID, "bin", []byte("not an image"))
	if p, _, _ := s.ThumbPath(ctx, raw); filepath.Ext(p) != ".bin" {
		t.Fatal("fallback to the original when no thumbnail exists")
	}

	p := &Page{StoryID: st.ID, Number: 1, Title: "One", Panels: []Panel{{Number: 1, Description: "d", Characters: []string{"Mara"}, Dialogue: []Line{{"Mara", "hi"}}}}}
	if err := s.InsertPage(ctx, p); err != nil {
		t.Fatal(err)
	}
	empty := &Page{StoryID: st.ID, Number: 2}
	if err := s.InsertPage(ctx, empty); err != nil {
		t.Fatal(err)
	}
	pages, _ := s.Pages(ctx, st.ID)
	if len(pages) != 2 || len(pages[0].Panels) != 1 || pages[0].Panels[0].Dialogue[0].Text != "hi" || pages[1].Panels == nil {
		t.Fatalf("pages round trip: %+v", pages)
	}
	p.Title = "Renamed"
	if err := s.UpdatePage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPageImage(ctx, p.ID, name, ImageReady, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Page(ctx, p.ID); got.Title != "Renamed" || got.ImageStatus != ImageReady {
		t.Fatalf("page update: %+v", got)
	}
	if _, err := s.Page(ctx, 999); err != ErrNotFound {
		t.Fatal("missing page should be ErrNotFound")
	}
	if _, err := s.Character(ctx, 999); err != ErrNotFound {
		t.Fatal("missing character should be ErrNotFound")
	}

	// Deleting the story removes files, thumbnails and every row.
	files, _ := os.ReadDir(filepath.Join(dir, "images"))
	if len(files) < 4 {
		t.Fatalf("expected images on disk, got %d", len(files))
	}
	if err := s.DeleteStory(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	files, _ = os.ReadDir(filepath.Join(dir, "images"))
	if len(files) != 0 {
		t.Fatalf("images should be removed with the story, %d left", len(files))
	}
	if _, err := s.Character(ctx, c.ID); err != ErrNotFound {
		t.Fatal("characters should cascade")
	}
}

func TestDeleteHelpers(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	_, st := seed(t, s)
	a := &Character{StoryID: st.ID, Name: "A"}
	b := &Character{StoryID: st.ID, Name: "B"}
	_ = s.InsertCharacter(ctx, a)
	_ = s.InsertCharacter(ctx, b)
	_ = s.InsertPage(ctx, &Page{StoryID: st.ID, Number: 1})
	if err := s.DeleteCharacter(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCharacters(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePages(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	chars, _ := s.Characters(ctx, st.ID)
	pages, _ := s.Pages(ctx, st.ID)
	if len(chars) != 0 || len(pages) != 0 {
		t.Fatal("delete helpers should empty the story")
	}
}

func TestJobs(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	_, st := seed(t, s)
	if j, err := s.LatestJob(ctx, st.ID); err != nil || j != nil {
		t.Fatalf("no jobs yet: %v %v", j, err)
	}
	j, err := s.CreateJob(ctx, st.ID, "analyze", "Reading…", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Running() {
		t.Fatal("new job should be running")
	}
	if err := s.UpdateJob(ctx, j.ID, 2, 3, "Drawing"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Job(ctx, j.ID)
	if got.Progress != 2 || got.Total != 3 || got.Message != "Drawing" {
		t.Fatalf("update: %+v", got)
	}
	if err := s.FinishJob(ctx, j.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LatestJob(ctx, st.ID)
	if got.Status != JobError || got.Error != "boom" || got.Running() {
		t.Fatalf("finish with error: %+v", got)
	}
	j2, _ := s.CreateJob(ctx, st.ID, "render", "", 1)
	if err := s.FailRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Job(ctx, j2.ID)
	if got.Status != JobError {
		t.Fatal("restart should fail running jobs")
	}
	if err := s.FinishJob(ctx, j2.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Job(ctx, j2.ID)
	if got.Status != JobDone {
		t.Fatal("finish without error should be done")
	}
	if _, err := s.Job(ctx, 999); err != ErrNotFound {
		t.Fatal("missing job should be ErrNotFound")
	}
	var nilJob *Job
	if nilJob.Running() {
		t.Fatal("nil job is not running")
	}
}

func TestReferencesAndNormalize(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	_, st := seed(t, s)
	c := &Character{StoryID: st.ID, Name: "Pip"}
	_ = s.InsertCharacter(ctx, c)

	if _, err := s.InsertRef(ctx, st.ID, c.ID, "notes.txt", "", []byte("nope")); err == nil {
		t.Fatal("non-image should be refused")
	}
	// JPEG input is normalized to PNG and downscaled.
	r, err := s.InsertRef(ctx, st.ID, c.ID, "big.jpg", "the coat", testJPEG(3000, 1500))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := s.ReadImage(ctx, r.Image)
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "png" || img.Bounds().Dx() != 1600 || img.Bounds().Dy() != 800 {
		t.Fatalf("normalized: %s %v %v", format, img.Bounds(), err)
	}
	tall, _ := NormalizeImage(testPNG(100, 400), 200)
	img, _, _ = image.Decode(bytes.NewReader(tall))
	if img.Bounds().Dy() != 200 || img.Bounds().Dx() != 50 {
		t.Fatalf("tall image scaled by height: %v", img.Bounds())
	}
	if _, err := NormalizeImage([]byte("x"), 100); err == nil {
		t.Fatal("garbage should not normalize")
	}
	unassigned, _ := s.InsertRef(ctx, st.ID, 0, "loose.png", "", testPNG(10, 10))
	refs, _ := s.Refs(ctx, st.ID)
	if len(refs) != 2 || refs[0].CharacterID != c.ID || unassigned.CharacterID != 0 || refs[0].CreatedAt.IsZero() {
		t.Fatalf("refs: %+v", refs)
	}
	if err := s.AssignRef(ctx, unassigned.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Ref(ctx, unassigned.ID); got.CharacterID != c.ID {
		t.Fatal("assign should attach the reference")
	}
	if err := s.AssignRef(ctx, unassigned.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRef(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ref(ctx, r.ID); err != ErrNotFound {
		t.Fatal("deleted ref should be gone")
	}
	if _, _, err := s.ImagePath(ctx, r.Image); err != ErrNotFound {
		t.Fatal("deleting a ref should drop its image")
	}
	if err := s.DeleteRef(ctx, 999); err != ErrNotFound {
		t.Fatal("deleting a missing ref should be ErrNotFound")
	}
}

func TestLibraryAndLink(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	u, a := seed(t, s)
	b, _ := s.CreateStory(ctx, u.ID, "Book Two", "script", "manga")
	other, _ := s.CreateUser(ctx, "other", "Other", "h")
	theirs, _ := s.CreateStory(ctx, other.ID, "Theirs", "script", "noir")
	_ = s.InsertCharacter(ctx, &Character{StoryID: theirs.ID, Name: "Mara"})

	src := &Character{StoryID: a.ID, Name: "Mara", Role: "hero", Age: "9", Visual: "freckles", Wardrobe: "coat", Items: "compass", Personality: "brave"}
	_ = s.InsertCharacter(ctx, src)
	sheet, _ := s.SaveImage(ctx, a.ID, "png", testPNG(30, 20))
	_ = s.SetCharacterSheet(ctx, src.ID, sheet, ImageReady, "")
	_, _ = s.InsertRef(ctx, a.ID, src.ID, "coat.png", "the coat", testPNG(8, 8))
	_, _ = s.InsertRef(ctx, a.ID, 0, "loose.png", "", testPNG(8, 8)) // not the character's: not copied
	src, _ = s.Character(ctx, src.ID)

	lib, err := s.Library(ctx, u.ID)
	if err != nil || len(lib) != 1 || lib[0].Story.ID != a.ID || lib[0].Character.Name != "Mara" {
		t.Fatalf("library: %+v %v", lib, err)
	}

	dst := &Character{StoryID: b.ID, Name: "Mara", Role: ""}
	_ = s.InsertCharacter(ctx, dst)
	if err := s.LinkCharacter(ctx, dst, src); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Character(ctx, dst.ID)
	if got.OriginID != src.ID || got.Visual != "freckles" || got.Role != "hero" || got.SheetStatus != ImageReady || got.SheetImage == sheet || got.SheetImage == "" {
		t.Fatalf("link should copy the look and duplicate the sheet: %+v", got)
	}
	refs, _ := s.Refs(ctx, b.ID)
	if len(refs) != 1 || refs[0].CharacterID != dst.ID || refs[0].Note != "the coat" {
		t.Fatalf("link should copy the character's references only: %+v", refs)
	}
	// Lineage chains: linking from a copy keeps the original origin.
	c3, _ := s.CreateStory(ctx, u.ID, "Book Three", "script", "comic")
	third := &Character{StoryID: c3.ID, Name: "Mara"}
	_ = s.InsertCharacter(ctx, third)
	if err := s.LinkCharacter(ctx, third, got); err != nil {
		t.Fatal(err)
	}
	if third.Origin() != src.ID {
		t.Fatalf("origin should chain to the first appearance, got %d", third.Origin())
	}
	// Deleting the source story leaves the copy whole.
	if err := s.DeleteStory(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadImage(ctx, got.SheetImage); err != nil {
		t.Fatal("copied sheet must survive deleting the source story")
	}
	// Linking from a character without a sheet leaves the sheet pending.
	nosheet := &Character{StoryID: c3.ID, Name: "Graves", Visual: "tall"}
	_ = s.InsertCharacter(ctx, nosheet)
	fourth := &Character{StoryID: b.ID, Name: "Graves"}
	_ = s.InsertCharacter(ctx, fourth)
	if err := s.LinkCharacter(ctx, fourth, nosheet); err != nil {
		t.Fatal(err)
	}
	if fourth.SheetStatus != ImagePending || fourth.Visual != "tall" {
		t.Fatalf("link without sheet: %+v", fourth)
	}
}
