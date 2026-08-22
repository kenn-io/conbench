<p align="right">
<a href="https://github.com/conbench/conbench/blob/main/.github/workflows/ci.yml"><img alt="Build Status" src="https://github.com/conbench/conbench/actions/workflows/ci.yml/badge.svg?branch=main"></a>
</p>

# Conbench

Check out the docs at https://conbench.github.io/conbench.

For existing Python/Flask Conbench deployments, start with the
[migration guide](docs/site/migration/python-app.md) and
[legacy parity roadmap](docs/site/migration/legacy-parity-roadmap.md) to audit
deleted, replaced, and future surfaces.
Package maintainers should also use the
[legacy deprecation notices](docs/site/migration/deprecation-notices.md).

Current dashboard screenshots are generated reproducibly and documented in
[Dashboard Screenshots](docs/site/dashboard-screenshots.md).
The maintained command surface is documented in the
[CLI reference](docs/site/cli-reference.md).

Conbench lets you publish benchmark results as JSON, persist them, inspect
their history, and catch regressions in CI.

The maintained implementation is a Go API server with an embedded Svelte
dashboard, a CLI-first write path, generated Go and TypeScript clients, and a
Go-owned Postgres schema. The legacy Flask application and Python client stack
have been removed from the maintained codebase; migration guidance lives in the
docs.

<p align="center">
    <img src="https://arrow.apache.org/img/arrow.png" alt="Apache Arrow" height="100">
</p>


The [Apache Arrow](https://arrow.apache.org/) project uses Conbench for
continuous benchmarking across Python, C++, R, Java, and JavaScript performance
suites. Arrow benchmark jobs emit Conbench-compatible result JSON at the
boundary, so benchmark execution can stay in the language and harness that best
fits the project while Conbench owns submission, storage, history, and
regression analysis. Example benchmark code can be found in the
[ursacomputing/benchmarks](https://github.com/ursacomputing/benchmarks)
repository, and the results are hosted on the
[Arrow Conbench Server](https://conbench.ursa.dev/).


- May 2021: https://ursalabs.org/blog/announcing-conbench/


<br>


## Installation And Migration

For the new system, build or install the Go CLI and submit result JSON with
`conbench results submit`:

```bash
make build
export CONBENCH_TOKEN=<token>
./bin/conbench results submit "bench-results/*.json" \
  --server "$CONBENCH_SERVER_URL"
```

Legacy Python package code is not the forward compatibility contract.
`conbench/` (the Flask app), `benchadapt/`, `benchclients/`, `benchconnect/`,
`benchrun/`, `benchalerts/`, and the old `legacy/conbenchlegacy` runner have
been removed from the maintained codebase. See the
[Python app migration guide](docs/site/migration/python-app.md) for
package-by-package guidance.

## Repository Layout

The maintained source tree is intentionally small:

- `cmd/` and `internal/`: Go server, CLI, services, storage, auth, and tests.
- `web/`: Svelte dashboard source and frontend tests.
- `api/`: generated OpenAPI contract artifacts reviewed with codegen changes.
- `sdk/`: the generated Go client.
- `docs/site/`: public Markdown/Zensical documentation.
- `scripts/`, `k8s/`, and Compose files: local checks, packaging, and deploy
  rendering for the single `conbench` binary.

Generated local state such as `bin/`, `site/`, `var/`, `.cache/`,
`web/node_modules`, Python cache directories, built
`web/dist` assets, and web test artifacts is ignored. Run `make clean-local` to
remove those generated artifacts from a checkout.


## Developer Environment

### Dependencies

- [`make`](https://www.gnu.org/software/make/)
- Go, Bun, uv, and Docker for the full local gate set
- `GITHUB_API_TOKEN` only for tests or workflows that need live GitHub commit
  metadata

### Makefile targets

Run these from the repository root:

* `make build`: Builds the Go server and CLI.
* `make go-test`: Runs the full Go test suite.
* `make go-test-short`: Runs the fast Go suite without Postgres-backed tests.
* `make go-lint-ci`: Runs the Go lint gate.
* `make codegen-check`: Regenerates OpenAPI and clients and fails on drift.
* `make migration-history-check`: Rejects edits to shipped migrations,
  duplicate numbers, missing up/down pairs, and—after the initial
  bootstrap—multiple migrations per PR.
* `make build-docs`: Builds the pinned Zensical docs site into `site/`.
* `make e2e`: Runs the keystone end-to-end stack.
* `make clean-local`: Removes generated local artifacts such as `bin/`,
  `site/`, `var/`, `.cache/`, `web/node_modules`, Python cache directories,
  built `web/dist` assets, and web test output.

### View API documentation

The Go server exposes OpenAPI at `/openapi.yaml` and interactive docs at
`/docs`.

### Schema migrations

The Go binary owns schema installation and upgrades. Production deploys run the
same image as the server with `conbench migrate` before starting the
application. New and existing installations advance through the same embedded,
numbered SQL migrations. Go records the current version and dirty state in
`schema_migrations`.

### To add new documentation pages

Add Markdown files under `docs/site/` and update `zensical.toml` navigation when
the page should be public. Run `make build-docs` before committing.

## Auth And Accounts

The new server has open reads by default. Writes require an API token. Users can
create tokens after OIDC/session login, and the CLI supports loopback
`conbench auth login` for interactive token setup.
