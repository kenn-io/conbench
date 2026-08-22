# Migrating From The Python Application

The maintained Conbench is a Go server, Svelte dashboard, and CLI-first
submission tool. The retired Flask application and its Python package ecosystem
are not compatibility surfaces for the new implementation.

## Migration Shape

1. Inventory the result payloads, read queries, CI reports, alerts, and routes
   used by the old deployment.
2. Deploy the Go server against a restored production snapshot and run the
   [production-clone checks](../prod-clone-compatibility.md).
3. Replace benchmark writes with Conbench result JSON plus
   `conbench results submit`.
4. Replace read automation with the HTTP API or generated Go client.
5. Replace scheduled Python alert jobs with server alert rules and the
   `conbench admin alerts` commands.
6. Cut traffic over only after result counts and representative workflows agree.

The existing Postgres data remains usable. Run `conbench migrate` with the new
server image before starting the application; the Go migrator serializes the
embedded numbered SQL migrations and records their version and dirty state in
`schema_migrations`.

## Result Submission

Each input file contains one Conbench result object. Existing Python benchmark
code can keep constructing dictionaries, but it should serialize those objects
to JSON and invoke the CLI as a subprocess:

```bash
conbench results submit "bench-results/*.json" \
  --server "$CONBENCH_SERVER_URL" \
  --token "$CONBENCH_TOKEN"
```

The CLI prints one JSON identity object per successful submission. Treat a
non-zero exit as a failed publish and retain the input file for retry.

## Read Automation

Use `/openapi.yaml` to generate a client in the language of the calling system,
call the HTTP API directly, or use the generated Go client in
`sdk/go/conbench`. There is no maintained Python SDK in this repository.

## CI And Alerts

Use `conbench ci report` after publishing a run. Scheduled alert evaluation and
delivery use:

```bash
conbench admin alerts evaluate
conbench admin alerts deliver --channel webhook
```

These commands use the same Go storage and analysis implementation as the
server, avoiding a second application runtime with different behavior.

## Removed Packages

`benchadapt`, `benchclients`, `benchconnect`, `benchrun`, `benchalerts`, the
Flask `conbench` package, and `conbench_client` are retired. Do not preserve
their import paths with adapters. Move integrations to the JSON/CLI or HTTP
boundaries explicitly.

The [legacy parity roadmap](legacy-parity-roadmap.md) records the supported
replacement for each product workflow, and the
[deprecation notices](deprecation-notices.md) provide concise language for old
package pages and release notes.
