# Operating story-time

This project is built, shipped and operated by the **shipyard** CLI —
`shipyard.toml` is the single source of truth, and the Makefile is an
implementation detail for the git hooks, not an interface.

| Task | Command |
|---|---|
| run locally | `shipyard dev` |
| tests | `shipyard test` |
| regenerate templ code | `shipyard generate` |
| verify the machine | `shipyard doctor` |
| set up a new machine | `shipyard setup` (then install the 1Password CLI natively) |
| deploy | `git push origin main` then `shipyard deploy` |
| roll back | `shipyard rollback <tag>` — seconds, no rebuild, no vault |
| what is live | `shipyard status` |
| server logs | `shipyard logs [-f] [-n N]` |
| job logs | `shipyard logs <cron-name>` |
| production DB copy | `shipyard db snapshot` |
| reconcile scaffold | `shipyard sync` → review → `shipyard sync --apply` |
| full manual | `shipyard docs` → http://127.0.0.1:8383 |

Three rules: deploys ship only commits already on `origin/main` (the push
gate runs tests + coverage); the machine is never edited by hand (every
deploy re-converges it from this repo, and
`box-1` is shared — other apps live beside this one, mutually
ignorant); any command shows its exact script and env with `--print`
before you commit to running it.
