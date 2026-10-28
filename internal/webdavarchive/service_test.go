package webdavarchive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/yibaiba/hideck/internal/db"
	"gorm.io/gorm"
)

type memoryRemote struct {
	mu       sync.Mutex
	objects  map[string][]byte
	uploads  int
	failNext error
}

func (remote *memoryRemote) Probe(context.Context) error { return nil }

func (remote *memoryRemote) Put(_ context.Context, upload Upload) error {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.failNext != nil {
		err := remote.failNext
		remote.failNext = nil
		return err
	}
	remote.uploads++
	remote.objects[upload.RemotePath] = append([]byte(nil), upload.Data...)
	return nil
}

func (remote *memoryRemote) PutFile(_ context.Context, upload FileUpload) error {
	data, err := os.ReadFile(upload.LocalPath)
	if err != nil {
		return err
	}
	return remote.Put(context.Background(), Upload{RemotePath: upload.RemotePath, Data: data})
}

func TestServiceArchivesIncrementallyAndCleansOnlyArchivedData(t *testing.T) {
	database := newArchiveTestDB(t)
	recordingDir := t.TempDir()
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -60)
	sms := db.SMS{
		IMSI: "234150000000001", ICCID: "8944100000000000001", Peer: "+447700900123",
		Sender: "+447700900123", Recipient: "+447700900999", Content: "archived", Type: 1,
		Status: 0, Timestamp: old,
	}
	if err := database.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	contact := db.SMSContact{
		IMSI: sms.IMSI, ICCID: sms.ICCID, Peer: sms.Peer, LastSMSID: sms.ID,
		LastTimestamp: sms.Timestamp, LastContent: sms.Content, LastType: sms.Type, UnreadCount: 1,
	}
	if err := database.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	ended := old.Add(2 * time.Minute)
	call := db.VoiceCallRecord{
		CallID: "call-1", DeviceID: "wwan0", ICCID: sms.ICCID, Direction: "inbound",
		Peer: sms.Peer, Status: "completed", StartedAt: old, EndedAt: &ended,
		DurationSeconds: 120, RecordingName: "call_old.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	recordingPath := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(recordingPath, []byte("audio-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	remote := &memoryRemote{objects: make(map[string][]byte)}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: remote, recordingDir: recordingDir, now: now,
	})
	settings := Settings{IncludeSMS: true, IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	first, err := service.Run(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if first.Uploaded != 3 || first.Skipped != 0 {
		t.Fatalf("first summary = %+v", first)
	}
	if !archiveContains(remote, "sms/", `"iccid": "8944100000000000001"`) {
		t.Fatalf("SMS archive did not retain ICCID: %+v", remote.objects)
	}
	second, err := service.Run(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if second.Uploaded != 0 || second.Skipped != 3 {
		t.Fatalf("second summary = %+v", second)
	}
	if err := database.Model(&db.SMS{}).Where("id = ?", sms.ID).Update("status", 1).Error; err != nil {
		t.Fatal(err)
	}
	third, err := service.Run(context.Background(), settings)
	if err != nil || third.Uploaded != 1 || third.Skipped != 2 {
		t.Fatalf("changed-data summary = %+v, %v", third, err)
	}

	unarchived := db.SMS{
		IMSI: sms.IMSI, ICCID: sms.ICCID, Peer: sms.Peer, Sender: sms.Sender, Recipient: sms.Recipient,
		Content: "keep locally", Type: 1, Status: 0, Timestamp: old.Add(24 * time.Hour),
	}
	if err := database.Create(&unarchived).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if preview.SMSCount != 1 || preview.CallCount != 1 || preview.RecordingCount != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	cleaned, err := service.Cleanup(context.Background(), settings, preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.SMSCount != 1 || cleaned.CallCount != 1 || cleaned.RecordingCount != 1 {
		t.Fatalf("cleaned = %+v", cleaned)
	}
	assertArchiveCleanupState(t, database, unarchived.ID, recordingPath)
}

func TestServiceRetriesUploadBeforeWritingLedger(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	sms := db.SMS{IMSI: "imsi", ICCID: "iccid", Peer: "+1", Content: "retry", Type: 1, Timestamp: now}
	if err := database.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	remote := &memoryRemote{objects: make(map[string][]byte), failNext: errors.New("dav unavailable")}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: remote, recordingDir: t.TempDir(), now: now,
	})
	if _, err := service.Run(context.Background(), Settings{IncludeSMS: true}); err == nil {
		t.Fatal("failed upload reported success")
	}
	var ledgerCount int64
	if err := database.Model(&db.ArchiveObject{}).Count(&ledgerCount).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger count after failed upload = %d", ledgerCount)
	}
	result, err := service.Run(context.Background(), Settings{IncludeSMS: true})
	if err != nil || result.Uploaded != 1 {
		t.Fatalf("retry result = %+v, %v", result, err)
	}
}

