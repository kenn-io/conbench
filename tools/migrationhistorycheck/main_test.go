package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateMigrationHistoryAllowsBootstrapFromEmptyHistory(t *testing.T) {
	err := validateMigrationHistory(nil, []string{
		"internal/db/migrations/000001_initial_schema.up.sql",
		"internal/db/migrations/000001_initial_schema.down.sql",
		"internal/db/migrations/000002_submission_idempotency.up.sql",
		"internal/db/migrations/000002_submission_idempotency.down.sql",
	}, nil)
	assert.NoError(t, err)
}

func TestValidateMigrationHistoryAllowsOneMigrationAfterBootstrap(t *testing.T) {
	base := []string{
		"internal/db/migrations/000001_initial_schema.up.sql",
		"internal/db/migrations/000001_initial_schema.down.sql",
	}
	candidate := append(append([]string(nil), base...),
		"internal/db/migrations/000002_next.up.sql",
		"internal/db/migrations/000002_next.down.sql",
	)
	assert.NoError(t, validateMigrationHistory(base, candidate, nil))
}

func TestValidateMigrationHistoryRejectsMultipleNewMigrations(t *testing.T) {
	base := []string{
		"internal/db/migrations/000001_initial_schema.up.sql",
		"internal/db/migrations/000001_initial_schema.down.sql",
	}
	candidate := append(append([]string(nil), base...),
		"internal/db/migrations/000002_first.up.sql",
		"internal/db/migrations/000002_first.down.sql",
		"internal/db/migrations/000003_second.up.sql",
		"internal/db/migrations/000003_second.down.sql",
	)
	err := validateMigrationHistory(base, candidate, nil)
	assert.ErrorContains(t, err, "only one new migration")
}

func TestValidateMigrationHistoryRejectsChangedBaseMigration(t *testing.T) {
	base := []string{
		"internal/db/migrations/000001_initial_schema.up.sql",
		"internal/db/migrations/000001_initial_schema.down.sql",
	}
	err := validateMigrationHistory(base, base, []string{"internal/db/migrations/000001_initial_schema.up.sql"})
	assert.ErrorContains(t, err, "already exists on the comparison base")
}

func TestValidateMigrationHistoryRequiresUpAndDownPair(t *testing.T) {
	err := validateMigrationHistory(nil, []string{
		"internal/db/migrations/000001_initial_schema.up.sql",
	}, nil)
	assert.ErrorContains(t, err, "must have matching .up.sql and .down.sql files")
}

func TestValidateMigrationHistoryRejectsDuplicateNumber(t *testing.T) {
	err := validateMigrationHistory(nil, []string{
		"internal/db/migrations/000001_first.up.sql",
		"internal/db/migrations/000001_first.down.sql",
		"internal/db/migrations/000001_second.up.sql",
		"internal/db/migrations/000001_second.down.sql",
	}, nil)
	assert.ErrorContains(t, err, "migration number 000001")
}

func TestValidateMigrationHistoryRequiresContiguousNumbers(t *testing.T) {
	err := validateMigrationHistory(nil, []string{
		"internal/db/migrations/000001_initial.up.sql",
		"internal/db/migrations/000001_initial.down.sql",
		"internal/db/migrations/000003_later.up.sql",
		"internal/db/migrations/000003_later.down.sql",
	}, nil)
	assert.ErrorContains(t, err, "expected migration 000002")
}
