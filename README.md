# charmap

[![Release](https://github.com/ashtonian/charmap/actions/workflows/release.yaml/badge.svg?branch=main)](https://github.com/ashtonian/charmap/actions/workflows/release.yaml)
[![GitHub release](https://img.shields.io/github/v/release/ashtonian/charmap)](https://github.com/ashtonian/charmap/releases/latest)
[![Docker pulls](https://img.shields.io/docker/pulls/ashtonian/charmap)](https://hub.docker.com/r/ashtonian/charmap)
[![Go Reference](https://pkg.go.dev/badge/github.com/ashtonian/charmap/v2.svg)](https://pkg.go.dev/github.com/ashtonian/charmap/v2)

charmap replaces `<::KEY::>` tokens in files with values you name explicitly
on the command line. It is built to run as a preprocessor in a GitOps
rendering path, for example in an Argo CD Config Management Plugin before
Kustomize or Helm, where a half-applied substitution or a silently empty value
would reach a cluster.

```sh
charmap render --dir manifests --set GROUP=us-east --env CLUSTER_NAME
```

What it guarantees:

- **All or nothing.** Every selected file is rendered before any is written.
  One unknown key, malformed token or unreadable file leaves the whole tree
  untouched and exits 1.
- **Explicit values only.** Nothing is read from the environment unless a
  flag names it.
- **Exact diagnostics.** Every problem is reported as `path:line:column`, and
  values never appear in output, logs or errors.
- **Safe writes.** Changed files are replaced by an atomic rename, keeping
  their permission bits and owner. Unchanged files are never touched.
  Symlinks and special files are never read or written, and nothing outside
  `--dir` is reachable.

## Contents

- [Install](#install)
- [Commands](#commands)
- [Examples](#examples)
- [Token syntax](#token-syntax)
- [Values](#values)
- [Configuration](#configuration)
- [File selection and writes](#file-selection-and-writes)
- [Exit status, diagnostics and logs](#exit-status-diagnostics-and-logs)
- [Argo CD Config Management Plugin](#argo-cd-config-management-plugin)
- [Security](#security)
- [When to use something else](#when-to-use-something-else)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Code map](#code-map)

## Install

**Container image** (linux/amd64 and linux/arm64), published to Docker Hub as
`docker.io/ashtonian/charmap` with tags for the full version (`2.0.0`), minor
(`2.0`), major (`2`) and `latest`. The image is distroless and runs as
nonroot; it has no shell. Pin it by digest in production.

```sh
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
  docker.io/ashtonian/charmap:2 render --set GROUP=us-east
```

`--user` runs charmap as the owner of the files: it writes each changed file
as a new file in the same directory, which needs write access to that
directory, and it keeps the file's owner.

To use charmap inside another image, copy the binary:

```dockerfile
COPY --from=docker.io/ashtonian/charmap:2 /usr/local/bin/charmap /usr/local/bin/charmap
```

**From source** (Go 1.26 or later):

```sh
go install github.com/ashtonian/charmap/v2@latest
```

`charmap version` prints the version, Go version and the platform the binary
was built for.

Upgrading from 1.x: v2 changed the command line and stopped reading the
environment implicitly. The [v2.0.0 release
notes](https://github.com/ashtonian/charmap/releases/tag/v2.0.0) list every
change.

## Commands

| Command | What it does |
|---|---|
| `charmap render [flags]` | Renders every selected file under `--dir` and, only if all succeed, replaces each changed file. |
| `charmap check [flags]` | Renders every selected file and reports every problem. Writes nothing. Use it in CI. |
| `charmap filter [flags]` | Renders stdin to stdout. Writes nothing to stdout unless the whole input renders. |
| `charmap version` | Prints version, Go version and platform. |
| `charmap help` | Prints usage. `charmap <command> -h` lists a command's flags. |

Every option is a flag; positional arguments are rejected. Go's flag parser
stops at the first positional argument, so accepting them would silently
ignore every flag after one.

## Examples

Fail a CI job when a template references a value it does not have, without
changing any file:

```sh
charmap check --dir manifests --set GROUP=us-east --set CLUSTER_NAME=ci
```

Substitute into rendered output instead of the source files:

```sh
kustomize build overlays/prod | charmap filter --env GROUP --env CLUSTER_NAME | kubectl apply -f -
```

Use every key of a Kubernetes Secret as a value. Load it into the container
with a prefix, then select that prefix:

```yaml
envFrom:
  - prefix: FLEET_
    secretRef:
      name: cluster-identity   # key REGION becomes FLEET_REGION, token <::REGION::>
```

```sh
charmap render --env-prefix FLEET_
```

Process JSON as well as YAML, skip a vendored directory, and use other
delimiters:

```sh
charmap render --include '\.ya?ml$' --include '\.json$' --exclude '^vendor/' \
  --open '%%' --close '%%' --env GROUP
```

## Token syntax

A token is the open delimiter, a key, and the close delimiter, with nothing in
between:

```yaml
metadata:
  labels:
    group: "<::GROUP::>"
```

- Keys match `[A-Za-z_][A-Za-z0-9_]*` and are at most 128 bytes. `<::my-key::>`
  and `<:: GROUP ::>` are not tokens.
- An open delimiter that does not start a well-formed token is an error
  (`malformed token`), not literal text. With a dedicated delimiter such as
  `<::` it is almost always a typo.
- A well-formed token whose key has no value is an error (`no value for KEY`).
- Values are inserted byte for byte and never scanned again, so a value may
  contain delimiters. charmap does no YAML, JSON or shell escaping: a value
  containing a newline or a quote can change the structure of the file it
  lands in. Keep values to simple strings, or use a structured tool (Helm
  values, Kustomize replacements) for structured data.
- Input is treated as bytes. CRLF line endings, byte-order marks and invalid
  UTF-8 pass through unchanged.
- There is no escape sequence. A file that must contain the delimiter
  literally should be excluded, or use different delimiters.

Delimiters are set with `--open` and `--close`. They must be non-empty, must
not contain whitespace, the open delimiter must not end with a letter, digit
or underscore, and the close delimiter must not start with one; otherwise a
key's boundaries would be ambiguous. Delimiters that occur in other template
languages, such as `{{`/`}}` (Helm, Go templates) or `${{`/`}}` (GitHub
Actions), make every one of their expressions a malformed token. Keep the
default unless files never contain them.

## Values

Values come only from these flags. When a key appears in more than one source,
the later one in this list wins:

| Source | Flag | Key | Notes |
|---|---|---|---|
| Environment variables with a prefix | `--env-prefix PREFIX` | name without the prefix | Every variable starting with `PREFIX` is read. A remainder that is not a valid key is an error. |
| Named environment variables | `--env NAME` (repeatable) | `NAME` | An unset variable is an error; an empty one is a valid empty value. |
| Assignments | `--set KEY=VALUE` (repeatable) | `KEY` | Splits at the first `=`, so values may contain `=` and `,`. |

Giving the same key twice to `--set`, or the same name twice to `--env`, is
an error. An empty value is legal; if a key must never be empty, check it
before calling charmap (for example `: "${GROUP:?}"` in a shell script).

Prefer `--env` or `--env-prefix` over `--set` for anything sensitive:
command-line arguments are visible to other processes on the same host.

## Configuration

All configuration is by flag. charmap reads no configuration files.

| Flag | Type | Default | Commands | Description |
|---|---|---|---|---|
| `--dir` | path | `.` | render, check | Directory to process. If the path itself is a symlink it is followed once; nothing inside it is. |
| `--include` | regexp, repeatable | `\.ya?ml$` | render, check | Select files whose root-relative, slash-separated path matches. Giving any `--include` replaces the default. |
| `--exclude` | regexp, repeatable | none | render, check | Skip files, and prune directories, whose root-relative path matches. A directory is tested as both `dir` and `dir/`, so `^vendor/` prunes `vendor`. |
| `--workers` | int, 1–1024 | `GOMAXPROCS` | render, check | Files processed concurrently. Go limits `GOMAXPROCS` to the container's CPU limit. |
| `--set` | `KEY=VALUE`, repeatable | none | all | Assign a value. |
| `--env` | name, repeatable | none | all | Read an environment variable as the key of the same name. |
| `--env-prefix` | string | none | all | Read every environment variable with this prefix. |
| `--open` | string | `<::` | all | Opening delimiter. |
| `--close` | string | `::>` | all | Closing delimiter. |
| `--max-file-size` | bytes | `67108864` (64 MiB) | all | Largest file (or stdin) read and largest rendered output per file. Larger is an error, not a skip. |
| `--log-level` | `debug` \| `info` \| `warn` \| `error` | `warn` | all | Log level for stderr. |
| `--log-format` | `text` \| `json` | `text` | all | Log format. |

## File selection and writes

**Selection.** charmap walks `--dir` in lexical order. It always skips
directories named `.git`, at any depth. It prunes excluded directories before
reading them, so an excluded directory may be unreadable. It skips symlinks
(to files or directories) and other non-regular files such as FIFOs and
sockets. It selects a regular file if no `--exclude` and some `--include`
matches its path relative to `--dir`, using `/` as the separator.

**Rendering.** Each selected file is read whole, up to `--max-file-size`, and
rendered. A file without tokens is neither copied nor written.

**Writing (render only).** A changed file's output goes to a temporary file
in the same directory (`.<name>.charmap-<random>.tmp`). The temporary file
gets the original's permission bits (not setuid, setgid or sticky) and its
owner and group; if the owner cannot be kept, the run fails rather than
changing it. The file is synced, then renamed over the original. Before each
rename, charmap checks the original has not changed since it was read; if it
has, it is not overwritten and the run fails.

Renames happen only after every file rendered without a problem. Each rename
is atomic, but the set of renames is not one transaction: if a rename fails,
the files renamed before it stay replaced, and the error says how many. The
same applies if the process is killed during renaming. Because files are
replaced rather than rewritten in place, a hard-linked file's other names keep
the old content.

**Interruption.** On SIGINT or SIGTERM before renaming starts, charmap stops,
removes its temporary files and exits 1 with nothing written. Once renaming
has started it runs to the end, since stopping part way would leave the tree
half written. A second signal terminates immediately.

Do not run two charmap processes on the same tree at once.

## Exit status, diagnostics and logs

| Exit | Meaning |
|---|---|
| 0 | Success. `render` wrote every changed file; `check` found no problem; `filter` wrote its output. |
| 1 | A token problem, an I/O error, or interruption. `render` wrote nothing (unless a rename failed part way, see above). |
| 2 | Usage or configuration error: unknown command or flag, positional argument, invalid key, missing `--env` variable, invalid regexp or delimiter. |

Problems go to stderr, one per line, in path then position order:

```text
manifests/app/deploy.yaml:12:15: no value for CLUSTER_NAME
manifests/app/deploy.yaml:30:9: malformed token: "<::" must be followed by a key ([A-Za-z_][A-Za-z0-9_]*, at most 128 bytes) and "::>"
```

Columns count bytes. A file with more than 100 problems has the rest
suppressed, with a note.

Logs also go to stderr; stdout carries only `filter` output. At the default
level (`warn`) a successful run prints nothing. `--log-level info` adds a
summary (files, changed, problems, elapsed time) and `--log-level debug` names
each skipped directory, skipped non-regular file and rendered file. Logs never
contain values.

## Argo CD Config Management Plugin

charmap runs in a CMP sidecar before the renderer, for example
[argocd-lovely-plugin](https://github.com/crumbhole/argocd-lovely-plugin).
Build the sidecar image with charmap baked in, rather than copying the binary
in at pod start:

```dockerfile
FROM docker.io/ashtonian/charmap:2.0.0@sha256:74df476a9a2dd40e46c550935da25e76bbc75e308b383e03061c91c23a6bdabc AS charmap
FROM ghcr.io/crumbhole/argocd-lovely-plugin-cmp:v2.0.0@sha256:7fb7711233d2aa76505bca5930b056bce6f4d0c18a2d905af98d10ee0094e894
COPY --from=charmap /usr/local/bin/charmap /usr/local/bin/charmap
```

[argo-fleet](https://github.com/ashtonian/argo-fleet) is a complete example:
a fleet of Argo CD instances whose cluster identity charmap substitutes
through argocd-lovely-plugin, with CI that renders every Application inside
the plugin image and installs the whole fleet into a kind cluster.

The `generate` command must stop when charmap fails. Without `set -e` a shell
carries on to the renderer, which then emits the unreplaced tokens and the
plugin reports success:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ConfigManagementPlugin
metadata:
  name: lovely-charmap
spec:
  version: v1.0
  generate:
    command: [bash, -c]
    args:
      - |
        set -euo pipefail
        # Applications must not configure the pipeline (see below).
        while IFS= read -r name; do unset "$name"; done < <(compgen -e | grep '^ARGOCD_ENV_' || true)
        : "${GROUP:?}" "${CLUSTER_NAME:?}"
        charmap render --env GROUP --env CLUSTER_NAME
        exec argocd-lovely-plugin
```

Where the values come from:

- **A Secret, every key** (`envFrom` with a `prefix` on the sidecar
  container): read them with `--env-prefix`, as in [Examples](#examples).
  Adding a value is then adding a key to the Secret. argo-fleet works this
  way.
- **Sidecar environment** (for example `env` with `secretKeyRef` on the
  sidecar container): read them with `--env NAME`. Argo CD's manifest cache
  key does not include the sidecar's environment. After changing a value,
  restart the repo-server pod and hard-refresh the affected Applications, or
  wait for the next commit.
- **The Application spec** (`spec.source.plugin.env`): Argo CD passes each
  entry as `ARGOCD_ENV_<NAME>`, so use `--env-prefix ARGOCD_ENV_`. These values
  are part of the cache key, so a change re-renders. With argocd-lovely-plugin,
  note that it reads its own settings from the same variables, including
  commands to run (`ARGOCD_ENV_LOVELY_PREPROCESSORS`): anyone who can edit an
  Application can run commands in the sidecar. If Applications should not
  configure the plugin, read identity from the sidecar instead and drop
  Application-supplied variables before running lovely, as the example's
  first line does.

The generate command runs in the Application's source directory and charmap
processes `--dir .` by default. Tokens in files outside it, such as Kustomize
bases referenced as `../../base`, are not replaced. To cover the whole
checked-out repository, run from its root:

```sh
charmap render --dir "${PWD%"/$ARGOCD_APP_SOURCE_PATH"}" --env GROUP --env CLUSTER_NAME
```

Each render gets a private copy of the repository, so rewriting it does not
affect other renders. Covering the whole repository costs a walk of all of
it on every render.

## Security

- **Environment.** charmap reads only the environment variables named by
  `--env` or selected by `--env-prefix`. Repository content cannot pull in an
  arbitrary variable by writing its name as a token. Choose a prefix that
  matches only variables meant for substitution.
- **Logs and errors** contain keys, paths and positions, never values. A
  malformed `--set` argument is not echoed, because it may be a value.
- **Filesystem.** Every file operation goes through an `os.Root` opened on
  `--dir`, so no path or symlink can reach outside it. Symlinks and
  non-regular files inside it are skipped as well.
- **Output.** Values are inserted verbatim. Anyone who can set a value
  controls the bytes at every place its token appears. Treat value sources as
  trusted configuration.
- **Image.** Distroless static base, nonroot user, no shell or package
  manager. Releases publish SBOM and provenance attestations.

## When to use something else

- **Structured values** (lists, maps, values that need YAML or JSON
  escaping): Helm values or Kustomize `replacements` place data in specific
  fields, which string substitution cannot do safely.
- **Lenient substitution** where a missing variable should become empty:
  `envsubst` does that; charmap treats it as an error by design.
- **Per-cluster patches** that change structure: Kustomize overlays.

charmap fits when plain string values must be filled into many files before
rendering, from a source you name, and a missing or malformed token must
stop the pipeline.

## Troubleshooting

| Symptom | Check | Then |
|---|---|---|
| `no value for KEY` | Is `KEY` given by `--set`, `--env` or `--env-prefix`? Keys are case-sensitive. | Add the source. In a CMP, check the variable exists in the sidecar: `kubectl exec <repo-server-pod> -c <sidecar> -- env`. |
| `--env NAME: environment variable is not set` | Is the variable defined in the process environment? | In a CMP, check the sidecar container's `env` and that the referenced Secret key exists. |
| `malformed token` | Look at the reported line and column. | Fix the token (key characters, no spaces). If the file legitimately contains the delimiter, `--exclude` it or change `--open`/`--close`. |
| Exit 0 but tokens remain in a file | Was the file selected? Run with `--log-level debug`. | The default `--include` selects only `.yaml`/`.yml`. Symlinks, `.git` directories and excluded paths are skipped. In a CMP, the file may be outside the Application directory (see above). |
| Tokens reach the cluster from a CMP | Does the generate script stop on failure? | Add `set -eu`, as in the example above. |
| `file is N bytes, over the M byte limit` | Is the file meant to be processed? | Exclude it, or raise `--max-file-size`. |
| `file changed during the run; not overwritten` | Is something else writing the tree? | Stop the other writer and run again. Nothing was written for that file. |
| `preserve owner U:G: operation not permitted` | Does the file belong to another user? | Run as the file's owner or as root, or change the file's owner first. |
| `create temporary file: … permission denied` | Can the running user write to the file's directory? In Docker, the image runs as UID 65532. | Run as the files' owner, e.g. `docker run --user "$(id -u):$(id -g)" …`. |
| `exec format error` when running charmap | `charmap version` on a working host prints the platform it was built for. | Use a v2 image. v1.0.2's arm64 image contained an amd64 binary. |
| `unexpected positional argument(s)` | A flag is missing its `--` or a value has a space. | Every option is a flag, for example `--dir manifests`. |

## Development

```sh
go test -race ./...                                           # unit and integration tests
go test -run '^$' -fuzz FuzzRender -fuzztime 60s ./internal/token
go test -run '^$' -bench . -benchmem ./internal/...           # benchmarks
golangci-lint run ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
scripts/smoke.sh                                              # builds and exercises the binary
docker buildx build --platform linux/amd64,linux/arm64 -t charmap:dev .
```

`FuzzRender` checks the scanner against an independent regular-expression
implementation on every input it accepts. The `internal/tree` tests run
against real temporary directories, covering partial failures, symlinks, hard
links, FIFOs, pruning, size limits, concurrent modification and cancellation.

**CI** (`.github/workflows/ci.yaml`) runs on pull requests and before every
release. It tests on Linux and macOS with the two supported Go versions,
runs lint, govulncheck, actionlint, shellcheck and the smoke test. It also
builds the image for both platforms and checks with `scripts/verify-image.sh`
that each platform runs a binary built for it.

**Docker Hub description** (`.github/workflows/dockerhub-description.yaml`)
copies this README to the Docker Hub page whenever it changes on `main`.

**Releases** (`.github/workflows/release.yaml`) run on every push to `main`.
Versions follow [Conventional Commits](https://www.conventionalcommits.org/):
`fix:` releases a patch, `feat:` a minor version, and a `BREAKING CHANGE:`
footer or `!` a major version. Other commit types do not release. The workflow
works out the next version, builds, pushes and verifies the image under that
version, and only then creates the Git tag and GitHub release and moves the
`latest`, major and minor image tags. If any step fails, no tag exists and a
re-run produces the same version. It needs the `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` repository secrets.

## Code map

| Path | Responsibility |
|---|---|
| `main.go` | Commands, flags, wiring, signal handling, exit codes. |
| `internal/token` | The scanner: token grammar, delimiter validation, problems with positions, output limit. No I/O. |
| `internal/values` | Builds the value map from `--set`, `--env` and `--env-prefix`. |
| `internal/tree` | Walks `--dir`, reads files safely, renders them with a bounded worker pool, stages output and commits it by rename. |
| `scripts/smoke.sh` | End-to-end checks of the built binary. |
| `scripts/verify-image.sh` | Checks each platform of an image runs a binary for that platform. |
| `Dockerfile` | Cross-compiled multi-platform build onto distroless static. |
| `.github/workflows/` | CI, release and the Docker Hub description. |
| `.releaserc.yaml` | semantic-release configuration. |

## License

GPL-3.0. See [LICENSE](LICENSE).
