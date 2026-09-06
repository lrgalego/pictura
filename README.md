# Pictura

Paste a script, get a comic. Pictura reads a story with **Muse Spark**
(`muse-spark-1.3-contributor`), designs the cast, storyboards the pages and
draws every page with **Muse Image** (`muse-image-1.0`), with the writer
approving and adjusting each step.

The workflow, one screen per step:

1. **Script** — paste the story, pick a look (comic, manga, storybook, cartoon, noir, pixel art).
2. **Characters** — every character gets an illustrator-ready description first. Attach reference images (photos, sketches, outfits, props) to any character, upload a finished model sheet to use as-is, or reuse a character from another story; adjust the words, then draw the remaining sheets from the references. Afterwards adjust, redraw or edit any character.
3. **Pages** — the script becomes pages of panels with shots, dialogue and captions. Adjust one page or the whole plan.
4. **Comic** — pages are drawn with the character sheets as references so the cast stays on-model. Redraw any page with notes; download as PDF or a zip of PNGs.

Signing up creates a disabled account parked on a "waiting to be enabled"
page; an operator enables it (see below). Accounts keep a library of stories
to revisit and iterate on, and a **cast
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

The Meta Model API key is read from `META_API_KEY`, which `make dev`
resolves from 1Password for the life of the process through the committed
`.env.dev.tpl` (an `op://` reference, never a value). Running the binary by
hand works the same way:

```sh
op run --env-file=.env.dev.tpl -- ./bin/server --host 0.0.0.0 --port 8787 --data ./data
```

In production `shipyard deploy` resolves `.env.production.tpl` the same way
and streams the value to the machine. Without a key the server starts in
placeholder-art mode so the whole workflow can be exercised for free.

Optional: `META_TEXT_MODEL` / `META_IMAGE_MODEL` override the model ids.

## Images

Every generated image and upload goes through one blob store
(`internal/blob`): files under `<data>/images` by default, or a Cloudflare
R2 bucket when `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`
and `R2_BUCKET` are set. On R2 the browser fetches images through
short-lived signed URLs, so neither the VM nor the tunnel carries the bytes.
The database only ever holds names.

A garbage collector reclaims superseded art: after every job, and once at
startup, images no character sheet, page or reference points at any more are
deleted. Redrawing a sheet or a page therefore frees the previous one.

Moving an existing installation to R2, once the bucket and token exist:

```sh
./bin/server --data ./data --migrate-blobs   # with the R2_* variables set; re-runnable
```

## Enabling accounts

There is no admin screen yet. The server binary doubles as the switch; run it
against the data directory:

```sh
./bin/server --data ./data --list-users
./bin/server --data ./data --enable-user lucas
./bin/server --data ./data --disable-user someone
```

In production the same image runs as a one-off container on the box:

```sh
shipyard ssh 'cd /opt/pictura && sudo docker compose run --rm --no-deps server --data /data --enable-user lucas'
```

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
(`pictura` light/dark bound to the default `light`/`dark` slugs so the
toggle works with no picker). Long model calls run in the background; step
panels poll every two seconds while a job runs and stop when it is done.

## Test

```sh
make test          # unit + handler tests, no network, no browser
make cover-check   # the same suite through shipyard's coverage gate (floor: 85%)
make test-e2e      # main flows in a real Chromium via playwright-go
```

Nothing in the suite calls the Meta API. `internal/meta/metatest` is a
stand-in server built from responses recorded against the real endpoints
(`testdata/*.json`, with the multi-megabyte image payloads replaced by a
2x2 PNG); the client tests assert on the exact requests it receives. Every
other layer runs on the offline `pipeline.Fake` provider. The web tests wrap
it in a gate that can hold model calls open, so "still working" states are
tested deterministically instead of racing a job that finishes instantly.

`e2e/` (build tag `e2e`) drives signup and login, the whole script → cast →
sheets → storyboard → comic flow with dialogs, uploads, lightboxes and the
PDF download, reusing characters across stories, uploading a finished sheet,
and a mobile overflow check. It self-bootstraps the Chromium driver on first
run.

## Deploy

Built and operated with [shipyard](SHIPYARD.md). The machine, registry and
hostname in `shipyard.toml` are placeholders until deploy is configured.
