# Legacy Package Deprecation Notices

Use this language when retiring packages from the Python/Flask Conbench
implementation. The replacement boundary is explicit and does not promise
source-compatible shims.

## General Notice

```text
This package belongs to the retired Python/Flask Conbench implementation.
Maintained Conbench uses a Go server, Svelte dashboard, `conbench` CLI, and
OpenAPI.

Do not build new automation on this package. Emit Conbench result JSON and
submit it with `conbench results submit`. Run `conbench ci report` for CI
diagnostics. Use the HTTP API or generated Go client for read automation.

Migration guide:
https://conbench.github.io/conbench/migration/python-app/
```

## Result And HTTP Clients

Apply the general notice to `benchadapt`, `benchconnect`, and `benchclients`.
Benchmark projects may keep useful payload-construction code, but must write the
result objects as JSON and cross the supported CLI boundary:

```bash
conbench results submit "bench-results/*.json" \
  --server "$CONBENCH_SERVER_URL" \
  --jobs 16
```

Password-login sessions, implicit run lifecycle helpers, old request
augmentation, and retired import names are not preserved.

## Alerts

`benchalerts` is retired. Use `conbench ci report` for synchronous pull request
diagnostics. Scheduled alert state belongs to the server: create alert rules
through the dashboard or authenticated API, run
`conbench admin alerts evaluate`, then use `conbench admin alerts deliver` for
the configured delivery channel.

## Runners

`benchrun` and `conbenchlegacy` are retired. Benchmark orchestration belongs in
the benchmark project. Keep the useful execution code, emit Conbench result
JSON at the boundary, invoke the Go CLI, and remove old decorators and runner
imports after the new path works.

## Flask Application

The Python/Flask application package is retired. The maintained server is
`conbench serve` with the embedded Svelte dashboard. Operators run
`conbench migrate` before deploying the server and should stop depending on
Flask routes, Jinja templates, password-login sessions, SQLAlchemy application
objects, and Flask-specific configuration.

## Publication Checklist

When updating a package README, release note, or project page:

- link the [migration guide](python-app.md),
- name the JSON/CLI submission path,
- name `conbench ci report` for CI,
- direct reads to HTTP or the generated Go client,
- state that source-compatible imports are intentionally unsupported,
- leave old distributions available for pinned environments unless a security
  or legal reason requires removal.
