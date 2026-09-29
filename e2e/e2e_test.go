//go:build e2e

// Package e2e drives the app in a real browser with playwright-go: the main
// flows a writer walks through, end to end, on the offline fake provider.
// Run with `make test-e2e`; the plain suite never compiles this package.
//
// Principles: no arbitrary sleeps — every post-htmx assertion is an
// auto-waiting expect; one fresh browser context per test; the fake
// provider's delay is short but non-zero so polling panels are exercised.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"github.com/lrgalego/pictura/internal/jobs"
	"github.com/lrgalego/pictura/internal/pipeline"
	"github.com/lrgalego/pictura/internal/store"
	"github.com/lrgalego/pictura/web"
)

const script = `THE LIGHTHOUSE KEEPER'S ROBOT

INT. LIGHTHOUSE. NIGHT.

MARA, nine, climbs the last of the spiral stairs in a raincoat two sizes too big. Something whirs in the dark.

MARA: Hello? Is somebody up here?
PIP: Please don't scream. I am very easy to dent.

A small teal robot rolls out from behind the great lamp, one amber eye blinking.

MARA: You're a robot.
PIP: And you are trespassing. Shall we call it even?

EXT. LIGHTHOUSE. CONTINUOUS.

Below, a long black car pulls up on the gravel. GRAVES steps out, silver cane tapping.

GRAVES: Find the robot. The key is inside it.
`

var (
	baseURL string
	browser playwright.Browser
	expect  playwright.PlaywrightAssertions
	refPNG  string
	db      *store.Store
)

func TestMain(m *testing.M) {
	if os.Getenv("PLAYWRIGHT_DOWNLOAD_HOST") == "" {
		os.Setenv("PLAYWRIGHT_DOWNLOAD_HOST", "https://cdn.playwright.dev/dbazure/download/playwright")
	}
	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		log.Fatalf("playwright install: %v", err)
	}
	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("playwright run: %v", err)
	}
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(os.Getenv("HEADFUL") == "")})
	if err != nil {
		log.Fatalf("chromium launch: %v", err)
	}

	dir, _ := os.MkdirTemp("", "pictura-e2e")
	st, err := store.Open(dir, nil)
	if err != nil {
		log.Fatal(err)
	}
	db = st
	runner := jobs.New(st, &pipeline.Fake{Delay: 250 * time.Millisecond}, 3)
	ts := httptest.NewServer(web.Router(web.Deps{Store: st, Jobs: runner, Fake: true}))
	baseURL = ts.URL
	expect = playwright.NewPlaywrightAssertions(15000)

	refPNG = filepath.Join(dir, "ref.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), 60, uint8(y * 4), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	_ = os.WriteFile(refPNG, buf.Bytes(), 0o644)

	code := m.Run()
	ts.Close()
	runner.Wait()
	st.Close()
	browser.Close()
	pw.Stop()
	os.RemoveAll(dir)
	os.Exit(code)
}

func newPage(t *testing.T) playwright.Page {
	t.Helper()
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		Viewport:      &playwright.Size{Width: 1280, Height: 800},
		ReducedMotion: playwright.ReducedMotionReduce,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ctx.NewPage()
	t.Cleanup(func() {
		if t.Failed() {
			if dir := os.Getenv("E2E_SHOTS"); dir != "" {
				_, _ = page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(dir + "/" + t.Name() + ".png"), FullPage: playwright.Bool(true)})
			}
		}
		ctx.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func goto_(t *testing.T, page playwright.Page, url string) {
	t.Helper()
	if _, err := page.Goto(url); err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// signup registers a fresh user through the form, which parks it on the
// pending page, then enables it the way an operator would and opens the
// new-story page.
func signup(t *testing.T, page playwright.Page, name string) {
	t.Helper()
	goto_(t, page, baseURL+"/signup")
	must(t, page.Fill("#username", name))
	must(t, page.Fill("#password", "correct-horse"))
	must(t, page.Click("button[type=submit]"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/pending$`)))
	must(t, expect.Locator(page.Locator("h1")).ToContainText("Almost there"))
	if err := db.SetUserEnabled(context.Background(), name, true); err != nil {
		t.Fatal(err)
	}
	goto_(t, page, baseURL+"/stories/new")
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/stories/new$`)))
}

// createStory submits the script and waits for the cast to be read.
func createStory(t *testing.T, page playwright.Page, title, style string) {
	t.Helper()
	goto_(t, page, baseURL+"/stories/new")
	must(t, page.Fill("#title", title))
	must(t, page.Click("label.style-tile:has-text('"+style+"')"))
	must(t, page.Fill("#script", script))
	must(t, page.Click("button:has-text('Find my characters')"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/characters$`)))
	// The panel polls itself while the editor reads, then settles on the cast.
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToBeVisible())
	must(t, expect.Locator(page.Locator(".tile")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
}

// openCharacter goes from the roster to one character's page.
func openCharacter(t *testing.T, page playwright.Page, name string) {
	t.Helper()
	must(t, page.Locator(".tile:has-text('"+name+"')").Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/characters/\d+$`)))
	must(t, expect.Locator(page.Locator(".char-head__name")).ToContainText(name))
}

