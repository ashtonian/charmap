#!/usr/bin/env bash
# Exercises the built command end to end: exit codes, diagnostics, and that a
# failed run writes nothing.
#
# Usage: scripts/smoke.sh [PATH_TO_CHARMAP]   (builds one when omitted)
set -euo pipefail

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

bin=${1:-}
if [[ -z $bin ]]; then
  bin=$work/charmap
  go build -o "$bin" .
fi

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# expect CODE CMD... runs CMD, capturing stdout and stderr, and checks its exit code.
expect() {
  local want=$1
  shift
  local got=0
  "$@" >"$work/stdout" 2>"$work/stderr" || got=$?
  [[ $got == "$want" ]] || fail "$* exited $got, want $want; stderr: $(cat "$work/stderr")"
}

tree=$work/tree
mkdir -p "$tree/sub"
printf 'group: <::GROUP::>\n' >"$tree/a.yaml"
printf 'cluster: <::CLUSTER_NAME::>\nurl: <::URL::>\n' >"$tree/sub/b.yaml"

# check reports what would change and writes nothing.
expect 0 "$bin" check --dir "$tree" --set GROUP=g --set CLUSTER_NAME=c --set 'URL=https://x/?a=1,b=2'
grep -q '<::GROUP::>' "$tree/a.yaml" || fail "check modified a file"

# A missing key fails the whole run and names the file, line and column.
expect 1 "$bin" render --dir "$tree" --set GROUP=g --set CLUSTER_NAME=c
grep -q "sub/b.yaml:2:6: no value for URL" "$work/stderr" || fail "missing-key diagnostic: $(cat "$work/stderr")"
grep -q '<::GROUP::>' "$tree/a.yaml" || fail "failed run modified a.yaml"

# All three value sources, with precedence --env-prefix < --env < --set.
expect 0 env PFX_GROUP=from-prefix CLUSTER_NAME=from-env PFX_URL=u \
  "$bin" render --dir "$tree" --env-prefix PFX_ --env CLUSTER_NAME --set GROUP=from-set
[[ $(cat "$tree/a.yaml") == "group: from-set" ]] || fail "a.yaml: $(cat "$tree/a.yaml")"
[[ $(cat "$tree/sub/b.yaml") == $'cluster: from-env\nurl: u' ]] || fail "b.yaml: $(cat "$tree/sub/b.yaml")"

# The environment is never read implicitly.
printf 'x: <::HOME::>\n' >"$tree/c.yaml"
expect 1 "$bin" render --dir "$tree"
grep -q "no value for HOME" "$work/stderr" || fail "HOME was read without being named"

# filter renders stdin to stdout and prints nothing to stdout on failure.
expect 0 "$bin" filter --set A=1 < <(printf 'a: <::A::>')
[[ $(cat "$work/stdout") == "a: 1" ]] || fail "filter output: $(cat "$work/stdout")"
expect 1 "$bin" filter < <(printf 'a: <::A::>')
[[ ! -s $work/stdout ]] || fail "filter wrote output despite a problem"

# Usage errors exit 2 and never echo a value.
expect 2 "$bin" render --dir "$tree" stray-positional
expect 2 "$bin" render --dir "$tree" --set hunter2
grep -q hunter2 "$work/stderr" && fail "a malformed --set was echoed"
expect 2 "$bin" render --no-such-flag
expect 2 "$bin" render --workers 0
expect 2 "$bin" render --include '('
expect 2 "$bin" frobnicate
expect 2 "$bin"

expect 0 "$bin" help
expect 0 "$bin" render -h
expect 0 "$bin" version
grep -q '^charmap ' "$work/stdout" || fail "version output: $(cat "$work/stdout")"

echo "smoke: ok"
