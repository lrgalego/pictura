# Production secrets as 1Password references rather than values.
#
# This file is COMMITTED, and safe to commit: an op:// reference is an
# address, not a credential. `shipyard deploy` re-execs itself under
# `op run --env-file=` with this file, resolves each reference into the
# deploy process's environment, and streams the values to the VM — nothing
# ever touches disk on this machine.
#
# One variable per line. The first variable doubles as the sentinel shipyard
# uses to detect that resolution happened. Example:
#
META_API_KEY=op://pictura/meta-ai-api/credential
#
# The registry read token (docker login for ghcr.io/lrgalego/pictura) resolves the
# same way — create the item, then keep this line:
SHIPYARD_REGISTRY_TOKEN=op://pictura/github-registry/token
