package webdavarchive

import (
	"context"
	"fmt"

	"github.com/yibaiba/hideck/internal/db"
	"gorm.io/gorm"
)

type DatabaseStore struct{ database *gorm.DB }

func NewDatabaseStore(database *gorm.DB) *DatabaseStore {
	return &DatabaseStore{database: database}
}

func (store *DatabaseStore) ListSMS(ctx context.Context) ([]SMSRecord, error) {
	rows, err := db.ListArchiveSMS(ctx, store.database)
	if err != nil {
		return nil, err
	}
	result := make([]SMSRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, smsRecordFromDB(row))
	}
	return result, nil
}

func (store *DatabaseStore) ListCalls(ctx context.Context) ([]CallRecord, error) {
	rows, err := db.ListArchiveCalls(ctx, store.database)
	if err != nil {
		return nil, err
	}
	result := make([]CallRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, callRecordFromDB(row))
	}
	return result, nil
}

func (store *DatabaseStore) FindObject(ctx context.Context, kind, localKey string) (Object, bool, error) {
	row, found, err := db.FindArchiveObject(ctx, store.database, kind, localKey)
	return Object{
		Kind: row.Kind, LocalKey: row.LocalKey, Checksum: row.Checksum, RemotePath: row.RemotePath,
		SizeBytes: row.SizeBytes, SourceTime: row.SourceTime, ArchivedAt: row.ArchivedAt,
	}, found, err
}

func (store *DatabaseStore) UpsertObject(ctx context.Context, object Object) error {
	return db.UpsertArchiveObject(ctx, store.database, db.ArchiveObject{
		Kind: object.Kind, LocalKey: object.LocalKey, Checksum: object.Checksum,
		RemotePath: object.RemotePath, SizeBytes: object.SizeBytes,
		SourceTime: object.SourceTime, ArchivedAt: object.ArchivedAt,
	})
}

func (store *DatabaseStore) DeleteRecords(ctx context.Context, request DeleteRequest) error {
	return store.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateSMSDeleteChecksums(tx, request.SMSIDs, request.SMSChecksums); err != nil {
			return err
		}
		if err := validateCallDeleteChecksums(tx, request.CallIDs, request.CallChecksums); err != nil {
			return err
		}
		return db.DeleteArchivedRecordsTx(tx, db.ArchiveDeleteRequest{
			SMSIDs: request.SMSIDs, CallIDs: request.CallIDs,
		})
	})
}

func smsRecordFromDB(row db.SMS) SMSRecord {
	return SMSRecord{
		ID: row.ID, IMSI: row.IMSI, ICCID: row.ICCID, Peer: row.Peer, LocalPhone: row.LocalPhone,
		Sender: row.Sender, Recipient: row.Recipient, Content: row.Content, Type: row.Type,
		Status: row.Status, Timestamp: row.Timestamp, Incomplete: row.Incomplete,
	}
}

func callRecordFromDB(row db.VoiceCallRecord) CallRecord {
	return CallRecord{
		ID: row.ID, CallID: row.CallID, DeviceID: row.DeviceID, ICCID: row.ICCID,
		Direction: row.Direction, Peer: row.Peer, Status: row.Status,
		StartedAt: row.StartedAt, AnsweredAt: row.AnsweredAt, EndedAt: row.EndedAt,
		DurationSeconds: row.DurationSeconds, EndReason: row.EndReason, Codec: row.Codec,
		RecordingName: row.RecordingName, RecordingError: row.RecordingError,
	}
}

func validateSMSDeleteChecksums(tx *gorm.DB, ids []uint, expected map[uint]string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(expected) != len(ids) {
		return fmt.Errorf("短信清理校验信息不完整，请重新预览")
	}
	var rows []db.SMS
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return fmt.Errorf("短信记录在清理期间发生变化，请重新预览")
	}
	for _, row := range rows {
		data, err := marshalArchiveJSON(smsRecordFromDB(row))
		expectedChecksum, found := expected[row.ID]
		if err != nil || !found || checksumBytes(data) != expectedChecksum {
			return fmt.Errorf("短信 %d 在清理期间发生变化，请重新预览", row.ID)
		}
	}
	return nil
}

func validateCallDeleteChecksums(tx *gorm.DB, ids []uint64, expected map[uint64]string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(expected) != len(ids) {
		return fmt.Errorf("通话清理校验信息不完整，请重新预览")
	}
	var rows []db.VoiceCallRecord
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return fmt.Errorf("通话记录在清理期间发生变化，请重新预览")
	}
	for _, row := range rows {
		data, err := marshalArchiveJSON(callRecordFromDB(row))
		expectedChecksum, found := expected[row.ID]
		if err != nil || !found || checksumBytes(data) != expectedChecksum {
			return fmt.Errorf("通话记录 %d 在清理期间发生变化，请重新预览", row.ID)
		}
	}
	return nil
}
