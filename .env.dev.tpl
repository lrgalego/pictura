# Local development secrets, as 1Password references rather than values.
#
# The counterpart to .env.production.tpl, pointing at a *different* vault item
# holding a *different* key: a key used on a laptop travels through shells,
# scrollback and screen shares, and this split means the one that does cannot
# be used against the production account. `shipyard doctor` warns when the two
# items hold the same value.
#
# Use it for anything that needs the key locally:
#
#   op run --env-file=.env.dev.tpl -- go run ./cmd/server
#   op run --env-file=.env.dev.tpl -- $SHELL      # a whole session
#
META_API_KEY=op://pictura/meta-ai-api/credential
