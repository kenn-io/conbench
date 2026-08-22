package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 0x436f6e62656e6368

type migration struct {
	version int64
	name    string
	apply   func(context.Context, *pgx.Conn) error
}

var migrations = []migration{
	{version: 1, name: "go-schema-baseline"},
	{version: 2, name: "submission-idempotency", apply: addSubmissionIdempotency},
}

// MigrationCount reports the number of schema revisions built into this
// binary. It is primarily useful to operational status and integration tests.
func MigrationCount() int {
	return len(migrations)
}

// Migrate brings a database to the schema version embedded in this binary.
// A session advisory lock serializes migrators across deploy jobs. Existing
// databases created by the retired schema tool are adopted at the baseline;
// subsequent migrations are idempotent and recorded by version and name.
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

	var schemaExists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.benchmark_result') IS NOT NULL`).Scan(&schemaExists); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	if !schemaExists {
		if _, err := conn.Exec(ctx, SchemaSQL); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
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
