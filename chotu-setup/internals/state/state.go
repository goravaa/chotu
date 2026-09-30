package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)


type MachineState string

const (
	StateFresh        MachineState = "FRESH"
	StateInitializing MachineState = "INITIALIZING"
	StateReady        MachineState = "READY"
	StateFailed       MachineState = "FAILED"
)

type Machine struct {
	State      MachineState
	ConfigHash string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS machine (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		state TEXT NOT NULL,
		config_hash TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS setup_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		error TEXT,
		updated_at DATETIME NOT NULL
	);
	`

	_, err := db.Exec(schema)
	return err
}



func GetMachine(db *sql.DB) (*Machine, error) {
	var machine Machine

	err := db.QueryRow(`
		SELECT state, config_hash, created_at, updated_at
		FROM machine
		WHERE id = 1
	`).Scan(
		&machine.State,
		&machine.ConfigHash,
		&machine.CreatedAt,
		&machine.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("get machine state: %w", err)
	}

	return &machine, nil
}

func CreateMachine(db *sql.DB, configHash string) error {
	now := time.Now().UTC()

	_, err := db.Exec(`
		INSERT INTO machine (
			id, state, config_hash, created_at, updated_at
		)
		VALUES (1, ?, ?, ?, ?)
	`,
		StateFresh,
		configHash,
		now,
		now,
	)

	if err != nil {
		return fmt.Errorf("create machine state: %w", err)
	}

	return nil
}

func UpdateMachineState(
	db *sql.DB,
	state MachineState,
	configHash string,
) error {
	_, err := db.Exec(`
		UPDATE machine
		SET state = ?,
		    config_hash = ?,
		    updated_at = ?
		WHERE id = 1
	`,
		state,
		configHash,
		time.Now().UTC(),
	)

	if err != nil {
		return fmt.Errorf("update machine state: %w", err)
	}

	return nil
}

func (s *Store) SetTaskState(
	name string,
	taskState string,
	taskErr error,
) error {
	now := time.Now().UTC()

	var errMessage *string
	if taskErr != nil {
		msg := taskErr.Error()
		errMessage = &msg
	}

	_, err := s.db.Exec(`
		INSERT INTO setup_tasks (
			name,
			state,
			started_at,
			completed_at,
			error,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			state = excluded.state,
			started_at = CASE
				WHEN excluded.state = 'RUNNING'
				THEN excluded.updated_at
				ELSE setup_tasks.started_at
			END,
			completed_at = CASE
				WHEN excluded.state = 'COMPLETED'
				THEN excluded.updated_at
				ELSE setup_tasks.completed_at
			END,
			error = excluded.error,
			updated_at = excluded.updated_at
	`,
		name,
		taskState,
		nil,
		nil,
		errMessage,
		now,
	)

	return err
}