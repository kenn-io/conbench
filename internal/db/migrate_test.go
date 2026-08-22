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