// settled waits for the character page to stop polling.
func settled(t *testing.T, page playwright.Page) {
	t.Helper()
	must(t, expect.Locator(page.Locator("#char-panel[hx-trigger]")).ToHaveCount(0))
}

func TestSignupValidationAndLogin(t *testing.T) {
	page := newPage(t)
	goto_(t, page, baseURL+"/")
	must(t, expect.Locator(page.Locator("h1")).ToContainText("Paste a script."))
	must(t, page.Click("a:has-text('Sign up')"))
	must(t, page.Fill("#username", "x!"))
	must(t, page.Fill("#password", "short"))
	must(t, page.Click("button[type=submit]"))
	// OOB field errors land without leaving the page.
	must(t, expect.Locator(page.Locator("#f-password .field__error")).ToContainText("At least 8 characters"))
	must(t, expect.Locator(page.Locator("#f-username .field__error")).ToBeVisible())
	must(t, page.Fill("#username", "browser-user"))
	must(t, page.Fill("#password", "correct-horse"))
	must(t, page.Click("button[type=submit]"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/pending$`)))
	must(t, expect.Locator(page.Locator("h1")).ToContainText("Almost there"))
	// Until an operator enables the account, the app stays out of reach.
	goto_(t, page, baseURL+"/stories")
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/pending$`)))
	if err := db.SetUserEnabled(context.Background(), "browser-user", true); err != nil {
		t.Fatal(err)
	}
	goto_(t, page, baseURL+"/stories")
	must(t, expect.Locator(page.Locator("h1")).ToContainText("Your stories"))

	// Log out from the account menu, then back in.
	must(t, page.Click("button[popovertarget=user-menu]"))
	must(t, page.Click("button:has-text('Log out')"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/$`)))
	goto_(t, page, baseURL+"/login")
	must(t, page.Fill("#username", "browser-user"))
	must(t, page.Fill("#password", "wrong-password"))
	must(t, page.Click("button[type=submit]"))
	must(t, expect.Locator(page.Locator("#f-password .field__error")).ToContainText("Wrong username or password"))
	must(t, page.Fill("#password", "correct-horse"))
	must(t, page.Click("button[type=submit]"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/stories$`)))
	must(t, expect.Locator(page.Locator("h1")).ToContainText("Your stories"))
}

