# GitHub App Commit Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let live commit enrichment and `admin repair-commits` use renewable GitHub App installation tokens while preserving static tokens and the server's local-provider mode.

**Architecture:** `internal/githubapi` owns a concurrency-safe App installation token source that parses the key at construction and refreshes tokens before expiry. `internal/commit` accepts that source through a small consumer interface while retaining its static token pool. A new `internal/commitauth` package is the single environment and key-file loader used by the server and repair command.

**Tech Stack:** Go standard library, `net/http`, RSA JSON Web Token signing, `httptest`, and Testify.

**Spec:** `docs/superpowers/specs/2026-08-26-github-app-commit-enrichment-design.md`

## Global Constraints

- Use `GITHUB_API_TOKEN` for the existing comma-separated static token pool.
- Use only `CONBENCH_COMMIT_GITHUB_APP_ID`, `CONBENCH_COMMIT_GITHUB_APP_INSTALLATION_ID`, and `CONBENCH_COMMIT_GITHUB_APP_PRIVATE_KEY_FILE` for App-backed commit enrichment.
- Keep CI-reporting GitHub App settings independent from commit enrichment.
- Preserve the server's local provider when no commit authentication mode is configured.
- Require one complete commit authentication mode for `admin repair-commits`.
- Reject partial App configuration and simultaneous static-token and App configuration without contacting GitHub.
- Keep static-token rotation and HTTP 403 handling inside the commit client.
- Invalidate and retry an App token once after HTTP 401.
- Add no dependencies, schema changes, webhook support, write permissions, or dashboard changes.
- Keep all examples and tests generic and free of deployment-specific details.

---

### Task 1: Renewable GitHub App installation token source

**Files:**
- Create: `internal/githubapi/app_token_source.go`
- Modify: `internal/githubapi/client.go`
- Modify: `internal/githubapi/client_test.go`

**Interfaces:**
- Consumes: existing `parseRSAPrivateKey`, GitHub App JSON Web Token signing, and `Client.doJSON`.
- Produces: `AppTokenSourceConfig`, `NewAppTokenSource(AppTokenSourceConfig) (*AppTokenSource, error)`, `(*AppTokenSource).Token(context.Context) (string, error)`, and `(*AppTokenSource).Invalidate(string)`.

- [x] **Step 1: Write failing source tests**

Add focused tests with a local HTTP server that require an explicit installation endpoint, cache a token outside the five-minute refresh window, refresh inside the window, and collapse concurrent refreshes to one exchange. Use a generated RSA test key and literal token/expiry responses.

```go
source, err := NewAppTokenSource(AppTokenSourceConfig{
	AppID:           "12345",
	InstallationID: 42,
	AppPrivateKey:  testPrivateKeyPEM(t),
	BaseURL:        server.URL,
	HTTPClient:     server.Client(),
})
require.NoError(t, err)

token, err := source.Token(context.Background())
require.NoError(t, err)
assert.Equal(t, "ghs_installation", token)
```

- [x] **Step 2: Verify the tests fail because the source API does not exist**

Run: `go test -shuffle=on ./internal/githubapi`

Expected: compilation fails with undefined `NewAppTokenSource` or `AppTokenSourceConfig`.

- [x] **Step 3: Implement the minimal source**

Create a source with these fields and methods:

```go
type AppTokenSourceConfig struct {
	AppID           string
	InstallationID int64
	AppPrivateKey  string
	BaseURL         string
	HTTPClient      *http.Client
}

type AppTokenSource struct {
	mu             sync.Mutex
	appID          string
	installationID int64
	privateKey     *rsa.PrivateKey
	baseURL        string
	httpc          *http.Client
	now            func() time.Time
	token          string
	expiresAt      time.Time
}
```

`Token` holds the mutex across refresh, returns the cached token while `now + 5 minutes` is before expiry, signs a fresh App JSON Web Token, posts to `/app/installations/{id}/access_tokens`, and requires both `token` and `expires_at`. `Invalidate` clears the rejected token under the same mutex only when that token is still cached. Split the existing signer so both PEM-input and parsed-key paths share the signing implementation; leave the CI client's one-shot installation discovery behavior unchanged.

- [x] **Step 4: Verify the source tests pass**

Run: `go test -shuffle=on ./internal/githubapi`

Expected: all package tests pass.

### Task 2: Commit client renewable credential support

**Files:**
- Modify: `internal/commit/github_client.go`
- Modify: `internal/commit/github_client_test.go`

**Interfaces:**
- Consumes: any source satisfying `Token(context.Context) (string, error)` and `Invalidate()`.
- Produces: `NewGitHubClientWithTokenSource(GitHubTokenSource, string) *GitHubClient`; all existing callers of `NewGitHubClient` remain valid.

- [x] **Step 1: Write failing commit-client tests**

Add a test source and local HTTP handler that return an expired credential on the first request and a fresh credential after invalidation. Assert the real HTTP operation succeeds after exactly one HTTP 401 retry. Add a second test proving a repeated HTTP 401 stops after the single refresh. Keep the existing static-token rotation test unchanged.

```go
client := NewGitHubClientWithTokenSource(source, server.URL)
branch, err := client.defaultBranch(context.Background(), "org/repo")
require.NoError(t, err)
assert.Equal(t, "org:main", branch)
assert.Equal(t, []string{"Bearer expired", "Bearer fresh"}, authorizations)
```

