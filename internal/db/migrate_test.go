package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/conbench/conbench/internal/db"
	"github.com/conbench/conbench/internal/dbtest"
)

func TestMigrateCreatesAndRecordsFreshSchema(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)

	require.NoError(t, db.Migrate(ctx, pool))

	var resultTable, migrationTable *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT to_regclass('public.benchmark_result')::text,
		       to_regclass('public.conbench_schema_migration')::text
	`).Scan(&resultTable, &migrationTable))
	assert.NotNil(t, resultTable)
	assert.NotNil(t, migrationTable)

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM conbench_schema_migration`).Scan(&count))
	assert.Equal(t, db.MigrationCount(), count)

	require.NoError(t, db.Migrate(ctx, pool))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM conbench_schema_migration`).Scan(&count))
	assert.Equal(t, db.MigrationCount(), count)
}

func TestMigrateAdoptsExistingSchema(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	_, err := pool.Exec(ctx, db.SchemaSQL)
	require.NoError(t, err)

	require.NoError(t, db.Migrate(ctx, pool))

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM conbench_schema_migration`).Scan(&count))
	assert.Equal(t, db.MigrationCount(), count)
}

// legacyDatabaseAtRevision shapes an empty database like one from before Go
// owned the schema: baseline tables, the old revision ledger, and no Go ledger
// or submission-idempotency additions.
func legacyDatabaseAtRevision(ctx context.Context, t *testing.T, pool *pgxpool.Pool, revision string) {
	t.Helper()
	_, err := pool.Exec(ctx, db.SchemaSQL)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		DROP TABLE public.conbench_schema_migration;
		DROP INDEX public.benchmark_result_submission_key_index;
		ALTER TABLE public.benchmark_result
			DROP CONSTRAINT benchmark_result_submission_idempotency_check,
			DROP COLUMN submission_payload_sha256,
			DROP COLUMN submission_key;
		CREATE TABLE public.alembic_version (version_num varchar(32) NOT NULL PRIMARY KEY)
	`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO public.alembic_version (version_num) VALUES ($1)`, revision)
	require.NoError(t, err)
}

func TestMigrateAdoptsLegacyBaselineRevision(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	legacyDatabaseAtRevision(ctx, t, pool, "9d5f3c1a7b2e")

	require.NoError(t, db.Migrate(ctx, pool))

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM conbench_schema_migration`).Scan(&count))
	assert.Equal(t, db.MigrationCount(), count)
	var legacyLedger *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.alembic_version')::text`).Scan(&legacyLedger))
	assert.Nil(t, legacyLedger)
}

func TestMigrateAdoptsPriorSubmissionRevision(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	_, err := pool.Exec(ctx, db.SchemaSQL)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		DROP TABLE public.conbench_schema_migration;
		CREATE TABLE public.alembic_version (version_num varchar(32) NOT NULL PRIMARY KEY);
		INSERT INTO public.alembic_version (version_num) VALUES ('a6b7c8d9e0f1')
	`)
	require.NoError(t, err)

	require.NoError(t, db.Migrate(ctx, pool))

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM conbench_schema_migration`).Scan(&count))
	assert.Equal(t, db.MigrationCount(), count)
}

func TestMigrateRejectsUnmarkedExistingDatabase(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	_, err := pool.Exec(ctx, db.SchemaSQL)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DROP TABLE public.conbench_schema_migration`)
	require.NoError(t, err)

	err = db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "no recognized schema revision")
}

func TestMigrateRejectsPreHeadAlembicDatabase(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	legacyDatabaseAtRevision(ctx, t, pool, "c4f9e2a1d6b8")

	err := db.Migrate(ctx, pool)
	require.ErrorContains(t, err, "c4f9e2a1d6b8")
	require.ErrorContains(t, err, "9d5f3c1a7b2e")

	var ledger *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.conbench_schema_migration')::text`).Scan(&ledger))
	assert.Nil(t, ledger, "rejected database must not gain a migration ledger")

	_, err = pool.Exec(ctx, `UPDATE public.alembic_version SET version_num = '9d5f3c1a7b2e'`)
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx, pool))
}

func TestMigrateUpgradesPreIdempotencySchema(t *testing.T) {
	pool, ctx := dbtest.NewEmptyPool(t)
	_, err := pool.Exec(ctx, db.SchemaSQL)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		DROP INDEX public.benchmark_result_submission_key_index;
		ALTER TABLE public.benchmark_result
			DROP CONSTRAINT benchmark_result_submission_idempotency_check,
			DROP COLUMN submission_payload_sha256,
			DROP COLUMN submission_key
	`)
	require.NoError(t, err)

	require.NoError(t, db.Migrate(ctx, pool))

	var keyColumn, hashColumn, constraintValid, indexValid bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'public.benchmark_result'::regclass AND attname = 'submission_key' AND NOT attisdropped),
			EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'public.benchmark_result'::regclass AND attname = 'submission_payload_sha256' AND NOT attisdropped),
			EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.benchmark_result'::regclass AND conname = 'benchmark_result_submission_idempotency_check' AND convalidated),
			EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = 'public.benchmark_result_submission_key_index'::regclass AND indisunique AND indisvalid)
	`).Scan(&keyColumn, &hashColumn, &constraintValid, &indexValid))
	assert.True(t, keyColumn)
	assert.True(t, hashColumn)
	assert.True(t, constraintValid)
	assert.True(t, indexValid)
}
