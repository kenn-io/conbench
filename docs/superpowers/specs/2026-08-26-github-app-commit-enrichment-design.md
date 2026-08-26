# GitHub App Commit Enrichment

## Problem

Conbench enriches submitted commit hashes through the GitHub API. Its commit
client currently accepts only static personal access tokens. Private-repository
deployments therefore have to attach commit enrichment to a person, or store
minimal commit rows whose message and author fields remain unknown.

Conbench already knows how to exchange GitHub App credentials for an
installation token in its CI-reporting client. That client mints one token when
it starts and does not refresh it, so it cannot serve the long-running commit
enrichment path.

## Goals

- Authenticate commit enrichment with a repository-scoped GitHub App.
- Refresh expiring installation tokens without restarting Conbench.
- Use the same authentication mode for live ingestion and `admin
  repair-commits`.
- Preserve static token-pool authentication as a separate supported mode.
- Repair existing minimal commit rows without resubmitting benchmark results.
- Keep GitHub outages from making benchmark ingestion unavailable.

## Non-goals

- Creating or managing GitHub Apps through Conbench.
- Adding webhook handling or write permissions.
- Changing benchmark result, run, series, or commit identities.
- Changing the dashboard's author fallback.
- Generalizing authentication for Git hosting providers other than GitHub.

## Configuration

Commit enrichment supports exactly one of these authentication modes:

1. `GITHUB_API_TOKEN`, the existing comma-separated static token pool.
2. A GitHub App configured with all of:
   - `CONBENCH_GITHUB_APP_ID`
   - `CONBENCH_GITHUB_APP_INSTALLATION_ID`
   - `CONBENCH_GITHUB_APP_PRIVATE_KEY_FILE`

The private-key setting names a readable PEM file. Production deployments can
provide that file through their service manager's credential mechanism;
Conbench does not require the PEM contents in an environment variable.

Startup and `admin repair-commits` reject partial App configuration and reject
configuration that supplies both authentication modes. They validate the IDs,
read the bounded key file, and parse its RSA private key before serving or
repairing. Startup validation does not contact GitHub, so a transient GitHub
outage does not prevent Conbench from starting.

The App itself needs read-only Metadata and Contents permissions. It needs no
webhook and no write permission. Installation scope is a deployment decision;
the recommended scope is only the repositories whose commits Conbench must
enrich.

## Token source

The GitHub API package owns a reusable App token source. It:

1. Signs a short-lived App JSON Web Token with the configured private key.
2. Exchanges it at the explicitly configured installation endpoint.
3. Records the installation token and GitHub's `expires_at` value.
4. Returns the cached token until five minutes before expiry.
5. Serializes refresh so concurrent requests share one exchange.

The source exposes token acquisition and invalidation, not HTTP request
semantics. This keeps commit enrichment responsible for its existing request,
quota, and response behavior while allowing CI-reporting code to adopt the
same renewable source separately if needed.

The commit client asks its credential source for a token for each request. The
App source normally answers from memory. If GitHub returns HTTP 401, the client
invalidates the App token and retries that request once with a newly minted
token. Other responses keep the current commit client's behavior. Static token
pools retain their existing rotation and quota handling.

## Ingestion and repair

The server builds the commit client from the validated authentication mode.
Successful App authentication follows the existing enrichment path and stores
the commit message, author identity, timestamp, branch, and ancestry metadata.

Authentication, quota, or network failures continue to degrade to a minimal
commit row. The benchmark result remains accepted, and existing unknown-commit
metrics and logs identify the failed enrichment. This preserves ingestion
availability.

`admin repair-commits` uses the same configuration loader and renewable token
source. Operators first run a repository-filtered dry run, inspect its summary,
and then apply the repair. Repair updates existing commit rows in place, so
already-published benchmark results gain commit authors without rerunning or
resubmitting benchmarks.

## Deployment flow

1. Register a dedicated App with read-only Metadata and Contents permissions,
   no webhook, and repository-selected installation scope.
2. Install its private key through the deployment's credential mechanism and
   set the App and installation IDs.
3. Start Conbench and exercise one authenticated commit lookup through the
   running enrichment path.
4. Run a repository-filtered `repair-commits --dry-run --format json`.
5. Apply the repair without ancestry backfill unless that separate behavior is
   explicitly required.
6. Verify an existing result and run expose the repaired commit author.

Rollback removes the App settings and credential, then restores the prior
authentication mode if one existed. Stored commit repairs remain valid data and
do not require rollback.

## Verification

Focused Go tests use local HTTP servers to verify:

- App JWT exchange targets the configured installation.
- A cached installation token is reused before its refresh window.
- An expiring token is refreshed once under concurrent demand.
- HTTP 401 invalidates the token and retries the commit request once.
- Partial, conflicting, unreadable, or malformed App configuration is rejected.
- Static token-pool behavior remains unchanged.
- Live ingestion and commit repair construct the same App-backed commit client.

Operational verification exercises the requested success path: the configured
App enriches a commit, and repair makes an existing unknown author visible
through the public API. Local tests retain coverage of ingestion's existing
degraded behavior when GitHub is unavailable.
