#!/usr/bin/env bash
# End-to-end harness for x-opencode-session forwarding.
#
# Builds the auth plugin, the pinned upstream proxy, and a stub opencode-go
# upstream; wires them together locally; and replays the four cases from the
# session-header investigation. No live credential and no deployed proxy are
# involved.
#
# The stub records the User-Agent and session header of every request it
# receives, which is what lets a failure name its own cause: a non-plugin
# User-Agent means the built-in compat executor handled the call and our
# executor never ran; our User-Agent with a missing session header means the
# host handed the plugin empty request headers.
set -euo pipefail
cd "$(dirname "$0")"

HERE=$PWD
REPO=$(cd .. && pwd)
UPSTREAM=${UPSTREAM:-$REPO/upstream/CLIProxyAPI}
WORK=${WORK:-/tmp/opencode/oc-go-session-e2e}

PROXY_HOST=127.0.0.1
PROXY_PORT=8398
STUB_HOST=127.0.0.1
STUB_PORT=9998
CLIENT_KEY=e2e-test-key
MODEL=${MODEL:-mimo-v2.5}
# The prefixed alias clients actually use. It comes from the prefix stamped on
# the auth by the plugin, not from an openai-compatibility config entry -- that
# entry cannot exist here without shadowing the plugin's executor.
PREFIXED_MODEL=${PREFIXED_MODEL:-opencode-go/$MODEL}
REQUEST_LOG=$WORK/upstream-requests.log
PROXY_LOG=$WORK/proxy.log
STUB_LOG=$WORK/stub.log

nixgo() { nix develop "$REPO" --command "$@"; }

if [[ ! -f $UPSTREAM/cmd/server/main.go ]]; then
  echo "upstream checkout not found at $UPSTREAM (git submodule update --init?)" >&2
  exit 1
fi

# Refuse to run against ports a previous run leaked, rather than silently
# testing a stale proxy.
for port in $PROXY_PORT $STUB_PORT; do
  if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$port/" 2>/dev/null; then
    echo "port $port is already in use -- a previous harness run may still be alive" >&2
    echo "  try: pkill -f cli-proxy-api; pkill -f oc-go-session-e2e/bin/stub" >&2
    exit 1
  fi
done

rm -rf "$WORK"
mkdir -p "$WORK/plugins/linux/amd64" "$WORK/bin"
cp config.yaml "$WORK/config.yaml"
cp -r auths "$WORK/auths"

echo "== build auth plugin =="
(cd "$REPO" && CGO_ENABLED=1 nixgo go build -buildmode=c-shared -trimpath \
  -o "$WORK/plugins/linux/amd64/cpa-opencode-go-auth.so" ./authplugin)
rm -f "$WORK/plugins/linux/amd64/cpa-opencode-go-auth.h"

echo "== build stub upstream =="
(cd stub && nixgo go build -o "$WORK/bin/stub" .)

echo "== build pinned proxy =="
(cd "$UPSTREAM" && nixgo go build -o "$WORK/bin/cli-proxy-api" ./cmd/server)

echo "== start stub + proxy =="
"$WORK/bin/stub" -addr "$STUB_HOST:$STUB_PORT" -log "$REQUEST_LOG" >"$STUB_LOG" 2>&1 &
STUB_PID=$!
trap 'kill $STUB_PID ${PROXY_PID:-} 2>/dev/null || true' EXIT

# The stub must answer before the proxy boots. Model registration runs once at
# startup and then only every 15 minutes, so a stub that is still binding its
# port when the proxy starts leaves the provider with no models for the whole
# run -- which looks identical to a routing bug.
stub_ready=""
for _ in $(seq 1 40); do
  if curl -sf -o /dev/null "http://$STUB_HOST:$STUB_PORT/v1/models"; then
    stub_ready=yes
    break
  fi
  sleep 0.25
done
if [[ -z $stub_ready ]]; then
  echo "stub did not become ready; see $STUB_LOG" >&2
  exit 1
fi

