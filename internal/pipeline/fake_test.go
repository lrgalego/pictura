package pipeline

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
	"time"
)

const screenplay = `THE LIGHTHOUSE

MARA: Hello? Is somebody up here?
PIP (whirring): Please don't scream.
NARRATOR: Below, a car pulled up.
GRAVES: Find the robot.
`

func TestFakeNames(t *testing.T) {
	got := names(screenplay)
	if strings.Join(got, ",") != "Mara,Pip,Graves" {
		t.Fatalf("speaker names: %v", got)
	}
	prose := "Once upon a time Ada met Bruno by the river. Ada laughed and Bruno waved. Ada ran. The river ran too."
	got = names(prose)
	if len(got) == 0 || got[0] != "Ada" {
		t.Fatalf("prose names should rank capitalized words: %v", got)
	}
	if got := names("nothing here"); strings.Join(got, ",") != "The Hero,The Rival" {
		t.Fatalf("fallback names: %v", got)
	}
}

func TestFakeChatShapes(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	var an Analysis
	if err := f.ChatJSON(ctx, "", "SCRIPT:\n"+screenplay, nil, "analysis", nil, &an); err != nil {
		t.Fatal(err)
	}
	if an.Title != "The Lighthouse" || len(an.Characters) != 3 || an.Characters[0].Name != "Mara" {
		t.Fatalf("analysis: %+v", an)
	}
	var cast struct {
		Characters []CharacterSpec `json:"characters"`
	}
	user := "CURRENT CAST:\n- Mara\n\nWRITER'S FEEDBACK:\nred scarf\n\nSCRIPT (for reference):\n" + screenplay
	if err := f.ChatJSON(ctx, "", user, nil, "characters", nil, &cast); err != nil {
		t.Fatal(err)
	}
	if len(cast.Characters) != 3 || !strings.Contains(cast.Characters[0].Wardrobe, "revised: red scarf") {
		t.Fatalf("cast revision: %+v", cast.Characters)
	}
	single := "CHARACTER:\n- Graves (antagonist, 60)\n  Appearance: x\n\nWRITER'S FEEDBACK:\n(no written notes; match the attached reference images)\n"
	if err := f.ChatJSON(ctx, "", single, []Image{{PNG: []byte("x")}}, "characters", nil, &cast); err != nil {
		t.Fatal(err)
	}
	if len(cast.Characters) != 1 || cast.Characters[0].Name != "Graves" || !strings.Contains(cast.Characters[0].Visual, "matched to 1 reference image") || strings.Contains(cast.Characters[0].Wardrobe, "revised") {
		t.Fatalf("single revision: %+v", cast.Characters)
	}
	var pages struct {
		Pages []PageSpec `json:"pages"`
	}
	if err := f.ChatJSON(ctx, "", "WRITER'S FEEDBACK:\nfaster\n\nSCRIPT (for reference):\n"+screenplay, nil, "pages", nil, &pages); err != nil {
		t.Fatal(err)
	}
	if len(pages.Pages) == 0 || !strings.Contains(pages.Pages[0].Summary, "revised: faster") || len(pages.Pages[0].Panels) < 2 {
		t.Fatalf("pages: %+v", pages.Pages)
	}
	var page struct {
		Page PageSpec `json:"page"`
	}
	if err := f.ChatJSON(ctx, "", "PAGE 1:\n...\n\nWRITER'S FEEDBACK:\nmore rain\n\nSCRIPT (for reference):\n"+screenplay, nil, "page", nil, &page); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Page.Summary, "revised: more rain") {
		t.Fatalf("page: %+v", page.Page)
	}
	if err := f.ChatJSON(ctx, "", "x", nil, "unknown", nil, &page); err == nil {
		t.Fatal("unknown schema should fail")
	}
	if p := breakdown("", nil); len(p) != 1 {
		t.Fatalf("empty script still yields one page: %+v", p)
	}
}

func TestFakeImagesAndDelay(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	sheet, err := f.GenerateImage(ctx, "Character reference sheet for Mara", SheetSize)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(sheet))
	if err != nil || img.Bounds().Dx() != 768 {
		t.Fatalf("sheet placeholder: %v %v", img.Bounds(), err)
	}
	page, err := f.EditImage(ctx, strings.Repeat("A finished comic page ", 10), [][]byte{sheet}, PageSize)
	if err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(page))
	if img.Bounds().Dx() != 512 || img.Bounds().Dy() != 768 {
		t.Fatalf("page placeholder: %v", img.Bounds())
	}
	slow := &Fake{Delay: time.Hour}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slow.GenerateImage(cancelled, "p", ""); err == nil {
		t.Fatal("cancelled context should stop the fake")
	}
	if _, err := slow.EditImage(cancelled, "p", nil, ""); err == nil {
		t.Fatal("cancelled context should stop the fake")
	}
	if err := slow.ChatJSON(cancelled, "", "", nil, "analysis", nil, nil); err == nil {
		t.Fatal("cancelled context should stop the fake")
	}
	quick := &Fake{Delay: time.Millisecond}
	if _, err := quick.GenerateImage(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
	if titleCase("the KEEPER's robot") != "The Keeper's Robot" {
		t.Fatalf("titleCase: %q", titleCase("the KEEPER's robot"))
	}
	if clip("a b  c", 3) != "a…" || clip("ab", 5) != "ab" {
		t.Fatal("clip")
	}
}
