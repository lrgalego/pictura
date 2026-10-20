# Operating pictura

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
| deploy | `git push origin main` then `shipyard deploy` (prompt-free once `.op-token` exists) |
| roll back | `shipyard rollback <tag>` — seconds, no rebuild, no vault |
| what is live | `shipyard status` |
| server logs | `shipyard logs [-f] [-n N]` |
| job logs | `shipyard logs <cron-name>` |
| production DB copy | `shipyard db snapshot` |
| reconcile scaffold | `shipyard sync` → review → `shipyard sync --apply` |
| full manual | `shipyard docs` → http://127.0.0.1:8383 |

Three rules:

1. **A release goes branch → local `main` → `origin/main` → production.**
   Merge on local main, push main from that checkout, then deploy:

   ```bash
   git switch main && git merge --ff-only my-change
   git push origin main   # the gate runs here
   shipyard deploy
   ```

   The push gate, `make gate`, runs generated-code, build, vet, tests and
   coverage, then any `gate-local::` checks this project adds in
   `Makefile.local`. A branch pushed straight to `origin/main` is refused, and
   so is a deploy whose commit is not on local main, or whose local main is not
   level with `origin/main`.
2. **The machine is never edited by hand.** Every deploy re-converges it from
   this repo, and `box-1` is
   shared — other apps live beside this one, mutually ignorant.
3. **Look before you run.** Any command shows its exact script and env with
   `--print` before you commit to running it.
