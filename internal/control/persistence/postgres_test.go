package persistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMigrateAppliesMissingMigrationsInOrderAndIsIdempotent(t *testing.T) {
	migrations := []Migration{
		{Name: "0001_first.sql", Up: "first"},
		{Name: "0002_second.sql", Up: "second"},
		{Name: "0003_third.sql", Up: "third"},
	}
	store := &recordingMigrationStore{applied: []string{"0001_first.sql"}}

	if err := Migrate(context.Background(), store, migrations); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0002_second.sql", "0003_third.sql"}; !reflect.DeepEqual(store.executed, want) {
		t.Fatalf("applied migrations = %v, want %v", store.executed, want)
	}
	if err := Migrate(context.Background(), store, migrations); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0002_second.sql", "0003_third.sql"}; !reflect.DeepEqual(store.executed, want) {
		t.Fatalf("second run changed applied migrations: %v", store.executed)
	}
	if store.lockCalls != 2 {
		t.Fatalf("lock calls = %d, want 2", store.lockCalls)
	}
}

func TestMigrateFailsClosedOnDivergentHistory(t *testing.T) {
	store := &recordingMigrationStore{applied: []string{"0002_second.sql"}}
	err := Migrate(context.Background(), store, []Migration{
		{Name: "0001_first.sql", Up: "first"},
		{Name: "0002_second.sql", Up: "second"},
	})
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("error = %v, want ErrSchemaDrift", err)
	}
	if len(store.executed) != 0 {
		t.Fatalf("executed migrations after drift: %v", store.executed)
	}
}

func TestMigrateRejectsUnorderedInputBeforeLocking(t *testing.T) {
	store := &recordingMigrationStore{}
	err := Migrate(context.Background(), store, []Migration{
		{Name: "0002_second.sql", Up: "second"},
		{Name: "0001_first.sql", Up: "first"},
	})
	if err == nil {
		t.Fatal("expected unordered migrations to fail")
	}
	if store.lockCalls != 0 {
		t.Fatalf("lock acquired for invalid migrations %d times", store.lockCalls)
	}
}

type recordingMigrationStore struct {
	applied   []string
	executed  []string
	lockCalls int
}

func (store *recordingMigrationStore) WithMigrationLock(ctx context.Context, apply func(MigrationSession) error) error {
	store.lockCalls++
	return apply(store)
}

func (store *recordingMigrationStore) AppliedMigrations(context.Context) ([]string, error) {
	return append([]string(nil), store.applied...), nil
}

func (store *recordingMigrationStore) ApplyMigration(_ context.Context, migration Migration) error {
	store.executed = append(store.executed, migration.Name)
	store.applied = append(store.applied, migration.Name)
	return nil
}