func TestScriptToComic(t *testing.T) {
	page := newPage(t)
	signup(t, page, "walker")
	createStory(t, page, "The Lighthouse Keeper's Robot", "Storybook")

	// Casting phase: no art yet. Every character has its own page.
	must(t, expect.Locator(page.Locator("button:has-text('Draw the character sheets')").First()).ToBeVisible())
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(0))
	openCharacter(t, page, "Mara")
	must(t, expect.Locator(page.Locator(".rail__item")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator(".refs-head")).ToContainText("0 of 5"))

	// Upload a reference from the page.
	must(t, page.Locator(".refs-section input[name=references]").SetInputFiles([]string{refPNG}))
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("attached to"))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".ref__img")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator(".refs-head")).ToContainText("1 of 5"))

	// Edit the words in place.
	must(t, page.Locator("#char-details button:has-text('Edit')").Click())
	must(t, expect.Locator(page.Locator(".details__form")).ToBeVisible())
	must(t, page.Fill("#role", "captain"))
	must(t, page.Click(".details__form button:has-text('Save')"))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".char-head__meta")).ToContainText("captain"))

	// Adjust with notes from the side panel.
	must(t, page.Locator("button:has-text('Adjust with notes')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToContainText("Adjust Mara"))
	must(t, page.Fill("#feedback", "shorter hair"))
	must(t, page.Click(".side-panel button:has-text('Revise')"))
	must(t, expect.Locator(page.Locator(".side-panel")).ToHaveCount(0))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".details__list")).ToContainText("revised: shorter hair"))

	// Next character, then back to the roster; draw the sheets, then storyboard.
	must(t, page.Locator(".char-head__nav a").Last().Click())
	must(t, expect.Locator(page.Locator(".char-head__name")).ToContainText("Pip"))
	must(t, page.Locator(".rail__back").Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/characters$`)))
	must(t, page.Locator("button:has-text('Draw the character sheets')").First().Click())
	must(t, expect.Locator(page.Locator(".job__title")).ToContainText("Drawing character sheets"))
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator("button:has-text('Storyboard the pages')").First()).ToBeVisible())

	// The sheet and its lightbox on the character page; redraw from there.
	openCharacter(t, page, "Mara")
	must(t, page.Locator(".char__sheet-btn").Click())
	must(t, expect.Locator(page.Locator(".lightbox__image")).ToBeVisible())
	must(t, expect.Locator(page.Locator(".lightbox__caption")).ToContainText("character sheet"))
	must(t, page.Keyboard().Press("Escape"))
	must(t, expect.Locator(page.Locator(".lightbox")).ToHaveCount(0))
	must(t, page.Locator("button:has-text('Redraw')").Click())
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("Redrawing Mara"))
	settled(t, page)
	must(t, page.Locator(".rail__back").Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/characters$`)))

	must(t, page.Locator("button:has-text('Storyboard the pages')").First().Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/pages$`)))
	must(t, expect.Locator(page.Locator(".pg").First()).ToBeVisible())
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
	pages, _ := page.Locator(".pg").Count()
	if pages < 1 {
		t.Fatal("no pages planned")
	}

	// Adjust the first page from its dialog.
	must(t, page.Locator(".pg button:has-text('Adjust')").First().Click())
	must(t, expect.Locator(page.Locator(".dialog")).ToContainText("Adjust page 1"))
	must(t, page.Fill("#feedback", "more rain"))
	must(t, page.Click(".dialog button:has-text('Revise page')"))
	must(t, expect.Locator(page.Locator(".dialog")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator(".pg").First()).ToContainText("revised: more rain"))

	// Draw the pages and read the comic.
	must(t, page.Locator("button:has-text('Draw the pages')").First().Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/book$`)))
	must(t, expect.Locator(page.Locator(".leaf__art img")).ToHaveCount(pages))
	must(t, expect.Locator(page.Locator(".toolbar").First()).ToContainText(fmt.Sprintf("%d of %d pages drawn", pages, pages)))
	must(t, page.Locator(".leaf .char__sheet-btn").First().Click())
	must(t, expect.Locator(page.Locator(".lightbox__caption")).ToContainText("Page 1"))
	must(t, page.Keyboard().Press("Escape"))
	must(t, expect.Locator(page.Locator(".lightbox")).ToHaveCount(0))

	// Redraw a page with notes.
	must(t, page.Locator(".leaf button:has-text('Redraw')").First().Click())
	must(t, page.Fill("#feedback", "more drama"))
	must(t, page.Click(".dialog button:has-text('Redraw page')"))
	must(t, expect.Locator(page.Locator(".job__title")).ToContainText("Redrawing the page"))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator(".leaf__art img")).ToHaveCount(pages))

	// Downloads are real files.
	download, err := page.ExpectDownload(func() error {
		return page.Click("a:has-text('Download PDF')")
	})
	must(t, err)
	if download.SuggestedFilename() != "the-lighthouse-keepers-robot.pdf" {
		t.Fatalf("pdf filename: %s", download.SuggestedFilename())
	}
	path, err := download.Path()
	must(t, err)
	data, _ := os.ReadFile(path)
	if !bytes.HasPrefix(data, []byte("%PDF-1.4")) {
		t.Fatal("download is not a PDF")
	}

	// The library shows the finished book with a cover, and the cast page lists everyone.
	must(t, page.Click("a:has-text('Your stories')"))
	must(t, expect.Locator(page.Locator(".book").First()).ToContainText("Step 4 of 4"))
	must(t, expect.Locator(page.Locator(".book__cover img")).ToHaveCount(1))
	must(t, page.Click("a:has-text('Your cast')"))
	must(t, expect.Locator(page.Locator(".castlib__card")).ToHaveCount(3))
}

