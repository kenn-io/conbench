package db_test

import (
	"testing"

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
