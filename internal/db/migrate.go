package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	legacyBaselineRevision   = "9d5f3c1a7b2e"
	legacySubmissionRevision = "a6b7c8d9e0f1"
	migrationTableName       = "schema_migrations"
	migrationHandoffLockID   = int64(0x636f6e62656e6368)
)

var errSubmissionSchemaIncomplete = errors.New("submission idempotency schema is incomplete")

//go:embed migrations/*.sql
var migrationFiles embed.FS

type legacyHandoff int

const (
	noLegacyHandoff legacyHandoff = iota
	baselineLegacyHandoff
	submissionLegacyHandoff
)

// Migrate advances the database through the numbered SQL migrations embedded
// in this binary. golang-migrate owns ordering, its advisory lock, and the
// version/dirty-state ledger. Go only recognizes the two exact schema markers
// needed to hand existing pre-Go databases to that migration history.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (returnErr error) {
	lockConn, err := acquireMigrationLock(ctx, pool)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseMigrationLock(lockConn))
	}()

	handoff, err := inspectLegacyHandoff(ctx, lockConn.Conn())
	if err != nil {
		return err
	}

	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	sqlDB, err := sql.Open("pgx", pool.Config().ConnString())
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("ping migration database: %w", err)
	}

	databaseDriver, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{
		MigrationsTable:       migrationTableName,
		MultiStatementEnabled: true,
	})
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("open migration driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "pgx5", databaseDriver)
	if err != nil {
		_ = databaseDriver.Close()
		_ = sourceDriver.Close()
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		returnErr = errors.Join(returnErr, sourceErr, databaseErr)
	}()

	latest, err := latestMigrationVersion()
	if err != nil {
		return err
	}

	if handoff != noLegacyHandoff {
		if err := prepareLegacyHandoff(ctx, lockConn.Conn(), databaseDriver, migrator, handoff, latest); err != nil {
			return err
		}
	}

	version, dirty, err := databaseDriver.Version()
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database is in a dirty migration state at version %d", version)
	}
	if version > latest {
		return fmt.Errorf(
			"database schema version %d is newer than this binary (expects %d); upgrade Conbench",
			version,
			latest,
		)
	}

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	version, dirty, err = databaseDriver.Version()
	if err != nil {
		return fmt.Errorf("read migration version after update: %w", err)
	}
	if dirty || version != latest {
		return fmt.Errorf("database ended at migration version %d (dirty=%t), expected %d", version, dirty, latest)
	}
	if err := verifyCurrentSchema(ctx, lockConn.Conn()); err != nil {
		return fmt.Errorf("verify migrated schema: %w", err)
	}
	if handoff != noLegacyHandoff {
		if _, err := lockConn.Exec(ctx, `DROP TABLE public.alembic_version`); err != nil {
			return fmt.Errorf("finish legacy schema handoff: %w", err)
		}
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock connection: %w", err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var acquired bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrationHandoffLockID).Scan(&acquired); err != nil {
			conn.Release()
			return nil, fmt.Errorf("acquire migration lock: %w", err)
		}
		if acquired {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			conn.Release()
			return nil, fmt.Errorf("acquire migration lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func releaseMigrationLock(conn *pgxpool.Conn) error {
	defer conn.Release()
	if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationHandoffLockID); err != nil {
		return fmt.Errorf("release migration lock: %w", err)
	}
	return nil
}

func inspectLegacyHandoff(ctx context.Context, conn *pgx.Conn) (legacyHandoff, error) {
	var schemaExists, ledgerExists, legacyLedgerExists bool
	if err := conn.QueryRow(ctx, `
		SELECT to_regclass('public.benchmark_result') IS NOT NULL,
		       to_regclass('public.schema_migrations') IS NOT NULL,
		       to_regclass('public.alembic_version') IS NOT NULL
	`).Scan(&schemaExists, &ledgerExists, &legacyLedgerExists); err != nil {
		return noLegacyHandoff, fmt.Errorf("inspect schema ownership: %w", err)
	}

	if ledgerExists {
		if !schemaExists {
			return noLegacyHandoff, errors.New("migration ledger exists without the Conbench schema")
		}
		if !legacyLedgerExists {
			return noLegacyHandoff, nil
		}
	}
	if !schemaExists {
		if legacyLedgerExists {
			return noLegacyHandoff, errors.New("legacy schema revision exists without the Conbench schema")
		}
		return noLegacyHandoff, nil
	}
	if !legacyLedgerExists {
		return noLegacyHandoff, errors.New("existing database has no recognized schema revision; restore a supported database backup before running this migrator")
	}

	rows, err := conn.Query(ctx, `SELECT version_num FROM public.alembic_version`)
	if err != nil {
		return noLegacyHandoff, fmt.Errorf("read legacy schema revision: %w", err)
	}
	revisions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return noLegacyHandoff, fmt.Errorf("read legacy schema revision: %w", err)
	}
	if len(revisions) == 1 {
		switch revisions[0] {
		case legacyBaselineRevision:
			return baselineLegacyHandoff, nil
		case legacySubmissionRevision:
			return submissionLegacyHandoff, nil
		}
	}
	return noLegacyHandoff, fmt.Errorf(
		"existing database has unsupported legacy revision %q; supported cutover revisions are %s",
		strings.Join(revisions, ", "),
		legacyBaselineRevision+", "+legacySubmissionRevision,
	)
}

