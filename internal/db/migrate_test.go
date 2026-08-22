package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conbench/conbench/internal/db"
	"github.com/conbench/conbench/internal/dbtest"
)

const latestMigrationVersion = 2

func TestMigrateCreatesAndRecordsFreshSchema(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)

	require.NoError(t, db.Migrate(ctx, pool))
	assertCurrentMigration(t, ctx, pool)

	require.NoError(t, db.Migrate(ctx, pool))
	assertCurrentMigration(t, ctx, pool)
}

func TestMigrateAdoptsLegacyBaselineRevision(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	createLegacyRevision(t, ctx, pool, "9d5f3c1a7b2e")

	require.NoError(t, db.Migrate(ctx, pool))
	assertCurrentMigration(t, ctx, pool)
	assertLegacyLedgerRemoved(t, ctx, pool)
}

func TestMigrateNormalizesLegacySubmissionRevision(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	_, err := pool.Exec(ctx, `
		ALTER TABLE public.benchmark_result
			ADD COLUMN submission_key text,
			ADD COLUMN submission_payload_sha256 text;
		ALTER TABLE public.benchmark_result
			ADD CONSTRAINT benchmark_result_submission_idempotency_check
			CHECK (
				(submission_key IS NULL AND submission_payload_sha256 IS NULL)
				OR (submission_key IS NOT NULL AND submission_payload_sha256 ~ '^[0-9a-f]{64}$')
			);
		CREATE UNIQUE INDEX benchmark_result_submission_key_index
			ON public.benchmark_result (submission_key)
			WHERE submission_key IS NOT NULL
	`)
	require.NoError(t, err)
	createLegacyRevision(t, ctx, pool, "a6b7c8d9e0f1")

	require.NoError(t, db.Migrate(ctx, pool))
	assertCurrentMigration(t, ctx, pool)
	assertLegacyLedgerRemoved(t, ctx, pool)

	insertBenchmarkDependencies(t, ctx, pool)
	_, err = pool.Exec(ctx, `
		INSERT INTO public.benchmark_result (
			id, case_id, context_id, info_id, hardware_id, run_id, run_tags,
			"timestamp", commit_repo_url, history_fingerprint,
			submission_key, submission_payload_sha256
		) VALUES (
			'result-1', 'case-1', 'context-1', 'info-1', 'hardware-1', 'run-1', '{}',
			now(), 'repo', 'fingerprint', 'key-without-hash', NULL
		)
	`)
	require.Error(t, err, "the current constraint must reject a keyed result without a payload hash")
}

func TestMigrateRejectsUnmarkedExistingDatabase(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "no recognized schema revision")
	assertTableMissing(t, ctx, pool, "schema_migrations")
}

func TestMigrateRejectsUnsupportedLegacyRevisionWithoutMutation(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	createLegacyRevision(t, ctx, pool, "c4f9e2a1d6b8")

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "c4f9e2a1d6b8")
	require.ErrorContains(t, err, "9d5f3c1a7b2e")
	assertTableMissing(t, ctx, pool, "schema_migrations")
}

func TestMigrateUpgradesVersionOneSchema(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	createMigrationLedger(t, ctx, pool, 1, false)

	require.NoError(t, db.Migrate(ctx, pool))
	assertCurrentMigration(t, ctx, pool)
}

func TestMigrateRejectsDirtyVersion(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	createMigrationLedger(t, ctx, pool, 1, true)

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "dirty migration state at version 1")
}

func TestMigrateRejectsNewerVersion(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	applyInitialSchema(t, ctx, pool)
	createMigrationLedger(t, ctx, pool, 99, false)

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "version 99 is newer than this binary")
}

func TestMigrateRejectsMixedSchemaOwnership(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	require.NoError(t, db.Migrate(ctx, pool))
	createLegacyRevision(t, ctx, pool, "9d5f3c1a7b2e")

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "both Go and legacy migration ledgers")
}

func TestMigrateRejectsSubmissionIndexDriftAtCurrentVersion(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	require.NoError(t, db.Migrate(ctx, pool))
	_, err := pool.Exec(ctx, `
		DROP INDEX public.benchmark_result_submission_key_index;
		CREATE UNIQUE INDEX benchmark_result_submission_key_index
			ON public.benchmark_result (submission_key)
	`)
	require.NoError(t, err)

	err = db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "submission idempotency schema is incomplete")
}

func TestMigrateRejectsSubmissionIndexOnWrongTableAtCurrentVersion(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	require.NoError(t, db.Migrate(ctx, pool))
	_, err := pool.Exec(ctx, `
		DROP INDEX public.benchmark_result_submission_key_index;
		CREATE TABLE public.other_submission_keys (
			submission_key text
		);
		CREATE UNIQUE INDEX benchmark_result_submission_key_index
			ON public.other_submission_keys (submission_key)
			WHERE submission_key IS NOT NULL
	`)
	require.NoError(t, err)

	err = db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "submission idempotency schema is incomplete")
}

func TestMigrateRejectsSubmissionConstraintDriftAtCurrentVersion(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	require.NoError(t, db.Migrate(ctx, pool))
	_, err := pool.Exec(ctx, `
		ALTER TABLE public.benchmark_result
			DROP CONSTRAINT benchmark_result_submission_idempotency_check;
		ALTER TABLE public.benchmark_result
			ADD CONSTRAINT benchmark_result_submission_idempotency_check
			CHECK (
				(submission_key IS NULL AND submission_payload_sha256 IS NULL)
				OR (submission_key IS NOT NULL AND submission_payload_sha256 ~ '^[0-9a-f]{64}$')
			)
	`)
	require.NoError(t, err)

	err = db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "submission idempotency schema is incomplete")
}

func applyInitialSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	schema, err := os.ReadFile("migrations/000001_initial_schema.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(schema))
	require.NoError(t, err)
}

func createLegacyRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, revision string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		CREATE TABLE public.alembic_version (
			version_num varchar(32) NOT NULL PRIMARY KEY
		)
	`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO public.alembic_version (version_num) VALUES ($1)`, revision)
	require.NoError(t, err)
}

func createMigrationLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version int, dirty bool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		CREATE TABLE public.schema_migrations (
			version bigint NOT NULL PRIMARY KEY,
			dirty boolean NOT NULL
		)
	`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO public.schema_migrations (version, dirty) VALUES ($1, $2)`, version, dirty)
	require.NoError(t, err)
}

func assertCurrentMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var version int
	var dirty bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT version, dirty FROM public.schema_migrations`).Scan(&version, &dirty))
	assert.Equal(t, latestMigrationVersion, version)
	assert.False(t, dirty)
}

func assertLegacyLedgerRemoved(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	assertTableMissing(t, ctx, pool, "alembic_version")
}

func assertTableMissing(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
	assert.False(t, exists)
}

func insertBenchmarkDependencies(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO public."case" (id, name, tags) VALUES ('case-1', 'bench', '{}');
		INSERT INTO public.context (id, tags) VALUES ('context-1', '{}');
		INSERT INTO public.info (id, tags) VALUES ('info-1', '{}');
		INSERT INTO public.hardware (id, name, type, hash) VALUES ('hardware-1', 'host', 'machine', 'hash-1')
	`)
	require.NoError(t, err)
}
