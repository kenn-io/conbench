package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 0x436f6e62656e6368

const (
	legacyBaselineRevision   = "9d5f3c1a7b2e"
	legacySubmissionRevision = "a6b7c8d9e0f1"
)

type migration struct {
	version int64
	name    string
	apply   func(context.Context, *pgx.Conn) error
}

var migrations = []migration{
	{version: 1, name: "go-schema-baseline", apply: finishLegacySchemaHandoff},
	{version: 2, name: "submission-idempotency", apply: addSubmissionIdempotency},
}

// MigrationCount reports the number of schema revisions built into this
// binary. It is primarily useful to operational status and integration tests.
func MigrationCount() int {
	return len(migrations)
}

// Migrate brings a database to the schema version embedded in this binary.
// A session advisory lock serializes migrators across deploy jobs. Existing
// databases are adopted only when they have a Go migration ledger or an exact
// supported legacy revision. Other states are rejected before anything is
// modified. Subsequent migrations are idempotent and recorded by version and
// name.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	var schemaExists, ledgerExists bool
	if err := conn.QueryRow(ctx, `
		SELECT to_regclass('public.benchmark_result') IS NOT NULL,
		       to_regclass('public.conbench_schema_migration') IS NOT NULL
	`).Scan(&schemaExists, &ledgerExists); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	if !schemaExists {
		if _, err := conn.Exec(ctx, SchemaSQL); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	} else if err := verifyAdoptable(ctx, conn.Conn(), ledgerExists); err != nil {
		return err
	}

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.conbench_schema_migration (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamp with time zone DEFAULT now() NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	for _, m := range migrations {
		applied, err := migrationApplied(ctx, conn.Conn(), m)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if m.apply != nil {
			if err := m.apply(ctx, conn.Conn()); err != nil {
				return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
			}
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO public.conbench_schema_migration (version, name) VALUES ($1, $2)`,
			m.version, m.name,
		); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// verifyAdoptable decides whether a pre-existing database may be adopted at
// the Go baseline. The Go ledger is the durable ownership marker. Before that
// ledger existed, alembic_version recorded the two exact cutover states that
// this binary knows how to advance. Unmarked and all other legacy databases
// are rejected before the database is modified.
func verifyAdoptable(ctx context.Context, conn *pgx.Conn, ledgerExists bool) error {
	if ledgerExists {
		return nil
	}
	var hasAlembic bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.alembic_version') IS NOT NULL`).Scan(&hasAlembic); err != nil {
		return fmt.Errorf("inspect legacy schema revision: %w", err)
	}
	if !hasAlembic {
		return errors.New("existing database has no recognized schema revision; restore a supported database backup before running this migrator")
	}
	rows, err := conn.Query(ctx, `SELECT version_num FROM public.alembic_version`)
	if err != nil {
		return fmt.Errorf("read legacy schema revision: %w", err)
	}
	revisions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read legacy schema revision: %w", err)
	}
	if len(revisions) == 1 {
		switch revisions[0] {
		case legacyBaselineRevision, legacySubmissionRevision:
			return nil
		}
	}
	return fmt.Errorf(
		"existing database has unsupported legacy revision %q; supported cutover revisions are %s",
		strings.Join(revisions, ", "), legacyBaselineRevision+", "+legacySubmissionRevision,
	)
}

func finishLegacySchemaHandoff(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `DROP TABLE IF EXISTS public.alembic_version`)
	return err
}

func migrationApplied(ctx context.Context, conn *pgx.Conn, m migration) (bool, error) {
	var name string
	err := conn.QueryRow(ctx,
		`SELECT name FROM public.conbench_schema_migration WHERE version = $1`,
		m.version,
	).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read migration %d: %w", m.version, err)
	}
	if name != m.name {
		return false, fmt.Errorf("migration %d is recorded as %q, expected %q", m.version, name, m.name)
	}
	return true, nil
}

func addSubmissionIdempotency(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, `
		ALTER TABLE public.benchmark_result
			ADD COLUMN IF NOT EXISTS submission_key text,
			ADD COLUMN IF NOT EXISTS submission_payload_sha256 text
	`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'public.benchmark_result'::regclass
				  AND conname = 'benchmark_result_submission_idempotency_check'
			) THEN
				ALTER TABLE public.benchmark_result
					ADD CONSTRAINT benchmark_result_submission_idempotency_check
					CHECK (
						(submission_key IS NULL AND submission_payload_sha256 IS NULL)
						OR (submission_key IS NOT NULL AND submission_payload_sha256 ~ '^[0-9a-f]{64}$')
					) NOT VALID;
			END IF;
		END
		$$
	`); err != nil {
		return err
	}
	// This migration intentionally runs outside a transaction so PostgreSQL can
	// build the unique index without blocking writes for the duration of the
	// table scan.
	if _, err := conn.Exec(ctx, `
		CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS benchmark_result_submission_key_index
		ON public.benchmark_result (submission_key)
		WHERE submission_key IS NOT NULL
	`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `
		ALTER TABLE public.benchmark_result
			VALIDATE CONSTRAINT benchmark_result_submission_idempotency_check
	`); err != nil {
		return err
	}
	var indexValid bool
	if err := conn.QueryRow(ctx, `
		SELECT indisvalid
		FROM pg_index
		WHERE indexrelid = 'public.benchmark_result_submission_key_index'::regclass
	`).Scan(&indexValid); err != nil {
		return err
	}
	if !indexValid {
		return errors.New("benchmark_result_submission_key_index exists but is invalid")
	}
	return nil
}
