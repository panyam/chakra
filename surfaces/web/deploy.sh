#!/bin/sh
# deploy.sh deploys agentweb to App Engine. `make deploy-web` runs it after the
# tests and the bundle build.
#
# The module's go.mod replaces chakra and mcpkit with relative paths that don't
# exist once it's uploaded on its own, so it vendors them first. The access
# token lives in .agentweb-token (gitignored, created on the first deploy) and
# goes into a temporary copy of app.yaml, so it never reaches git. Both the
# vendor tree and the copy are removed whether or not the deploy worked.
set -eu
cd "$(dirname "$0")"
project="${1:?usage: deploy.sh <gcp-project>}"

if [ ! -s .agentweb-token ]; then
	openssl rand -hex 24 > .agentweb-token
	echo "==> new access token in surfaces/web/.agentweb-token: paste it into the Sessions panel"
fi
token=$(cat .agentweb-token)

cleanup() { rm -rf vendor app.deploy.yaml; }
trap cleanup EXIT

go mod vendor
awk -v tok="$token" '{ print } /^env_variables:/ { printf "  AGENTWEB_ACCESS_TOKEN: \"%s\"\n", tok }' app.yaml > app.deploy.yaml
gcloud app deploy app.deploy.yaml --project "$project" --quiet
