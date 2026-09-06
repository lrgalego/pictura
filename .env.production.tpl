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

# Image storage. With these four set the app keeps every generated image in
# a Cloudflare R2 bucket and serves it through short-lived signed URLs, so
# the VM's disk and the tunnel never carry image bytes. Leave them unset to
# keep images on the machine under /data/images.
R2_ACCOUNT_ID=op://pictura/cloudflare-r2/account id
R2_ACCESS_KEY_ID=op://pictura/cloudflare-r2/access key id
R2_SECRET_ACCESS_KEY=op://pictura/cloudflare-r2/secret access key
R2_BUCKET=pictura-media