- [x] **Step 2: Verify the tests fail because the constructor does not exist**

Run: `go test -shuffle=on ./internal/commit`

Expected: compilation fails with undefined `NewGitHubClientWithTokenSource`.

- [x] **Step 3: Implement source-backed request authentication**

Define the consumer-owned interface and add an optional source field:

```go
type GitHubTokenSource interface {
	Token(context.Context) (string, error)
	Invalidate(rejectedToken string)
}
```

Resolve the token for each HTTP attempt. On the first HTTP 401 for a source-backed request, invalidate the source and retry immediately. Return the second HTTP 401 as a permanent error. Do not route HTTP 403 through the App source; retain the existing static-pool quota rotation unchanged.

- [x] **Step 4: Verify commit-client tests pass, including static rotation**

Run: `go test -shuffle=on ./internal/commit`

Expected: all package tests pass.

### Task 3: Shared environment loader and runtime wiring

**Files:**
- Create: `internal/commitauth/config.go`
- Create: `internal/commitauth/config_test.go`
- Modify: `internal/serverapp/app.go`
- Modify: `internal/serverapp/app_test.go`
- Modify: `cmd/conbench/admin.go`
- Modify: `cmd/conbench/main_test.go`

**Interfaces:**
- Consumes: `githubapi.NewAppTokenSource`, `commit.NewGitHubClient`, and `commit.NewGitHubClientWithTokenSource`.
- Produces: `commitauth.Load() (*commit.GitHubClient, error)` for optional server authentication and `commitauth.LoadRequired() (*commit.GitHubClient, error)` for repair.

- [x] **Step 1: Write failing loader and wiring tests**

Test these owned behaviors with isolated environment variables and temporary files:

```go
client, err := Load()
require.NoError(t, err)
assert.Nil(t, client) // zero mode

t.Setenv("CONBENCH_GITHUB_APP_ID", "ci-app")
t.Setenv("CONBENCH_GITHUB_APP_PRIVATE_KEY", "ci-key-contents")
client, err = Load()
require.NoError(t, err)
assert.Nil(t, client) // CI settings are ignored
```

Also require errors for partial App settings, simultaneous static and App modes, non-positive installation IDs, unreadable or oversized key files, and malformed RSA keys. Verify a complete App configuration constructs a client without an HTTP exchange. Update server config tests to preserve a nil client in zero mode and accept a complete App mode. Update repair command tests so zero mode errors while App mode reaches its injected runner.

- [x] **Step 2: Verify the tests fail because the loader and wiring do not exist**

Run: `go test -shuffle=on ./internal/commitauth ./internal/serverapp ./cmd/conbench`

Expected: compilation fails for the missing loader or new config fields.

- [x] **Step 3: Implement the shared loader**

`Load` reads only the static token and three commit-specific App variables. It returns `nil, nil` for zero mode, rejects conflicting or partial App modes, parses a positive base-10 installation ID, reads at most 64 KiB of PEM data, and constructs the App source without making a GitHub request. `LoadRequired` calls `Load`, then rejects zero mode and a static token pool with no usable token.

- [x] **Step 4: Wire server and repair through the loader**

Store the optional client in server configuration so invalid App configuration fails before database connection. Keep `commit.LocalProvider{}` when the client is nil; otherwise create the existing provider and backfiller. Store the required client in `adminRepairConfig` and use it for both repair and optional ancestry backfill. Preserve the repair integration test's injected local API client with a constructor seam that returns `(*commit.GitHubClient, error)`.

- [x] **Step 5: Verify loader, server, and command tests pass**

Run: `go test -shuffle=on ./internal/commitauth ./internal/serverapp ./cmd/conbench`

Expected: all selected package tests pass.

### Task 4: Repository-wide verification and delivery

**Files:**
- Modify only files changed by Tasks 1-3 plus this plan.

**Interfaces:**
- Consumes: all preceding task outputs.
- Produces: a formatted, linted, tested feature commit with no private deployment data.

- [x] **Step 1: Format and run focused concurrency checks**

Run:

```bash
gofmt -w internal/githubapi/app_token_source.go internal/githubapi/client.go internal/githubapi/client_test.go internal/commit/github_client.go internal/commit/github_client_test.go internal/commitauth/config.go internal/commitauth/config_test.go internal/serverapp/app.go internal/serverapp/app_test.go cmd/conbench/admin.go cmd/conbench/main_test.go
go test -race -shuffle=on ./internal/githubapi ./internal/commit ./internal/commitauth
```

Expected: formatting makes no unintended edits and all race-enabled focused tests pass.

- [x] **Step 2: Run repository checks**

Run:

```bash
make go-lint-ci
go test -shuffle=on ./...
```

Expected: lint and the full Go suite pass.

- [x] **Step 3: Review and scrub the outgoing changes**

Review `git diff --check`, `git diff --stat`, and `git diff HEAD`. Scan the exact diff and new commit message using the repository private-data workflow. Remove any deployment-specific identifiers, absolute home paths, private hostnames, or non-public identities before committing.

- [x] **Step 4: Commit the implementation**

Stage only the plan and implementation files. Create a new commit with a concise subject and a body that explains renewable commit metadata enrichment, the separate configuration namespace, and preserved fallback behavior. Do not amend prior commits, push, or open a pull request.
