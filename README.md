# Story Time

Paste a script, get a comic. Story Time reads a story with **Muse Spark**
(`muse-spark-1.3-contributor`), designs the cast, storyboards the pages and
draws every page with **Muse Image** (`muse-image-1.0`), with the writer
approving and adjusting each step.

The workflow, one screen per step:

1. **Script** — paste the story, pick a look (comic, manga, storybook, cartoon, noir, pixel art).
2. **Characters** — every character gets an illustrator-ready description first. Attach reference images (photos, sketches, outfits, props) to any character, upload a finished model sheet to use as-is, or reuse a character from another story; adjust the words, then draw the remaining sheets from the references. Afterwards adjust, redraw or edit any character.
3. **Pages** — the script becomes pages of panels with shots, dialogue and captions. Adjust one page or the whole plan.
4. **Comic** — pages are drawn with the character sheets as references so the cast stays on-model. Redraw any page with notes; download as PDF or a zip of PNGs.

Accounts keep a library of stories to revisit and iterate on, and a **cast
registry** across stories: when a new script names a character you already
have, the casting step suggests reusing them (look, references and finished
sheet are copied, no redraw needed), and any character from another story can
be picked by hand. "Your cast" lists every distinct character and the stories
they appear in.

## Run it

```sh
make dev            # generate templ code, serve on 0.0.0.0:8080 (Makefile.local)
./bin/server --host 0.0.0.0 --port 8787 --data ./data   # or any port
```

Flags: `--host`, `--port`, `--data` (SQLite database + generated images,
default `./data`), `--env-file` (default `.env`), `--fake-ai` (offline
placeholder provider, no API calls), `--health-check`.

The Meta Model API key is read from `META_API_KEY`. Locally, put it in the
gitignored `.env`:

```sh
op read "op://StoryTime/meta-ai-api/credential" | sed 's/^/META_API_KEY=/' > .env
```

or run under 1Password: `op run --env-file=.env.dev.tpl -- make dev`. Without
a key the server starts in placeholder-art mode so the whole workflow can be
exercised for free.

Optional: `META_TEXT_MODEL` / `META_IMAGE_MODEL` override the model ids.

## Layout

| Path | What |
|---|---|
| `cmd/server` | the binary: flags, `.env` loading, provider selection |
| `internal/store` | SQLite (modernc, pure Go): users, sessions, stories, characters, pages, jobs, images |
| `internal/meta` | Meta Model API client: chat completions with JSON schema, image generations/edits |
| `internal/pipeline` | prompts, schemas and the `AI` interface; `fake.go` is the offline provider |
| `internal/jobs` | background runner: one job per story, concurrent image drawing, progress for the UI |
| `internal/pdf` | minimal PDF writer (one JPEG page per comic page) |
| `web` | routes, auth, handlers; `web/views` templ pages; `web/static/app.css` the brand theme |

The UI is [htmx-ds](https://github.com/lrgalego/htmx-ds) with a custom theme
(`storytime` light/dark bound to the default `light`/`dark` slugs so the
toggle works with no picker). Long model calls run in the background; step
panels poll every two seconds while a job runs and stop when it is done.

## Test

```sh
make test
```

`web/web_test.go` drives the full workflow (signup, script, cast, feedback,
pages, drawing, downloads, isolation between users, delete) on the fake
provider.

## Deploy

Built and operated with [shipyard](SHIPYARD.md). The machine, registry and
hostname in `shipyard.toml` are placeholders until deploy is configured.