# `exec` matters: without it $! is the subshell's PID, the trap kills only the
# subshell, and the orphaned proxy keeps holding the port. A later run then
# silently talks to the stale proxy from an earlier build.
(cd "$WORK" && exec ./bin/cli-proxy-api --config "$WORK/config.yaml" --local-model >"$PROXY_LOG" 2>&1) &
PROXY_PID=$!

ready=""
for _ in $(seq 1 40); do
  if curl -sf -o /dev/null "http://$PROXY_HOST:$PROXY_PORT/v1/models" -H "Authorization: Bearer $CLIENT_KEY"; then
    ready=yes
    break
  fi
  sleep 0.5
done
if [[ -z $ready ]]; then
  echo "proxy did not become ready; see $PROXY_LOG" >&2
  tail -40 "$PROXY_LOG" >&2
  exit 1
fi

# call <session-header|""> <stream true|false> [model] -> prints HTTP status
call() {
  local session=$1 stream=$2 model=${3:-$MODEL}
  local args=(-sS -o "$WORK/last-body.txt" -w '%{http_code}'
    -X POST "http://$PROXY_HOST:$PROXY_PORT/v1/chat/completions"
    -H "Authorization: Bearer $CLIENT_KEY"
    -H "Content-Type: application/json")
  if [[ -n $session ]]; then
    args+=(-H "x-opencode-session: $session")
  fi
  args+=(-d "{\"model\":\"$model\",\"max_tokens\":16,\"stream\":$stream,\"messages\":[{\"role\":\"user\",\"content\":\"say pong\"}]}")
  curl "${args[@]}"
}

echo "== run cases =="
: >"$REQUEST_LOG.cases"
status_a=$(call ses_e2e_a false); echo "A oc-go + header       http=$status_a"
status_b=$(call "" false);        echo "B oc-go, no header     http=$status_b"
status_c=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "http://$STUB_HOST:$STUB_PORT/v1/chat/completions" \
  -H 'Content-Type: application/json' -H 'x-opencode-session: ses_e2e_c' \
  -d '{"model":"'"$MODEL"'","messages":[{"role":"user","content":"say pong"}]}')
echo "C stub direct+header   http=$status_c"
status_d=$(call ses_e2e_d true);  echo "D oc-go stream+header  http=$status_d"
status_e=$(call ses_e2e_e false "$PREFIXED_MODEL")
echo "E prefixed model       http=$status_e"

echo
echo "== what the stub actually received =="
grep chat/completions "$REQUEST_LOG" || echo "(no chat/completions requests reached the stub)"

echo
echo "== verdict =="
fail=0
expect() {
  local label=$1 got=$2 want=$3
  if [[ $got == "$want" ]]; then
    echo "  ok   $label = $want"
  else
    echo "  FAIL $label = $got (want $want)"
    fail=1
  fi
}
expect "A status" "$status_a" 200
expect "B status" "$status_b" 400
expect "C status" "$status_c" 200
expect "D status" "$status_d" 200
expect "E status" "$status_e" 200

proxied=$(grep -c 'chat/completions' "$REQUEST_LOG" || true)
ours=$(grep -c 'ua=cpa-opencode-go-auth/' "$REQUEST_LOG" || true)
forwarded=$(grep 'chat/completions' "$REQUEST_LOG" | grep -c 'session=ses_e2e_' || true)

if [[ $proxied -le 1 ]]; then
  echo "  DIAGNOSIS: proxied requests never reached the stub at all -- routing or"
  echo "             model registration failed before the executor. See $PROXY_LOG."
  fail=1
elif [[ $ours -eq 0 ]]; then
  echo "  DIAGNOSIS: candidate 1 -- the built-in compat executor handled these calls."
  echo "             The plugin executor never ran (no cpa-opencode-go-auth User-Agent)."
  fail=1
elif [[ $forwarded -eq 0 ]]; then
  echo "  DIAGNOSIS: candidate 2 -- our executor ran but forwarded no session header."
  echo "             The host handed the plugin empty request headers."
  fail=1
fi

if [[ $fail -eq 0 ]]; then
  echo "  session header forwarding is healthy"
else
  echo
  echo "  logs: $PROXY_LOG  $STUB_LOG  $REQUEST_LOG"
fi
exit $fail
