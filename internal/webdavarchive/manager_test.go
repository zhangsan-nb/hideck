package webdavarchive

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yibaiba/hideck/internal/db"
	"gorm.io/gorm"
)

func TestManagerStartArchivesImmediatelyAndAcceptsHotConfig(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	if err := database.Create(&db.SMS{Content: "scheduled", Timestamp: now}).Error; err != nil {
		t.Fatal(err)
	}
	remote := &memoryRemote{objects: make(map[string][]byte)}
	manager := newArchiveTestManager(t, database, remote, RuntimeConfig{
		Enabled: true, Endpoint: "https://dav.example.test", BasePath: "hideck",
		Interval: time.Hour, RetentionDays: 30, IncludeSMS: true,
	}, now)
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx)
	})

	waitFor(t, time.Second, func() bool { return manager.Status().LastCompletedAt != nil })
	if remote.uploads != 1 {
		t.Fatalf("uploads = %d, want 1", remote.uploads)
	}
	updated := manager.Config()
	updated.Enabled = false
	updated.RetentionDays = 45
	if err := manager.Configure(updated); err != nil {
		t.Fatal(err)
	}
	if manager.Config().RetentionDays != 45 {
		t.Fatal("hot configuration was not applied")
	}
}

func TestManagerRejectsConcurrentRun(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	if err := database.Create(&db.SMS{Content: "blocking", Timestamp: now}).Error; err != nil {
		t.Fatal(err)
	}
	remote := &blockingRemote{started: make(chan struct{}), release: make(chan struct{})}
	manager := newArchiveTestManager(t, database, remote, RuntimeConfig{
		Endpoint: "https://dav.example.test", Interval: time.Hour,
		RetentionDays: 30, IncludeSMS: true,
	}, now)
	finished := make(chan error, 1)
	go func() {
		_, err := manager.RunNow(context.Background())
		finished <- err
	}()
	<-remote.started
	if _, err := manager.RunNow(context.Background()); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("concurrent RunNow error = %v", err)
	}
	close(remote.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestCleanupDoesNotReplaceLastArchiveStatus(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	if err := database.Create(&db.SMS{Content: "status", Timestamp: now}).Error; err != nil {
		t.Fatal(err)
	}
	remote := &memoryRemote{objects: make(map[string][]byte)}
	manager := newArchiveTestManager(t, database, remote, RuntimeConfig{
		Endpoint: "https://dav.example.test", Interval: time.Hour,
		RetentionDays: 30, IncludeSMS: true,
	}, now)
	if _, err := manager.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := manager.Status()
	preview, err := manager.PreviewCleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cleanup(context.Background(), preview.Token); err != nil {
		t.Fatal(err)
	}
	after := manager.Status()
	if after.Running || after.LastSummary != before.LastSummary ||
		after.LastStartedAt != before.LastStartedAt || after.LastCompletedAt != before.LastCompletedAt {
		t.Fatalf("cleanup changed archive status: before=%+v after=%+v", before, after)
	}
}

type blockingRemote struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (remote *blockingRemote) Probe(context.Context) error { return nil }

func (remote *blockingRemote) Put(context.Context, Upload) error {
	remote.once.Do(func() { close(remote.started) })
	<-remote.release
	return nil
}

func (remote *blockingRemote) PutFile(context.Context, FileUpload) error { return nil }

func newArchiveTestManager(
	t *testing.T,
	database *gorm.DB,
	remote ProbeRemote,
	config RuntimeConfig,
	now time.Time,
) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerOptions{
		Store: NewDatabaseStore(database), RecordingDir: filepath.Join(t.TempDir(), "recordings"),
		Config: config, NewRemote: func(RuntimeConfig) (ProbeRemote, error) { return remote, nil },
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}
