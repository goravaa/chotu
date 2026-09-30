package state

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(filepath.Join(t.TempDir(), "chotu.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "chotu.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Reopening must run the migrations again without failing.
	db.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	for _, table := range []string{"machine", "setup_tasks"} {
		var name string

		err := reopened.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&name)

		if err != nil {
			t.Errorf("table %q: %v", table, err)
		}
	}
}

func TestMachineLifecycle(t *testing.T) {
	db := testDB(t)

	machine, err := GetMachine(db)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}

	if machine != nil {
		t.Fatalf("machine = %+v, want nil on a fresh database", machine)
	}

	if err := CreateMachine(db, "hash-1"); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	machine, err = GetMachine(db)
	if err != nil {
		t.Fatalf("GetMachine after create: %v", err)
	}

	if machine == nil {
		t.Fatal("machine is nil after CreateMachine")
	}

	if machine.State != StateFresh {
		t.Errorf("state = %s, want %s", machine.State, StateFresh)
	}

	if machine.ConfigHash != "hash-1" {
		t.Errorf("config hash = %s, want hash-1", machine.ConfigHash)
	}

	if machine.CreatedAt.IsZero() || machine.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", machine)
	}

	for _, want := range []MachineState{
		StateInitializing,
		StateReady,
		StateFailed,
	} {
		if err := UpdateMachineState(db, want, "hash-2"); err != nil {
			t.Fatalf("UpdateMachineState(%s): %v", want, err)
		}

		machine, err = GetMachine(db)
		if err != nil {
			t.Fatalf("GetMachine: %v", err)
		}

		if machine.State != want {
			t.Errorf("state = %s, want %s", machine.State, want)
		}

		if machine.ConfigHash != "hash-2" {
			t.Errorf("config hash = %s, want hash-2", machine.ConfigHash)
		}
	}
}

func TestSetTaskStateRecordsTimestamps(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	if err := store.SetTaskState("docker", "RUNNING", nil); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	var (
		state       string
		startedAt   sql.NullTime
		completedAt sql.NullTime
		taskErr     sql.NullString
	)

	err := db.QueryRow(`
		SELECT state, started_at, completed_at, error
		FROM setup_tasks
		WHERE name = 'docker'
	`).Scan(&state, &startedAt, &completedAt, &taskErr)

	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if state != "RUNNING" {
		t.Errorf("state = %s, want RUNNING", state)
	}

	if !startedAt.Valid {
		t.Error("started_at is NULL after a RUNNING write")
	}

	if completedAt.Valid {
		t.Error("completed_at is set before the task completed")
	}

	if taskErr.Valid {
		t.Errorf("error = %q, want NULL", taskErr.String)
	}

	if err := store.SetTaskState("docker", "COMPLETED", nil); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	err = db.QueryRow(`
		SELECT state, started_at, completed_at
		FROM setup_tasks
		WHERE name = 'docker'
	`).Scan(&state, &startedAt, &completedAt)

	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if state != "COMPLETED" {
		t.Errorf("state = %s, want COMPLETED", state)
	}

	if !completedAt.Valid {
		t.Error("completed_at is NULL after a COMPLETED write")
	}

	if !startedAt.Valid {
		t.Error("started_at was cleared by the COMPLETED write")
	}

	firstStart := startedAt.Time

	// A later run must not lose the timestamps or duplicate the row.
	if err := store.SetTaskState("docker", "RUNNING", nil); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	if err := store.SetTaskState("docker", "COMPLETED", nil); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM setup_tasks WHERE name = 'docker'",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}

	if count != 1 {
		t.Fatalf("rows = %d, want 1: a re-run must upsert, not insert", count)
	}

	err = db.QueryRow(
		"SELECT started_at FROM setup_tasks WHERE name = 'docker'",
	).Scan(&startedAt)

	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if startedAt.Time.Before(firstStart) {
		t.Error("started_at went backwards on re-run")
	}
}

func TestSetTaskStateStoresError(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	err := store.SetTaskState("gpu", "FAILED", errors.New("no nvidia-smi"))
	if err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	var state, taskErr string

	if err := db.QueryRow(`
		SELECT state, error FROM setup_tasks WHERE name = 'gpu'
	`).Scan(&state, &taskErr); err != nil {
		t.Fatalf("query: %v", err)
	}

	if state != "FAILED" {
		t.Errorf("state = %s, want FAILED", state)
	}

	if taskErr != "no nvidia-smi" {
		t.Errorf("error = %q, want %q", taskErr, "no nvidia-smi")
	}

	// A later successful run must clear the stored error.
	if err := store.SetTaskState("gpu", "COMPLETED", nil); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}

	var cleared sql.NullString
	if err := db.QueryRow(
		"SELECT error FROM setup_tasks WHERE name = 'gpu'",
	).Scan(&cleared); err != nil {
		t.Fatalf("query: %v", err)
	}

	if cleared.Valid {
		t.Errorf("error = %q, want NULL after a successful run", cleared.String)
	}
}

// TestSetTaskStateConcurrentWrites is a regression test. SQLite allows a
// single writer, so concurrent task state writes used to fail with
// SQLITE_BUSY and the runner discarded those errors, losing most rows.
func TestSetTaskStateConcurrentWrites(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)

	const writers = 40

	var wg sync.WaitGroup

	errs := make(chan error, writers)

	for i := range writers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name := fmt.Sprintf("task-%02d", i)

			for _, taskState := range []string{"RUNNING", "COMPLETED"} {
				if err := store.SetTaskState(name, taskState, nil); err != nil {
					errs <- fmt.Errorf("%s/%s: %w", name, taskState, err)

					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	rows, err := db.Query(
		"SELECT name, state FROM setup_tasks ORDER BY name",
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0

	for rows.Next() {
		var name, taskState string

		if err := rows.Scan(&name, &taskState); err != nil {
			t.Fatalf("scan: %v", err)
		}

		if taskState != "COMPLETED" {
			t.Errorf("%s = %s, want COMPLETED", name, taskState)
		}

		seen++
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if seen != writers {
		t.Fatalf("persisted %d / %d rows: concurrent writes were lost", seen, writers)
	}
}

func TestConcurrentMachineUpdates(t *testing.T) {
	db := testDB(t)

	if err := CreateMachine(db, "hash-1"); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}

	var wg sync.WaitGroup

	errs := make(chan error, 20)

	for i := range 20 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			state := MachineState(fmt.Sprintf("STATE-%d", i))
			if err := UpdateMachineState(db, state, "hash-1"); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	machine, err := GetMachine(db)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}

	if machine == nil {
		t.Fatal("machine disappeared during concurrent updates")
	}

	// Exactly one row must exist no matter how many writers raced.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM machine").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}

	if count != 1 {
		t.Fatalf("machine rows = %d, want 1", count)
	}
}
