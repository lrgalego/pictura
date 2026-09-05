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
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	"github.com/lrgalego/story-time/internal/jobs"
	"github.com/lrgalego/story-time/internal/pipeline"
	"github.com/lrgalego/story-time/internal/store"
	"github.com/lrgalego/story-time/web"
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

	dir, _ := os.MkdirTemp("", "story-time-e2e")
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
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
	t.Cleanup(func() { ctx.Close() })
	page, err := ctx.NewPage()
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

// signup registers a fresh user through the form and lands on the new-story page.
func signup(t *testing.T, page playwright.Page, name string) {
	t.Helper()
	goto_(t, page, baseURL+"/signup")
	must(t, page.Fill("#username", name))
	must(t, page.Fill("#password", "correct-horse"))
	must(t, page.Click("button[type=submit]"))
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
	must(t, expect.Locator(page.Locator(".char")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
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
	must(t, expect.Page(page).ToHaveURL(regexp.MustCompile(`/stories/new$`)))

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

	// Casting phase: no art, upload a reference to the first character.
	must(t, expect.Locator(page.Locator("button:has-text('Draw the character sheets')").First()).ToBeVisible())
	must(t, expect.Locator(page.Locator(".char__sheet img")).ToHaveCount(0))
	must(t, page.Locator(".ref-upload input[name=references]").First().SetInputFiles([]string{refPNG}))
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("attached to"))
	must(t, expect.Locator(page.Locator(".char").First().Locator(".ref__img")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))

	// Adjust one character from the dialog with feedback.
	must(t, page.Locator("button:has-text('Adjust'):not(:has-text('cast'))").First().Click())
	must(t, expect.Locator(page.Locator(".dialog")).ToContainText("Adjust Mara"))
	must(t, page.Fill("#feedback", "shorter hair"))
	must(t, page.Click(".dialog button:has-text('Revise and redraw')"))
	must(t, expect.Locator(page.Locator(".dialog")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator(".char").First()).ToContainText("revised: shorter hair"))

	// Draw the sheets, then storyboard.
	must(t, page.Locator("button:has-text('Draw the character sheets')").First().Click())
	must(t, expect.Locator(page.Locator(".job__title")).ToContainText("Drawing character sheets"))
	must(t, expect.Locator(page.Locator(".char__sheet img")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator("button:has-text('Storyboard the pages')").First()).ToBeVisible())

	// The sheet lightbox opens and closes.
	must(t, page.Locator(".char__sheet-btn").First().Click())
	must(t, expect.Locator(page.Locator(".lightbox__image")).ToBeVisible())
	must(t, expect.Locator(page.Locator(".lightbox__caption")).ToContainText("character sheet"))
	must(t, page.Keyboard().Press("Escape"))
	must(t, expect.Locator(page.Locator(".lightbox")).ToHaveCount(0))

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
	must(t, expect.Locator(page.Locator(".char__sheet img")).ToHaveCount(3))

	createStory(t, page, "Book Two", "Manga")
	must(t, expect.Locator(page.Locator(".suggest")).ToHaveCount(3))
	must(t, expect.Locator(page.Locator(".suggest").First()).ToContainText("Same Mara as in Book One?"))
	must(t, page.Locator(".suggest button:has-text('Yes, use this one')").First().Click())
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("is now the same character"))
	must(t, expect.Locator(page.Locator(".char").First().Locator(".char__sheet img")).ToHaveCount(1))
	must(t, expect.Locator(page.Locator(".char").First()).ToContainText("Also in"))
	must(t, expect.Locator(page.Locator(".suggest")).ToHaveCount(2))

	// The rest of the cast can be picked from the registry dialog.
	must(t, page.Locator("button:has-text('From another story')").First().Click())
	must(t, expect.Locator(page.Locator(".dialog")).ToContainText("someone you already have?"))
	must(t, expect.Locator(page.Locator(".link-row")).ToHaveCount(3))
	must(t, page.Locator(".link-row:has-text('Pip') button:has-text('Use')").Click())
	must(t, expect.Locator(page.Locator(".dialog")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator(".char__sheet img")).ToHaveCount(2))
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
	must(t, page.Locator(".ref-upload input[name=sheet]").Nth(1).SetInputFiles([]string{refPNG}))
	must(t, expect.Locator(page.Locator(".toast")).ToContainText("Sheet set for Pip"))
	must(t, expect.Locator(page.Locator("#step-panel[hx-trigger]")).ToHaveCount(0))
	must(t, expect.Locator(page.Locator(".char__sheet img")).ToHaveCount(1))
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
