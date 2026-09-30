package state

import (
	"database/sql"
	"fmt"
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

const (
	taskStateRunning   = "RUNNING"
	taskStateCompleted = "COMPLETED"
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

	startedAt := any(nil)
	if taskState == taskStateRunning {
		startedAt = now
	}

	completedAt := any(nil)
	if taskState == taskStateCompleted {
		completedAt = now
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
		startedAt,
		completedAt,
		errMessage,
		now,
	)

	return err
}
