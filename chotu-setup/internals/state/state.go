package state

import (
	"database/sql"
	"fmt"
	"time"
)

type MachineState string

const (
	StateFresh        MachineState = "FRESH"
	StateInitializing MachineState = "INITIALIZING"
	StateReady        MachineState = "READY"
	StateFailed       MachineState = "FAILED"
)

type TaskState string

const (
	TaskPending   TaskState = "PENDING"
	TaskRunning   TaskState = "RUNNING"
	TaskCompleted TaskState = "COMPLETED"
	TaskFailed    TaskState = "FAILED"
)

type Machine struct {
	State      MachineState
	ConfigHash string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type SetupTask struct {
	Name        string
	State       TaskState
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       *string
	UpdatedAt   time.Time
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
