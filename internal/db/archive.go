package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ArchiveObject struct {
	Kind       string    `gorm:"column:kind;primaryKey"`
	LocalKey   string    `gorm:"column:local_key;primaryKey"`
	Checksum   string    `gorm:"column:checksum;not null"`
	RemotePath string    `gorm:"column:remote_path;not null"`
	SizeBytes  int64     `gorm:"column:size_bytes;not null"`
	SourceTime time.Time `gorm:"column:source_time;index;not null"`
	ArchivedAt time.Time `gorm:"column:archived_at;index;not null"`
}

func (ArchiveObject) TableName() string { return "archive_objects" }

type ArchiveDeleteRequest struct {
	SMSIDs  []uint
	CallIDs []uint64
}

func ListArchiveSMS(ctx context.Context, database *gorm.DB) ([]SMS, error) {
	var rows []SMS
	err := database.WithContext(ctx).Order("id asc").Find(&rows).Error
	return rows, err
}

func ListArchiveCalls(ctx context.Context, database *gorm.DB) ([]VoiceCallRecord, error) {
	var rows []VoiceCallRecord
	err := database.WithContext(ctx).Order("id asc").Find(&rows).Error
	return rows, err
}

func FindArchiveObject(ctx context.Context, database *gorm.DB, kind, localKey string) (ArchiveObject, bool, error) {
	var row ArchiveObject
	err := database.WithContext(ctx).Where("kind = ? AND local_key = ?", kind, localKey).First(&row).Error
	if err == nil {
		return row, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ArchiveObject{}, false, nil
	}
	return ArchiveObject{}, false, err
}

func UpsertArchiveObject(ctx context.Context, database *gorm.DB, object ArchiveObject) error {
	return database.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "local_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"checksum", "remote_path", "size_bytes", "source_time", "archived_at"}),
	}).Create(&object).Error
}

func DeleteArchivedRecords(ctx context.Context, database *gorm.DB, request ArchiveDeleteRequest) error {
	return database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return DeleteArchivedRecordsTx(tx, request)
	})
}

func DeleteArchivedRecordsTx(tx *gorm.DB, request ArchiveDeleteRequest) error {
	scopes, err := archiveSMSThreadScopes(tx, request.SMSIDs)
	if err != nil {
		return err
	}
	if len(request.SMSIDs) > 0 {
		if err := tx.Where("id IN ?", request.SMSIDs).Delete(&SMS{}).Error; err != nil {
			return err
		}
	}
	if len(request.CallIDs) > 0 {
		if err := tx.Where("id IN ?", request.CallIDs).Delete(&VoiceCallRecord{}).Error; err != nil {
			return err
		}
	}
	for _, scope := range scopes {
		if _, err := rebuildSMSContactTx(tx, scope.iccid, scope.peer); err != nil {
			return err
		}
	}
	return nil
}

type archiveSMSThreadScope struct {
	iccid string
	peer  string
}

func archiveSMSThreadScopes(tx *gorm.DB, ids []uint) ([]archiveSMSThreadScope, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []SMS
	if err := tx.Select("iccid", "peer").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := make(map[archiveSMSThreadScope]struct{})
	for _, row := range rows {
		scope := archiveSMSThreadScope{iccid: CanonicalICCID(row.ICCID), peer: strings.TrimSpace(row.Peer)}
		if scope.iccid != "" && scope.peer != "" {
			seen[scope] = struct{}{}
		}
	}
	result := make([]archiveSMSThreadScope, 0, len(seen))
	for scope := range seen {
		result = append(result, scope)
	}
	return result, nil
}
