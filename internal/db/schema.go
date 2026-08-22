package db

import _ "embed"

// SchemaSQL is the canonical Postgres schema for a new database. It is embedded
// so the server, migration command, and tests use the same reviewed definition.
//
//go:embed schema.sql
var SchemaSQL string