func TestReuseACharacterAcrossStories(t *testing.T) {
	page := newPage(t)
	signup(t, page, "showrunner")
	createStory(t, page, "Book One", "Classic comic")
	must(t, page.Locator("button:has-text('Draw the character sheets')").First().Click())
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(3))

	createStory(t, page, "Book Two", "Manga")
	must(t, expect.Locator(page.Locator(".tile:has-text('Match in Book One')")).ToHaveCount(3))
	openCharacter(t, page, "Mara")
	must(t, expect.Locator(page.Locator(".suggest")).ToContainText("Same Mara as in Book One?"))
	must(t, page.Locator(".suggest button:has-text('Yes, use this one')").Click())
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("is now the same character"))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".char__sheet-btn img")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator(".char-head")).ToContainText("Also in"))
	must(t, expect.Locator(page.Locator(".suggest")).ToHaveCount(0))

	// The rest of the cast can be picked from the registry panel.
	must(t, page.Locator(".rail__item:has-text('Pip')").Click())
	must(t, expect.Locator(page.Locator(".char-head__name")).ToContainText("Pip"))
	must(t, page.Locator("button:has-text('Reuse from another story')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToContainText("someone you already have?"))
	must(t, expect.Locator(page.Locator(".link-row")).ToHaveCount(3))
	must(t, page.Locator(".link-row:has-text('Pip') button:has-text('Use')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToHaveCount(0))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".char__sheet-btn img")).ToHaveCount(1))
	must(t, page.Locator(".rail__back").Click())
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(2))
	must(t, expect.Locator(page.Locator(".tile:has-text('Match in Book One')")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator("button:has-text('Draw the remaining sheets')").First()).ToBeVisible())

	must(t, page.Click("a:has-text('Your cast')"))
	// Mara and Pip are shared; Graves exists twice (once per book) so far.
	must(t, expect.Locator(page.Locator(".castlib__card")).ToHaveCount(4))
	must(t, expect.Locator(page.Locator(".castlib__card:has-text('Mara')")).ToContainText("Book Two"))
}

func TestUploadAFinishedSheet(t *testing.T) {
	page := newPage(t)
	signup(t, page, "illustrator")
	createStory(t, page, "Own Art", "Noir")
	openCharacter(t, page, "Pip")
	must(t, page.Locator("input[name=sheet]").SetInputFiles([]string{refPNG}))
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("Sheet set for Pip"))
	settled(t, page)
	must(t, expect.Locator(page.Locator(".char__sheet-btn img")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator("label:has-text('Replace with my own')")).ToBeVisible())
	must(t, page.Locator(".rail__back").Click())
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator("button:has-text('Draw the remaining sheets')").First()).ToBeVisible())
	// Deleting the story from its dialog returns to the library.
	must(t, page.Click("button:has-text('Delete')"))
	must(t, expect.Locator(page.Locator(".dialog")).ToContainText("Delete this story?"))
	must(t, page.Click(".dialog button:has-text('Delete story')"))
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/stories$`)))
	must(t, expect.Locator(page.Locator(".empty-state")).ToContainText("No stories yet"))
}

func TestMobileLayoutHasNoHorizontalOverflow(t *testing.T) {
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{Viewport: &playwright.Size{Width: 390, Height: 844}})
	must(t, err)
	t.Cleanup(func() { ctx.Close() })
	page, err := ctx.NewPage()
	must(t, err)
	for _, path := range []string{"/", "/login", "/signup"} {
		goto_(t, page, baseURL+path)
		w, err := page.Evaluate(`document.documentElement.scrollWidth`)
		must(t, err)
		var width int
		switch v := w.(type) {
		case int:
			width = v
		case float64:
			width = int(v)
		default:
			t.Fatalf("unexpected scrollWidth type %T", w)
		}
		if width > 390 {
			t.Fatalf("%s overflows horizontally on mobile: %d", path, width)
		}
	}
}

// drawnComic takes a fresh story from script to drawn book through the UI.
func drawnComic(t *testing.T, page playwright.Page, user string) int {
	t.Helper()
	signup(t, page, user)
	createStory(t, page, "The Lighthouse Keeper's Robot", "Storybook")
	must(t, page.Locator("button:has-text('Draw the character sheets')").First().Click())
	must(t, expect.Locator(page.Locator("img.tile__avatar")).ToHaveCount(3))
	must(t, page.Locator("button:has-text('Storyboard the pages')").First().Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/pages$`)))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
	pages, _ := page.Locator(".pg").Count()
	must(t, page.Locator("button:has-text('Draw the pages')").First().Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/book$`)))
	must(t, expect.Locator(page.Locator(".leaf__art img")).ToHaveCount(pages))
	return pages
}

func number(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	t.Fatalf("not a number: %T %v", v, v)
	return 0
}

func TestReadToMe(t *testing.T) {
	page := newPage(t)
	pages := drawnComic(t, page, "reader")

	// Drawing the book prepares the narration; the reader opens on a cover.
	must(t, page.Locator("a:has-text('Read to me')").Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/read$`)))
	must(t, expect.Locator(page.Locator(".reader__status")).ToContainText("ready when you are", playwright.LocatorAssertionsToContainTextOptions{Timeout: playwright.Float(30000)}))
	must(t, page.Locator("[data-act=start]").Click())
	must(t, expect.Locator(page.Locator(".reader__cover")).ToBeHidden())
	must(t, expect.Locator(page.Locator(".reader__pageno")).ToHaveText(fmt.Sprintf("Page 1 of %d", pages)))

	// A voice speaks, the balloon is spotlit and the highlighter walks
	// across the words, always inside the balloon.
	must(t, expect.Locator(page.Locator(".reader__hl")).ToBeVisible())
	must(t, expect.Locator(page.Locator(".reader__who")).Not().ToBeEmpty())
	must(t, expect.Locator(page.Locator(".reader__ctl--play.is-playing")).ToHaveCount(1))
	seen := map[string]bool{}
	for i := 0; i < 40 && len(seen) < 3; i++ {
		pos, err := page.Evaluate(`(() => {
			const h = document.querySelector('.reader__hl'), s = document.querySelector('.reader__spot');
			const a = h.getBoundingClientRect(), b = s.getBoundingClientRect();
			return { key: h.style.left + ',' + h.style.top, inside: a.left >= b.left - 2 && a.right <= b.right + 2 && a.top >= b.top - 2 && a.bottom <= b.bottom + 2 };
		})()`)
		must(t, err)
		m := pos.(map[string]any)
		if m["inside"] != true {
			t.Fatalf("the highlighted word left its balloon: %v", m)
		}
		seen[m["key"].(string)] = true
		page.WaitForTimeout(100)
	}
	if len(seen) < 3 {
		t.Fatalf("the highlight should move from word to word, saw %d positions", len(seen))
	}
	if dir := os.Getenv("E2E_SHOTS"); dir != "" {
		_, _ = page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(dir + "/reader-playing.png")})
	}

	// Pause and resume; speed; a tap on a balloon replays it.
	must(t, page.Locator("[data-act=play]").Click())
	must(t, expect.Locator(page.Locator(".reader__ctl--play.is-playing")).ToHaveCount(0))
	must(t, page.Locator("[data-act=speed]").Click())
	must(t, expect.Locator(page.Locator("[data-act=speed]")).ToHaveText("1.25×"))
	must(t, page.Locator(".reader__hot").First().Click())
	must(t, expect.Locator(page.Locator(".reader__ctl--play.is-playing")).ToHaveCount(1))

	// Turn to the last page by keyboard and let it play out to the end.
	for i := 1; i < pages; i++ {
		must(t, page.Keyboard().Press("ArrowDown"))
		must(t, expect.Locator(page.Locator(".reader__pageno")).ToHaveText(fmt.Sprintf("Page %d of %d", i+1, pages)))
	}
	must(t, expect.Locator(page.Locator(".reader__end")).ToBeVisible(playwright.LocatorAssertionsToBeVisibleOptions{Timeout: playwright.Float(45000)}))
	must(t, expect.Locator(page.Locator(".reader__end")).ToContainText("The End"))
	must(t, page.Locator("[data-act=again]").Click())
	must(t, expect.Locator(page.Locator(".reader__pageno")).ToHaveText(fmt.Sprintf("Page 1 of %d", pages)))
	must(t, expect.Locator(page.Locator(".reader__ctl--play.is-playing")).ToHaveCount(1))
}

