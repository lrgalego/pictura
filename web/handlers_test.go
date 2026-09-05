package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lrgalego/story-time/internal/store"
)

// finished walks a story through every step on the fake provider.
func (e *env) finished(t *testing.T) (string, string) {
	t.Helper()
	resp, _ := e.post("/stories", url.Values{"title": {"Book"}, "script": {script}, "style": {"comic"}}, false)
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	e.post(base+"/characters/draw", nil, true)
	e.waitIdle()
	e.post(base+"/characters/approve", nil, true)
	e.waitIdle()
	e.post(base+"/pages/approve", nil, true)
	e.waitIdle()
	_, body := e.get(base + "/pages")
	return base, firstPageID(body)
}

func firstPageID(body string) string {
	i := strings.Index(body, `id="page-`)
	rest := body[i+len(`id="page-`):]
	return rest[:strings.Index(rest, `"`)]
}

func TestNotFoundAndErrors(t *testing.T) {
	e := newEnv(t)
	e.signup("notfound")
	for _, p := range []string{"/stories/999", "/stories/999/characters", "/stories/999/pages", "/stories/999/book", "/stories/999/script", "/stories/999/delete", "/media/nope.png", "/media/t/nope.png"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d", p, resp.StatusCode)
		}
	}
	base, pid := e.finished(t)
	for _, p := range []string{base + "/characters/999/adjust", base + "/characters/999/edit", base + "/characters/999/link", base + "/pages/999/adjust", base + "/book/999/redraw"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{base + "/refs/999/delete", base + "/characters/999/redraw", base + "/characters/999/refs", base + "/characters/999/sheet", base + "/characters/999/link", base + "/pages/999/adjust", base + "/book/999/redraw"} {
		resp, _ := e.post(p, url.Values{"source": {"1"}}, true)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("POST %s: expected 404, got %d", p, resp.StatusCode)
		}
	}
	_ = pid
	// Unauthenticated htmx requests get an HX-Redirect instead of HTML.
	other := &env{t: t, srv: e.srv, runner: e.runner, client: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	req, _ := http.NewRequest("GET", e.srv.URL+"/stories", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := other.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("htmx without session: %v %v", resp, err)
	}
	// Logged-in users skip the auth pages.
	for _, p := range []string{"/login", "/signup"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s while logged in should redirect, got %d", p, resp.StatusCode)
		}
	}
	// /stories/{id} redirects to the current step; the home page still serves.
	resp, _ = e.get(base)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != base+"/book" {
		t.Fatalf("story redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := e.get("/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Your stories") {
		t.Fatal("home for a logged-in user")
	}
	// Steps not reached yet redirect back.
	resp, _ = e.post("/stories", url.Values{"script": {script}, "style": {"comic"}}, false)
	fresh := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	for _, p := range []string{fresh + "/pages", fresh + "/book"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s before its time should redirect, got %d", p, resp.StatusCode)
		}
	}
	// Full-page login with a bad next falls back to the library.
	e.post("/logout", nil, false)
	resp, _ = e.post("/login", url.Values{"username": {"notfound"}, "password": {"correct-horse"}, "next": {"http://evil"}}, false)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/stories" {
		t.Fatalf("login next guard: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestScriptEditing(t *testing.T) {
	e := newEnv(t)
	e.signup("editor")
	base, _ := e.finished(t)
	resp, body := e.get(base + "/script")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Save and read it again") {
		t.Fatalf("script page: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/script", url.Values{"title": {"Book"}, "script": {"short"}, "style": {"comic"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "at least a few paragraphs") {
		t.Fatalf("short script: %d", resp.StatusCode)
	}
	// Unchanged script: no re-analysis, characters are kept.
	resp, _ = e.post(base+"/script", url.Values{"title": {"Renamed"}, "script": {script}, "style": {"comic"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save unchanged: %d", resp.StatusCode)
	}
	_, body = e.get(base + "/characters")
	if !strings.Contains(body, "Renamed") || !strings.Contains(body, "Storyboard") {
		t.Fatal("renaming must keep the cast and sheets")
	}
	// Changed style: re-read, cast rebuilt, step reset.
	e.hold()
	resp, _ = e.post(base+"/script", url.Values{"title": {"Renamed"}, "script": {script}, "style": {"noir"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save changed: %d", resp.StatusCode)
	}
	// While busy, saving again is refused.
	resp, body = e.post(base+"/script", url.Values{"title": {"x"}, "script": {script + "\nmore"}, "style": {"noir"}}, false)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "still being worked on") {
		t.Fatalf("busy save: %d", resp.StatusCode)
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if !strings.Contains(body, "Draw the character sheets") {
		t.Fatal("a re-read story is back in the casting phase")
	}
}

func TestCharacterEditDialogAndSave(t *testing.T) {
	e := newEnv(t)
	e.signup("tweaker")
	base, _ := e.finished(t)
	_, body := e.get(base + "/characters")
	cid := firstCharacterID(body)
	resp, body := e.get(base + "/characters/" + cid + "/edit")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="char-edit-fields"`) {
		t.Fatalf("edit dialog: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/characters/"+cid+"/edit", url.Values{"name": {""}, "visual": {"x"}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "A name and an appearance are required") {
		t.Fatalf("invalid edit: %d", resp.StatusCode)
	}
	// Words-only change: saved, sheet kept.
	resp, body = e.post(base+"/characters/"+cid+"/edit", url.Values{"name": {"Mara"}, "role": {"lead"}, "age": {"12 years old"}, "visual": {"Small and wiry with a round face, freckles across the nose, wide hazel eyes and a mop of curly copper hair that never stays put."}, "wardrobe": {"An oversized mustard-yellow raincoat over a striped tee, rolled jeans and red rubber boots."}, "items": {"A brass compass on a string and a battered satchel."}, "personality": {"calm"}, "redraw": {"1"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Saving Mara") {
		t.Fatalf("save words: %d %s", resp.StatusCode, body[:200])
	}
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if !strings.Contains(body, "lead") || !strings.Contains(body, "calm") {
		t.Fatal("edit should be applied")
	}
	// A look change while a revision runs is queued, not refused, and wins.
	e.hold()
	resp, body = e.postMultipart(base+"/characters/"+cid+"/adjust", map[string]string{"feedback": "older"}, nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising Mara") {
		t.Fatalf("adjust: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/characters/"+cid+"/edit", url.Values{"name": {"Mara"}, "visual": {"tall now"}, "redraw": {"1"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Queued behind the change already running") {
		t.Fatalf("queued edit: %d %s", resp.StatusCode, body[:300])
	}
	// The card keeps showing the running revision (not the queued edit) and polls.
	if !strings.Contains(body, "Revising the character") || strings.Contains(body, "Waiting its turn") || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatal("the card should show the running job and keep polling")
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if !strings.Contains(body, "tall now") || strings.Contains(body, "Waiting its turn") {
		t.Fatal("the queued edit should be applied after the revision")
	}
	// Plain redraw and the lightbox.
	resp, body = e.post(base+"/characters/"+cid+"/redraw", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Redrawing Mara") {
		t.Fatalf("redraw: %d", resp.StatusCode)
	}
	e.waitIdle()
	resp, body = e.get(base + "/characters/" + cid + "/view?img=1")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "character sheet") {
		t.Fatalf("lightbox: %d", resp.StatusCode)
	}
	// Cast-level adjust: empty refused, then runs; story buttons disable while it is active.
	resp, body = e.post(base+"/characters/adjust", url.Values{"feedback": {""}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty cast adjust: %d", resp.StatusCode)
	}
	e.hold()
	resp, body = e.post(base+"/characters/adjust", url.Values{"feedback": {"everyone taller"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising the cast") || !strings.Contains(body, "Adjust the cast</button>") {
		t.Fatalf("cast adjust: %d", resp.StatusCode)
	}
	if !strings.Contains(body, `disabled`) {
		t.Fatal("story-level buttons disable while the cast is being revised")
	}
	// A character change during a story-level job queues behind it.
	resp, body = e.postMultipart(base+"/characters/"+cid+"/adjust", map[string]string{"feedback": "hat"}, nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Waiting its turn") {
		t.Fatalf("character work should queue behind the story job: %d", resp.StatusCode)
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if !strings.Contains(body, "revised: hat") {
		t.Fatal("the queued character change ran after the cast revision")
	}
}

func TestReferenceEdgeCases(t *testing.T) {
	e := newEnv(t)
	e.signup("references")
	resp, _ := e.post("/stories", url.Values{"script": {script}, "style": {"comic"}}, false)
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	_, body := e.get(base + "/characters")
	cid := firstCharacterID(body)
	// No file chosen.
	resp, body = e.postMultipart(base+"/characters/"+cid+"/refs", nil, nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "choose at least one image") {
		t.Fatalf("no file: %d", resp.StatusCode)
	}
	// Too many files.
	many := map[string][]byte{}
	for i := 0; i < 9; i++ {
		many[strings.Repeat("a", i+1)+".png"] = tinyPNG()
	}
	resp, body = e.postMultipart(base+"/characters/"+cid+"/refs", nil, many, true)
	if !strings.Contains(body, "at most 8") {
		t.Fatal("more than 8 files should be refused")
	}
	// Oversized file.
	big := map[string][]byte{"huge.png": make([]byte, 21<<20)}
	resp, body = e.postMultipart(base+"/characters/"+cid+"/refs", nil, big, true)
	if !strings.Contains(body, "over 20 MB") {
		t.Fatalf("oversized file should be refused: %s", body[:200])
	}
	// Adjust dialog upload error paths.
	resp, body = e.postMultipart(base+"/characters/"+cid+"/adjust", map[string]string{"feedback": "x"}, map[string][]byte{"bad.txt": []byte("no")}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "not a readable image") {
		t.Fatalf("adjust with bad file: %d", resp.StatusCode)
	}
	resp, body = e.postMultipart(base+"/characters/"+cid+"/adjust", map[string]string{"feedback": "x"}, many, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "at most 8") {
		t.Fatalf("adjust with too many files: %d", resp.StatusCode)
	}
	// Sheet upload: two files is not one.
	resp, body = e.postMultipartField(base+"/characters/"+cid+"/sheet", nil, "sheet", map[string][]byte{"a.png": tinyPNG(), "b.png": tinyPNG()}, true)
	if !strings.Contains(body, "choose one image") {
		t.Fatal("two sheets should be refused")
	}
	// Uploads during a running change are queued behind it.
	e.hold()
	resp, _ = e.postMultipart(base+"/characters/"+cid+"/adjust", map[string]string{"feedback": "slow"}, nil, true)
	resp, body = e.postMultipart(base+"/characters/"+cid+"/refs", nil, map[string][]byte{"a.png": tinyPNG()}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Queued behind") {
		t.Fatalf("refs while busy should queue: %d", resp.StatusCode)
	}
	resp, body = e.postMultipartField(base+"/characters/"+cid+"/sheet", nil, "sheet", map[string][]byte{"a.png": tinyPNG()}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Queued behind") {
		t.Fatalf("sheet while busy should queue: %d", resp.StatusCode)
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if strings.Count(body, `character sheet"`) != 1 {
		t.Fatal("the queued sheet upload should have landed")
	}
	// Deleting a reference via a plain form (from the script page) redirects.
	resp, _ = e.postMultipart(base+"/characters/"+cid+"/refs", nil, map[string][]byte{"a.png": tinyPNG()}, true)
	e.waitIdle()
	_, body = e.get(base + "/characters")
	rid := firstRefID(body)
	resp, _ = e.post(base+"/refs/"+rid+"/delete", nil, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("plain delete: %d", resp.StatusCode)
	}
}

func TestPagesAndBookFlows(t *testing.T) {
	e := newEnv(t)
	e.signup("pager")
	base, pid := e.finished(t)

	// Page adjust dialog, empty feedback, feedback, busy.
	resp, body := e.get(base + "/pages/" + pid + "/adjust")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Adjust page 1") {
		t.Fatalf("page adjust dialog: %d", resp.StatusCode)
	}
	resp, _ = e.post(base+"/pages/"+pid+"/adjust", url.Values{"feedback": {""}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty page feedback: %d", resp.StatusCode)
	}
	e.hold()
	resp, body = e.post(base+"/pages/"+pid+"/adjust", url.Values{"feedback": {"split it"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising page 1") {
		t.Fatalf("page adjust: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/pages/"+pid+"/adjust", url.Values{"feedback": {"again"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Waiting its turn") {
		t.Fatal("a second page adjustment queues behind the first")
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/pages")
	if !strings.Contains(body, "Will be redrawn") || !strings.Contains(body, "Draw the changed pages") || !strings.Contains(body, "revised: again") {
		t.Fatal("both adjustments applied in order and the page is marked for redraw")
	}

	// Whole-plan adjust: dialog, empty, run, busy.
	resp, body = e.get(base + "/pages/adjust")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Adjust the whole breakdown") {
		t.Fatalf("pages adjust dialog: %d", resp.StatusCode)
	}
	resp, _ = e.post(base+"/pages/adjust", url.Values{"feedback": {""}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty pages feedback: %d", resp.StatusCode)
	}
	e.hold()
	resp, body = e.post(base+"/pages/adjust", url.Values{"feedback": {"eight pages"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising the pages") {
		t.Fatalf("pages adjust: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "Revising the pages") || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatal("the panel polls while the plan is revised")
	}
	e.release()
	e.waitIdle()
	// Start over rebuilds the plan.
	resp, body = e.post(base+"/pages/restart", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Storyboarding again") {
		t.Fatalf("restart: %d", resp.StatusCode)
	}
	e.waitIdle()

	// Book: draw missing pages, redraw with notes, busy states, panel fragment.
	_, body = e.get(base + "/book")
	if !strings.Contains(body, "Draw the missing pages") || !strings.Contains(body, "0 of") {
		t.Fatal("book should offer to draw the missing pages")
	}
	e.hold()
	resp, body = e.post(base+"/book/draw", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Drawing the missing pages") {
		t.Fatalf("book draw: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "Drawing your pages") {
		t.Fatal("the job bar shows the running render")
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/pages")
	pid = firstPageID(body)
	resp, body = e.get(base + "/book/" + pid + "/redraw")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Redraw page 1") {
		t.Fatalf("redraw dialog: %d", resp.StatusCode)
	}
	e.hold()
	resp, body = e.post(base+"/book/"+pid+"/redraw", url.Values{"feedback": {"more drama"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Redrawing page 1") {
		t.Fatalf("redraw: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/book/"+pid+"/redraw", url.Values{"feedback": {""}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Waiting its turn") {
		t.Fatal("a second redraw queues behind the first")
	}
	for _, p := range []string{base + "/book/panel", base + "/pages/panel", base + "/characters/panel"} {
		resp, body = e.get(p)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="step-panel"`) || !strings.Contains(body, `hx-trigger="every 2s"`) {
			t.Fatalf("%s while busy should poll: %d", p, resp.StatusCode)
		}
	}
	e.release()
	e.waitIdle()
	// Lightbox index clamping and downloads.
	resp, body = e.get(base + "/book/view?img=99")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Page 1") {
		t.Fatalf("lightbox clamp: %d", resp.StatusCode)
	}
	resp, _ = e.get(base + "/download.pdf")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Disposition"), "book.pdf") {
		t.Fatalf("pdf download: %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	// Delete dialog renders; media serves thumbnails with cache headers.
	resp, body = e.get(base + "/delete")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Delete this story?") {
		t.Fatalf("delete dialog: %d", resp.StatusCode)
	}
	_, body = e.get(base + "/book")
	i := strings.Index(body, "/media/t/")
	name := body[i+len("/media/t/") : i+len("/media/t/")+strings.Index(body[i+len("/media/t/"):], `"`)]
	resp, _ = e.get("/media/t/" + name)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("thumb: %d", resp.StatusCode)
	}
	resp, _ = e.get("/media/" + name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("media: %d", resp.StatusCode)
	}
}

func TestBookWithoutArtAndEmptyStates(t *testing.T) {
	e := newEnv(t)
	e.signup("emptiness")
	resp, _ := e.post("/stories", url.Values{"script": {script}, "style": {"comic"}}, false)
	base := strings.TrimSuffix(resp.Header.Get("Location"), "/characters")
	e.waitIdle()
	// No pages drawn yet: downloads are 404, lightbox is empty.
	resp, _ = e.get(base + "/download.pdf")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("pdf without pages: %d", resp.StatusCode)
	}
	resp, body := e.get(base + "/book/view")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "lightbox") {
		t.Fatalf("empty lightbox: %d", resp.StatusCode)
	}
	resp, body = e.get(base + "/characters/1/view")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty sheet lightbox: %d", resp.StatusCode)
	}
	// Zip works with only the script.
	resp, _ = e.get(base + "/download.zip")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("zip: %d", resp.StatusCode)
	}
	// Job error is shown on the panel.
	_, body = e.get(base + "/characters")
	if strings.Contains(body, "The last step failed") {
		t.Fatal("no failure yet")
	}
	if fileStem("  ") != "story" || fileStem("The Lighthouse Keeper's Robot!") != "the-lighthouse-keepers-robot" {
		t.Fatalf("fileStem: %q", fileStem("The Lighthouse Keeper's Robot!"))
	}
	if plural(1, "page", "pages") != "1 page" || plural(2, "page", "pages") != "2 pages" {
		t.Fatal("plural")
	}
	if titleOr(&store.Story{}) != "Untitled story" {
		t.Fatal("titleOr")
	}
	if sameName("MARA,", "mara quinn") != true || sameName("Al", "Al Capone") != false || sameName("", "x") {
		t.Fatal("sameName")
	}
}

func TestTwoCharactersCanBeEditedAtOnce(t *testing.T) {
	e := newEnv(t)
	e.signup("multitasker")
	base, _ := e.finished(t)
	_, body := e.get(base + "/characters")
	ids := allCharacterIDs(body)
	e.hold()
	resp, body := e.postMultipart(base+"/characters/"+ids[0]+"/adjust", map[string]string{"feedback": "older"}, nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising Mara") {
		t.Fatalf("first adjust: %d", resp.StatusCode)
	}
	// The second character runs alongside the first.
	resp, body = e.postMultipart(base+"/characters/"+ids[1]+"/adjust", map[string]string{"feedback": "younger"}, nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Revising Pip") {
		t.Fatalf("second adjust should be accepted: %d %s", resp.StatusCode, body[:300])
	}
	if strings.Count(body, "char--working") != 2 || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, "Other characters stay editable") {
		t.Fatalf("two working cards expected:\n%s", body[:500])
	}
	// The third card is untouched and its edit runs right away; a story-level
	// action queues behind the character jobs instead of being refused.
	resp, body = e.post(base+"/characters/"+ids[2]+"/edit", url.Values{"name": {"Graves"}, "visual": {"tall"}}, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Saving Graves") || strings.Contains(body, "Queued behind") {
		t.Fatalf("third character edit: %d", resp.StatusCode)
	}
	resp, body = e.post(base+"/characters/draw", nil, true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Waiting its turn: Drawing character sheets") {
		t.Fatalf("draw all should queue: %d %s", resp.StatusCode, body[:300])
	}
	if strings.Contains(body, "The last change to") {
		t.Fatal("no failure yet")
	}
	e.release()
	e.waitIdle()
	_, body = e.get(base + "/characters")
	if strings.Contains(body, "char--working") || strings.Contains(body, `hx-trigger="every 2s"`) || strings.Contains(body, "Waiting its turn") {
		t.Fatal("nothing should be working after the jobs finish")
	}
	if !strings.Contains(body, "revised: older") || !strings.Contains(body, "revised: younger") || !strings.Contains(body, ">tall<") && !strings.Contains(body, "tall") {
		t.Fatal("all changes should have been applied")
	}
}

func TestSessionCookieIsSecureBehindTLS(t *testing.T) {
	e := newEnv(t)
	for i, tc := range []struct {
		proto  string
		secure bool
	}{{"", false}, {"http", false}, {"https", true}, {"HTTPS", true}} {
		body := url.Values{"username": {fmt.Sprintf("tls-user-%d", i)}, "password": {"correct-horse"}}.Encode()
		req, _ := http.NewRequest("POST", e.srv.URL+"/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.proto != "" {
			req.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		resp, err := e.client.Do(req)
		if err != nil || resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("signup %q: %v %v", tc.proto, resp, err)
		}
		var found bool
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie {
				found = true
				if c.Secure != tc.secure || !c.HttpOnly {
					t.Fatalf("proto %q: Secure=%v HttpOnly=%v", tc.proto, c.Secure, c.HttpOnly)
				}
			}
		}
		if !found {
			t.Fatalf("proto %q: no session cookie", tc.proto)
		}
		e.post("/logout", nil, false)
	}
}