func prepareLegacyHandoff(
	ctx context.Context,
	conn *pgx.Conn,
	driver migratedb.Driver,
	migrator *migrate.Migrate,
	handoff legacyHandoff,
	latest int,
) error {
	version, dirty, err := driver.Version()
	if err != nil {
		return fmt.Errorf("read legacy handoff version: %w", err)
	}
	if version == migratedb.NilVersion {
		if err := seedLegacyVersion(driver, handoff); err != nil {
			return err
		}
		version = 1
		if handoff == submissionLegacyHandoff {
			version = 2
		}
	}
	if version > latest {
		return fmt.Errorf("legacy handoff migration version %d is newer than this binary (expects %d)", version, latest)
	}
	if dirty {
		if version != 2 {
			return fmt.Errorf("legacy handoff has unsupported dirty migration version %d", version)
		}
		if err := migrator.Force(2); err != nil {
			return fmt.Errorf("reset interrupted legacy handoff: %w", err)
		}
		if err := migrator.Steps(-1); err != nil {
			return fmt.Errorf("clean interrupted legacy handoff: %w", err)
		}
		return nil
	}

	switch handoff {
	case baselineLegacyHandoff:
		if version < 1 {
			return fmt.Errorf("legacy baseline handoff has invalid migration version %d", version)
		}
	case submissionLegacyHandoff:
		switch version {
		case 1:
			return nil
		case 2:
			if err := verifyCurrentSchema(ctx, conn); err == nil {
				return nil
			} else if !errors.Is(err, errSubmissionSchemaIncomplete) {
				return fmt.Errorf("inspect legacy submission schema: %w", err)
			}
			if err := migrator.Steps(-1); err != nil {
				return fmt.Errorf("normalize legacy submission schema: %w", err)
			}
		default:
			return fmt.Errorf("legacy submission handoff has invalid migration version %d", version)
		}
	}
	return nil
}

func seedLegacyVersion(driver migratedb.Driver, handoff legacyHandoff) error {
	version := 1
	if handoff == submissionLegacyHandoff {
		version = 2
	}
	if err := driver.SetVersion(version, false); err != nil {
		return fmt.Errorf("seed legacy migration version %d: %w", version, err)
	}
	return nil
}

func latestMigrationVersion() (int, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil {
		return migratedb.NilVersion, fmt.Errorf("list embedded migrations: %w", err)
	}
	latest := migratedb.NilVersion
	for _, file := range files {
		name := strings.TrimSuffix(path.Base(file), ".up.sql")
		versionText, _, found := strings.Cut(name, "_")
		if !found {
			return migratedb.NilVersion, fmt.Errorf("parse migration version from %q", file)
		}
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return migratedb.NilVersion, fmt.Errorf("parse migration version from %q: %w", file, err)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == migratedb.NilVersion {
		return migratedb.NilVersion, errors.New("no embedded migrations found")
	}
	return latest, nil
}

func verifyCurrentSchema(ctx context.Context, conn *pgx.Conn) error {
	const submissionConstraint = "CHECK ((((submission_key IS NULL) AND (submission_payload_sha256 IS NULL)) OR ((submission_key IS NOT NULL) AND (submission_payload_sha256 IS NOT NULL) AND (submission_payload_sha256 ~ '^[0-9a-f]{64}$'::text))))"
	var keyColumn, hashColumn, constraintValid, indexValid bool
	if err := conn.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM pg_attribute
				WHERE attrelid = 'public.benchmark_result'::regclass
				  AND attname = 'submission_key'
				  AND NOT attisdropped
			),
			EXISTS (
				SELECT 1 FROM pg_attribute
				WHERE attrelid = 'public.benchmark_result'::regclass
				  AND attname = 'submission_payload_sha256'
				  AND NOT attisdropped
			),
			EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'public.benchmark_result'::regclass
				  AND conname = 'benchmark_result_submission_idempotency_check'
				  AND convalidated
				  AND pg_get_constraintdef(oid) = $1
			),
			EXISTS (
				SELECT 1
				FROM pg_index AS i
				JOIN pg_class AS idx ON idx.oid = i.indexrelid
				JOIN pg_am AS am ON am.oid = idx.relam
				LEFT JOIN pg_attribute AS a
				  ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
				WHERE i.indexrelid = to_regclass('public.benchmark_result_submission_key_index')
				  AND i.indrelid = 'public.benchmark_result'::regclass
				  AND i.indisunique
				  AND i.indisvalid
				  AND i.indnkeyatts = 1
				  AND i.indnatts = 1
				  AND am.amname = 'btree'
				  AND a.attname = 'submission_key'
				  AND pg_get_expr(i.indpred, i.indrelid) = '(submission_key IS NOT NULL)'
			)
	`, submissionConstraint).Scan(&keyColumn, &hashColumn, &constraintValid, &indexValid); err != nil {
		return err
	}
	if !keyColumn || !hashColumn || !constraintValid || !indexValid {
		return fmt.Errorf(
			"%w (key column=%t, hash column=%t, constraint=%t, index=%t)",
			errSubmissionSchemaIncomplete,
			keyColumn,
			hashColumn,
			constraintValid,
			indexValid,
		)
	}
	return nil
}