func TestReadToMeOnAPhone(t *testing.T) {
	desk := newPage(t)
	drawnComic(t, desk, "pocket")
	cookies, err := desk.Context().Cookies()
	must(t, err)
	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{Viewport: &playwright.Size{Width: 390, Height: 844}, HasTouch: playwright.Bool(true), IsMobile: playwright.Bool(true), ReducedMotion: playwright.ReducedMotionReduce})
	must(t, err)
	t.Cleanup(func() { ctx.Close() })
	var add []playwright.OptionalCookie
	for _, c := range cookies {
		add = append(add, c.ToOptionalCookie())
	}
	must(t, ctx.AddCookies(add))
	page, err := ctx.NewPage()
	must(t, err)
	goto_(t, page, desk.URL()+"/../read")
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/read$`)))
	must(t, expect.Locator(page.Locator(".reader__status")).ToContainText("ready when you are", playwright.LocatorAssertionsToContainTextOptions{Timeout: playwright.Float(30000)}))
	w, err := page.Evaluate(`document.documentElement.scrollWidth`)
	must(t, err)
	if number(t, w) > 390 {
		t.Fatalf("the reader overflows on a phone: %v", w)
	}
	// Zoom follows the balloon on small screens.
	must(t, expect.Locator(page.Locator("[data-act=zoom]")).ToHaveAttribute("aria-pressed", "true"))
	must(t, page.Locator("[data-act=start]").Tap())
	must(t, expect.Locator(page.Locator(".reader__sheet.is-zoomed")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator(".reader__hl")).ToBeVisible())
	if dir := os.Getenv("E2E_SHOTS"); dir != "" {
		_, _ = page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(dir + "/reader-phone.png")})
	}
}

func TestPickAVoice(t *testing.T) {
	page := newPage(t)
	signup(t, page, "caster")
	createStory(t, page, "The Lighthouse Keeper's Robot", "Storybook")

	// Casting gave everyone a voice and the story a narrator.
	must(t, expect.Locator(page.Locator(".voice-line--narrator")).ToContainText("Narrator: George"))
	must(t, expect.Locator(page.Locator(".tile").First()).ToContainText("voice: "))

	// Pick another voice for Mara from her page; hear one on the way.
	openCharacter(t, page, "Mara")
	before, err := page.Locator("#char-voice b").TextContent()
	must(t, err)
	must(t, page.Locator("#char-voice button:has-text('Change')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToContainText("A voice for Mara"))
	must(t, expect.Locator(page.Locator(".voice-row--current")).ToHaveCount(1))
	must(t, page.Locator(".voice-row [data-listen]").Nth(3).Click())
	must(t, expect.Locator(page.Locator(".voice-row [data-listen].is-playing, .voice-row [data-listen].is-loading")).ToHaveCount(1))
	row := page.Locator(".voice-row:not(.voice-row--current)").Last()
	name, err := row.Locator("b").TextContent()
	must(t, err)
	must(t, row.Locator("button:has-text('Use')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator("#char-voice b")).ToHaveText("Voice: "+name))
	if before == "Voice: "+name {
		t.Fatal("the voice did not change")
	}
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("now speaks as "+name))

	// The narrator is changed from the cast.
	must(t, page.Locator(".rail__back").Click())
	must(t, page.Locator("#narrator-voice button:has-text('Change')").Click())
	must(t, expect.Locator(page.Locator(".side-panel")).ToContainText("A voice for the narrator"))
	must(t, page.Locator(".voice-row:has-text('Bill') button:has-text('Use')").Click())
	must(t, expect.Locator(page.Locator(".voice-line--narrator")).ToContainText("Narrator: Bill"))
}

func TestSoundStudioFlow(t *testing.T) {
	page := newPage(t)
	drawnComic(t, page, "studio")
	shots := os.Getenv("E2E_SHOTS")
	shot := func(name string) {
		if shots != "" {
			_, _ = page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(shots + "/" + name + ".png"), FullPage: playwright.Bool(true)})
		}
	}
	// Narration is prepared after drawing; the studio is off by default.
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0, playwright.LocatorAssertionsToHaveCountOptions{Timeout: playwright.Float(30000)}))
	must(t, expect.Locator(page.Locator(".leaf__studio")).ToHaveCount(0))
	must(t, page.Locator(".studio-switch .label").Click())
	must(t, expect.Locator(page.Locator(".leaf__studio").First()).ToBeVisible())
	shot("book-studio-on")

	// Into the studio of page one: a cue card per line, markers on the art.
	must(t, page.Locator(".leaf__studio").First().Click())
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/studio/\d+$`)))
	must(t, expect.Locator(page.Locator(".cue").First()).ToBeVisible())
	cues, _ := page.Locator(".cue").Count()
	markers, _ := page.Locator(".studio__marker").Count()
	if cues == 0 || cues != markers {
		t.Fatalf("%d cues, %d markers", cues, markers)
	}
	// Hovering a cue lights its balloon on the page.
	must(t, page.Locator(".cue").First().Hover())
	must(t, expect.Locator(page.Locator(".studio__outline.is-lit")).ToHaveCount(1))
	shot("studio")

	// Direct a line with a suggestion: it becomes a sound effect.
	must(t, page.Locator(".cue__direct > summary").First().Click())
	must(t, page.Locator(".cue__idea:has-text('Make it a sound effect')").First().Click())
	must(t, expect.Locator(page.Locator(".cue__direct[open] textarea")).ToHaveValue("Make it a sound effect"))
	shot("studio-direct")
	must(t, page.Locator(".cue__direct[open] button[type=submit]").Click())
	must(t, expect.Locator(page.Locator(".cue").First()).ToContainText("Sound effect", playwright.LocatorAssertionsToContainTextOptions{Timeout: playwright.Float(20000)}))
	must(t, expect.Locator(page.Locator(".cue").First()).ToContainText("Your note: Make it a sound effect"))
	shot("studio-directed")

	// Simple mode with a line that failed: a plain Try again on the card.
	must(t, page.Locator(".studio__back").Click())
	must(t, page.Locator(".studio-switch .label").Click())
	must(t, expect.Locator(page.Locator(".leaf__studio")).ToHaveCount(0))
	u := page.URL()
	var storyID int64
	fmt.Sscanf(u[strings.Index(u, "/stories/")+len("/stories/"):], "%d", &storyID)
	pages, _ := db.Pages(context.Background(), storyID)
	lines, _ := db.PageLines(context.Background(), pages[0].ID)
	_ = db.SetLineAudio(context.Background(), lines[1].ID, "", "", lines[1].Words)
	_ = db.SetLineError(context.Background(), lines[1].ID, "elevenlabs: the voice service returned no audio")
	_, err := page.Reload()
	must(t, err)
	must(t, expect.Locator(page.Locator(".leaf__sound--trouble")).ToContainText("1 line couldn't be voiced"))
	shot("book-trouble")
	must(t, page.Locator(".leaf__sound--trouble button").Click())
	must(t, expect.Locator(page.Locator(".leaf__sound--trouble")).ToHaveCount(0, playwright.LocatorAssertionsToHaveCountOptions{Timeout: playwright.Float(20000)}))
}