func TestMissingRecordingPreventsCallCleanup(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	started := now.AddDate(0, 0, -60)
	ended := started.Add(time.Minute)
	call := db.VoiceCallRecord{
		CallID: "missing-recording", DeviceID: "wwan0", Direction: "inbound", Status: "completed",
		StartedAt: started, EndedAt: &ended, RecordingName: "missing.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	remote := &memoryRemote{objects: make(map[string][]byte)}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: remote, recordingDir: t.TempDir(), now: now,
	})
	settings := Settings{IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err == nil {
		t.Fatal("missing recording reported a successful archive run")
	}
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CallCount != 0 || preview.RecordingCount != 0 {
		t.Fatalf("missing recording became cleanup eligible: %+v", preview)
	}
}

func TestRecordingOnlyCleanupKeepsCallHistory(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ended := now.AddDate(0, 0, -60)
	call := db.VoiceCallRecord{
		CallID: "recording-only", DeviceID: "wwan0", Direction: "inbound", Status: "completed",
		StartedAt: ended.Add(-time.Minute), EndedAt: &ended, RecordingName: "recording-only.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	recordingDir := t.TempDir()
	recordingPath := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(recordingPath, []byte("archived audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: &memoryRemote{objects: make(map[string][]byte)},
		recordingDir: recordingDir, now: now,
	})
	settings := Settings{IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	preview, err := cleanupWithPreview(t, service, settings)
	if err != nil || preview.CallCount != 0 || preview.RecordingCount != 1 {
		t.Fatalf("recording-only cleanup = %+v, %v", preview, err)
	}
	var calls int64
	if err := database.Model(&db.VoiceCallRecord{}).Count(&calls).Error; err != nil || calls != 1 {
		t.Fatalf("call count = %d, %v", calls, err)
	}
	if _, err := os.Stat(recordingPath); !os.IsNotExist(err) {
		t.Fatalf("recording still exists: %v", err)
	}
}

func TestCleanupKeepsRecordingReferencedByCurrentCall(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	oldEnded := now.AddDate(0, 0, -60)
	currentEnded := now.AddDate(0, 0, -1)
	for index, ended := range []*time.Time{&oldEnded, &currentEnded} {
		call := db.VoiceCallRecord{
			CallID: fmt.Sprintf("shared-%d", index), DeviceID: "wwan0", Direction: "inbound",
			Status: "completed", StartedAt: ended.Add(-time.Minute), EndedAt: ended,
			RecordingName: "shared.mp3",
		}
		if err := database.Create(&call).Error; err != nil {
			t.Fatal(err)
		}
	}
	recordingDir := t.TempDir()
	recordingPath := filepath.Join(recordingDir, "shared.mp3")
	if err := os.WriteFile(recordingPath, []byte("shared audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: &memoryRemote{objects: make(map[string][]byte)},
		recordingDir: recordingDir, now: now,
	})
	settings := Settings{IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	preview, err := cleanupWithPreview(t, service, settings)
	if err != nil || preview.RecordingCount != 0 {
		t.Fatalf("shared recording cleanup = %+v, %v", preview, err)
	}
	if _, err := os.Stat(recordingPath); err != nil {
		t.Fatalf("current recording was removed: %v", err)
	}
}

func TestCleanupCanRetryDatabaseFailureAfterArchivedRecordingRemoval(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	started := now.AddDate(0, 0, -60)
	ended := started.Add(time.Minute)
	call := db.VoiceCallRecord{
		CallID: "cleanup-retry", DeviceID: "wwan0", Direction: "inbound", Status: "completed",
		StartedAt: started, EndedAt: &ended, RecordingName: "cleanup-retry.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	recordingDir := t.TempDir()
	recordingPath := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(recordingPath, []byte("archived audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := &memoryRemote{objects: make(map[string][]byte)}
	store := &failingDeleteStore{Store: NewDatabaseStore(database)}
	service, err := NewService(ServiceOptions{
		Store: store, Remote: remote, RecordingDir: recordingDir, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	store.failNext = true
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cleanup(context.Background(), settings, preview.Token); err == nil {
		t.Fatal("database failure reported successful cleanup")
	}
	if _, err := os.Stat(recordingPath); err != nil {
		t.Fatalf("recording was not restored after database failure: %v", err)
	}
	if preview, err := cleanupWithPreview(t, service, settings); err != nil || preview.CallCount != 1 {
		t.Fatalf("retry cleanup = %+v, %v", preview, err)
	}
	if _, err := os.Stat(recordingPath); !os.IsNotExist(err) {
		t.Fatalf("recording still exists after successful retry: %v", err)
	}
}

func TestChangedRecordingIsNotCommittedToArchiveLedger(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	call := db.VoiceCallRecord{
		CallID: "changing-recording", DeviceID: "wwan0", Direction: "inbound", Status: "completed",
		StartedAt: now.Add(-2 * time.Minute), EndedAt: &ended, RecordingName: "changing.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	recordingDir := t.TempDir()
	path := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(path, []byte("first version"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := &mutatingFileRemote{memoryRemote: memoryRemote{objects: make(map[string][]byte)}}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: remote, recordingDir: recordingDir, now: now,
	})
	settings := Settings{IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err == nil {
		t.Fatal("changed recording reported successful archive")
	}
	_, found, err := NewDatabaseStore(database).FindObject(context.Background(), KindRecording, call.RecordingName)
	if err != nil || found {
		t.Fatalf("recording ledger found=%v err=%v", found, err)
	}
}

func TestCleanupRefusesSMSChangedAfterPreview(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	sms := db.SMS{Content: "unread", Status: 0, Timestamp: now.AddDate(0, 0, -60)}
	if err := database.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	store := &mutatingDeleteStore{Store: NewDatabaseStore(database)}
	service, err := NewService(ServiceOptions{
		Store: store, Remote: &memoryRemote{objects: make(map[string][]byte)},
		RecordingDir: t.TempDir(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{IncludeSMS: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	store.before = func() {
		if err := database.Model(&db.SMS{}).Where("id = ?", sms.ID).Update("status", 1).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Cleanup(context.Background(), settings, preview.Token); err == nil {
		t.Fatal("changed SMS was deleted")
	}
	var count int64
	if err := database.Model(&db.SMS{}).Where("id = ?", sms.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("SMS count = %d, %v", count, err)
	}
}

func TestCleanupRejectsStalePreviewToken(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	sms := db.SMS{Content: "unread", Status: 0, Timestamp: now.AddDate(0, 0, -60)}
	if err := database.Create(&sms).Error; err != nil {
		t.Fatal(err)
	}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: &memoryRemote{objects: make(map[string][]byte)},
		recordingDir: t.TempDir(), now: now,
	})
	settings := Settings{IncludeSMS: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&db.SMS{}).Where("id = ?", sms.ID).Update("status", 1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cleanup(context.Background(), settings, preview.Token); !errors.Is(err, ErrCleanupPreviewChanged) {
		t.Fatalf("stale preview error = %v", err)
	}
}

func TestCleanupRefusesCallChangedAfterPreview(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ended := now.AddDate(0, 0, -60)
	call := db.VoiceCallRecord{
		CallID: "changed-call", DeviceID: "wwan0", Direction: "inbound",
		Status: "completed", StartedAt: ended.Add(-time.Minute), EndedAt: &ended,
		RecordingName: "changed-call.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	store := &mutatingDeleteStore{Store: NewDatabaseStore(database)}
	recordingDir := t.TempDir()
	recordingPath := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(recordingPath, []byte("call audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Remote: &memoryRemote{objects: make(map[string][]byte)},
		RecordingDir: recordingDir, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	store.before = func() {
		if err := database.Model(&db.VoiceCallRecord{}).Where("id = ?", call.ID).
			Update("end_reason", "changed").Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Cleanup(context.Background(), settings, preview.Token); err == nil {
		t.Fatal("changed call was deleted")
	}
	var count int64
	if err := database.Model(&db.VoiceCallRecord{}).Where("id = ?", call.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("call count = %d, %v", count, err)
	}
	if _, err := os.Stat(recordingPath); err != nil {
		t.Fatalf("recording was not restored: %v", err)
	}
}

func TestCleanupRecoversInterruptedRecordingStaging(t *testing.T) {
	database := newArchiveTestDB(t)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ended := now.AddDate(0, 0, -60)
	call := db.VoiceCallRecord{
		CallID: "staged-call", DeviceID: "wwan0", Direction: "inbound", Status: "completed",
		StartedAt: ended.Add(-time.Minute), EndedAt: &ended, RecordingName: "staged-call.mp3",
	}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}
	recordingDir := t.TempDir()
	original := filepath.Join(recordingDir, call.RecordingName)
	if err := os.WriteFile(original, []byte("call audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: &memoryRemote{objects: make(map[string][]byte)},
		recordingDir: recordingDir, now: now,
	})
	settings := Settings{IncludeCalls: true, IncludeRecordings: true, RetentionDays: 30}
	if _, err := service.Run(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	staged := original + cleanupStagingMarker + "0011223344556677"
	if err := os.Rename(original, staged); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanupWithPreview(t, service, settings); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{original, staged} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup residue %q: %v", path, err)
		}
	}
}

func TestCleanupDoesNotRemoveUnknownStagingFile(t *testing.T) {
	database := newArchiveTestDB(t)
	recordingDir := t.TempDir()
	staged := filepath.Join(recordingDir, "manual.mp3"+cleanupStagingMarker+"0011223344556677")
	if err := os.WriteFile(staged, []byte("not managed by archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newArchiveTestService(t, archiveTestServiceOptions{
		database: database, remote: &memoryRemote{objects: make(map[string][]byte)},
		recordingDir: recordingDir, now: time.Now(),
	})
	if _, err := service.PreviewCleanup(context.Background(), Settings{IncludeRecordings: true, RetentionDays: 30}); err == nil {
		t.Fatal("unknown staging file was accepted")
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("unknown staging file was removed: %v", err)
	}
}

type mutatingFileRemote struct{ memoryRemote }

func cleanupWithPreview(t *testing.T, service *Service, settings Settings) (CleanupPreview, error) {
	t.Helper()
	preview, err := service.PreviewCleanup(context.Background(), settings)
	if err != nil {
		return CleanupPreview{}, err
	}
	return service.Cleanup(context.Background(), settings, preview.Token)
}

func (remote *mutatingFileRemote) PutFile(_ context.Context, upload FileUpload) error {
	data, err := os.ReadFile(upload.LocalPath)
	if err != nil {
		return err
	}
	remote.objects[upload.RemotePath] = data
	return os.WriteFile(upload.LocalPath, []byte("second version"), 0o600)
}

type failingDeleteStore struct {
	Store
	failNext bool
}

type mutatingDeleteStore struct {
	Store
	before func()
}

func (store *mutatingDeleteStore) DeleteRecords(ctx context.Context, request DeleteRequest) error {
	if store.before != nil {
		before := store.before
		store.before = nil
		before()
	}
	return store.Store.DeleteRecords(ctx, request)
}

func (store *failingDeleteStore) DeleteRecords(ctx context.Context, request DeleteRequest) error {
	if store.failNext {
		store.failNext = false
		return errors.New("database temporarily unavailable")
	}
	return store.Store.DeleteRecords(ctx, request)
}

func newArchiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "archive.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&db.SMS{}, &db.SMSContact{}, &db.VoiceCallRecord{}, &db.ArchiveObject{}); err != nil {
		t.Fatal(err)
	}
	return database
}

type archiveTestServiceOptions struct {
	database     *gorm.DB
	remote       Remote
	recordingDir string
	now          time.Time
}

func newArchiveTestService(t *testing.T, options archiveTestServiceOptions) *Service {
	t.Helper()
	service, err := NewService(ServiceOptions{
		Store: NewDatabaseStore(options.database), Remote: options.remote, RecordingDir: options.recordingDir,
		Now: func() time.Time { return options.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func archiveContains(remote *memoryRemote, prefix, value string) bool {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for path, data := range remote.objects {
		if strings.HasPrefix(path, prefix) && strings.Contains(string(data), value) {
			return true
		}
	}
	return false
}

func assertArchiveCleanupState(t *testing.T, database *gorm.DB, remainingSMS uint, recordingPath string) {
	t.Helper()
	var smsRows []db.SMS
	if err := database.Find(&smsRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(smsRows) != 1 || smsRows[0].ID != remainingSMS {
		t.Fatalf("remaining SMS = %+v", smsRows)
	}
	var contact db.SMSContact
	if err := database.First(&contact).Error; err != nil || contact.LastSMSID != remainingSMS {
		t.Fatalf("rebuilt contact = %+v, %v", contact, err)
	}
	var calls int64
	if err := database.Model(&db.VoiceCallRecord{}).Count(&calls).Error; err != nil || calls != 0 {
		t.Fatalf("remaining calls = %d, %v", calls, err)
	}
	if _, err := os.Stat(recordingPath); !os.IsNotExist(err) {
		t.Fatalf("recording still exists: %v", err)
	}
}
