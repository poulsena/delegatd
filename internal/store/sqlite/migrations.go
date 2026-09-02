package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const currentSchemaVersion = 1

func migrate(ctx context.Context, database *sql.DB) error {
	version, err := databaseSchemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if version < 0 || version > currentSchemaVersion {
		return errStoreUnsupported
	}
	if version == 0 {
		var objects int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return errors.New("state store has an unknown version-zero schema")
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer transaction.Rollback()
		statements := []string{
			`CREATE TABLE resources (
			  id TEXT PRIMARY KEY NOT NULL,
			  name TEXT NOT NULL UNIQUE,
			  kind TEXT NOT NULL,
			  connector_instance TEXT NOT NULL,
			  external_ref TEXT NOT NULL,
			  external_identity TEXT NOT NULL,
			  revision TEXT NOT NULL,
			  configuration_json BLOB NOT NULL,
			  policy_request_json BLOB NOT NULL,
			  onboarded_at TEXT NOT NULL,
			  updated_at TEXT NOT NULL,
			  UNIQUE (connector_instance, external_identity)
			) STRICT`,
			`CREATE TABLE tasks (
			  id TEXT PRIMARY KEY NOT NULL,
			  resource_id TEXT NOT NULL REFERENCES resources(id),
			  status TEXT NOT NULL,
			  resource_json BLOB NOT NULL,
			  input_json BLOB NOT NULL,
			  configuration_json BLOB NOT NULL,
			  policy_json BLOB NOT NULL,
			  created_at TEXT NOT NULL
			) STRICT`,
			`CREATE TABLE task_history (
			  task_id TEXT NOT NULL REFERENCES tasks(id),
			  sequence INTEGER NOT NULL CHECK (sequence > 0),
			  status TEXT NOT NULL,
			  occurred_at TEXT NOT NULL,
			  reason TEXT NOT NULL,
			  PRIMARY KEY (task_id, sequence)
			) STRICT`,
			`PRAGMA user_version = 1`,
		}
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if err := transaction.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func databaseSchemaVersion(ctx context.Context, database *sql.DB) (int, error) {
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func validateSchema(ctx context.Context, database *sql.DB) error {
	version, err := databaseSchemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if version != currentSchemaVersion {
		return errStoreUnsupported
	}
	expected := map[string][]columnDefinition{
		"resources": {
			{name: "id", typ: "TEXT", notNull: 1, primaryKey: 1},
			{name: "name", typ: "TEXT", notNull: 1},
			{name: "kind", typ: "TEXT", notNull: 1},
			{name: "connector_instance", typ: "TEXT", notNull: 1},
			{name: "external_ref", typ: "TEXT", notNull: 1},
			{name: "external_identity", typ: "TEXT", notNull: 1},
			{name: "revision", typ: "TEXT", notNull: 1},
			{name: "configuration_json", typ: "BLOB", notNull: 1},
			{name: "policy_request_json", typ: "BLOB", notNull: 1},
			{name: "onboarded_at", typ: "TEXT", notNull: 1},
			{name: "updated_at", typ: "TEXT", notNull: 1},
		},
		"tasks": {
			{name: "id", typ: "TEXT", notNull: 1, primaryKey: 1},
			{name: "resource_id", typ: "TEXT", notNull: 1},
			{name: "status", typ: "TEXT", notNull: 1},
			{name: "resource_json", typ: "BLOB", notNull: 1},
			{name: "input_json", typ: "BLOB", notNull: 1},
			{name: "configuration_json", typ: "BLOB", notNull: 1},
			{name: "policy_json", typ: "BLOB", notNull: 1},
			{name: "created_at", typ: "TEXT", notNull: 1},
		},
		"task_history": {
			{name: "task_id", typ: "TEXT", notNull: 1, primaryKey: 1},
			{name: "sequence", typ: "INTEGER", notNull: 1, primaryKey: 2},
			{name: "status", typ: "TEXT", notNull: 1},
			{name: "occurred_at", typ: "TEXT", notNull: 1},
			{name: "reason", typ: "TEXT", notNull: 1},
		},
	}
	rows, err := database.QueryContext(ctx, `SELECT name, type, sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	type tableDefinition struct {
		name       string
		objectType string
		definition string
	}
	var definitions []tableDefinition
	for rows.Next() {
		var definition tableDefinition
		if err := rows.Scan(&definition.name, &definition.objectType, &definition.definition); err != nil {
			rows.Close()
			return err
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	for _, definition := range definitions {
		if definition.objectType != "table" {
			return fmt.Errorf("unexpected state store object")
		}
		columns, ok := expected[definition.name]
		if !ok || !strings.Contains(strings.ToUpper(definition.definition), "STRICT") {
			return errStoreCorrupt
		}
		if err := validateTableColumns(ctx, database, definition.name, columns); err != nil {
			return err
		}
		seen[definition.name] = struct{}{}
	}
	if len(seen) != len(expected) {
		return errStoreCorrupt
	}
	return nil
}

type columnDefinition struct {
	name       string
	typ        string
	notNull    int
	primaryKey int
}

func validateTableColumns(ctx context.Context, database *sql.DB, table string, expected []columnDefinition) error {
	rows, err := database.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if index >= len(expected) || name != expected[index].name || !strings.EqualFold(typ, expected[index].typ) || notNull != expected[index].notNull || primaryKey != expected[index].primaryKey {
			return errStoreCorrupt
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index != len(expected) {
		return errStoreCorrupt
	}
	return nil
}
