package webdavarchive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func (service *Service) PreviewCleanup(ctx context.Context, settings Settings) (CleanupPreview, error) {
	if settings.RetentionDays <= 0 {
		return CleanupPreview{}, fmt.Errorf("本地保留天数必须大于 0")
	}
	if settings.IncludeCalls || settings.IncludeRecordings {
		if err := recoverStagedRecordings(ctx, service.store, service.recordingDir); err != nil {
			return CleanupPreview{}, err
		}
	}
	preview := CleanupPreview{
		Before:       service.now().AddDate(0, 0, -settings.RetentionDays).UTC(),
		SMSChecksums: make(map[uint]string), CallChecksums: make(map[uint64]string),
		RecordingChecksums: make(map[string]string),
	}
	if settings.IncludeSMS {
		if err := service.collectCleanupSMS(ctx, &preview); err != nil {
			return CleanupPreview{}, err
		}
	}
	if settings.IncludeCalls || settings.IncludeRecordings {
		if err := service.collectCleanupCalls(ctx, settings, &preview); err != nil {
			return CleanupPreview{}, err
		}
	}
	preview.Token = cleanupPreviewToken(preview)
	return preview, nil
}

func (service *Service) Cleanup(ctx context.Context, settings Settings, expectedToken string) (CleanupPreview, error) {
	preview, err := service.PreviewCleanup(ctx, settings)
	if err != nil {
		return CleanupPreview{}, err
	}
	if expectedToken == "" || expectedToken != preview.Token {
		return preview, ErrCleanupPreviewChanged
	}
	staged, err := stageRecordingFiles(preview.RecordingFiles, preview.RecordingChecksums)
	if err != nil {
		return preview, err
	}
	request := DeleteRequest{
		SMSIDs: preview.EligibleSMSIDs, CallIDs: preview.EligibleCallIDs,
		SMSChecksums: preview.SMSChecksums, CallChecksums: preview.CallChecksums,
	}
	if err := service.store.DeleteRecords(ctx, request); err != nil {
		restoreErr := restoreStagedRecordings(staged)
		return preview, errors.Join(fmt.Errorf("删除已归档本地记录失败: %w", err), restoreErr)
	}
	if err := discardStagedRecordings(staged); err != nil {
		return preview, err
	}
	return preview, nil
}

func (service *Service) collectCleanupSMS(ctx context.Context, preview *CleanupPreview) error {
	rows, err := service.store.ListSMS(ctx)
	if err != nil {
		return fmt.Errorf("读取短信记录失败: %w", err)
	}
	for _, row := range rows {
		if !row.Timestamp.Before(preview.Before) {
			continue
		}
		data, err := marshalArchiveJSON(row)
		if err != nil {
			return err
		}
		item := newDataItem(KindSMS, strconv.FormatUint(uint64(row.ID), 10), row.Timestamp, data)
		matches, err := service.objectMatches(ctx, item.object)
		if err != nil {
			return err
		}
		if matches {
			preview.EligibleSMSIDs = append(preview.EligibleSMSIDs, row.ID)
			preview.SMSChecksums[row.ID] = item.object.Checksum
			preview.SMSCount++
		}
	}
	return nil
}

func (service *Service) collectCleanupCalls(ctx context.Context, settings Settings, preview *CleanupPreview) error {
	rows, err := service.store.ListCalls(ctx)
	if err != nil {
		return fmt.Errorf("读取通话记录失败: %w", err)
	}
	protectedRecordings := currentRecordingNames(rows, preview.Before)
	seenRecordings := make(map[string]struct{})
	for _, row := range rows {
		if !endedBefore(row, preview.Before) {
			continue
		}
		files, bytes, checksum, safe, err := service.cleanupRecording(ctx, settings, row)
		if err != nil {
			return err
		}
		callChecksum, callEligible, err := service.cleanupCallEligible(ctx, settings, row, safe)
		if err != nil {
			return err
		}
		if callEligible {
			preview.EligibleCallIDs = append(preview.EligibleCallIDs, row.ID)
			preview.CallChecksums[row.ID] = callChecksum
			preview.CallCount++
		}
		if _, protected := protectedRecordings[strings.TrimSpace(row.RecordingName)]; protected {
			continue
		}
		for _, path := range files {
			if _, found := seenRecordings[path]; found {
				continue
			}
			seenRecordings[path] = struct{}{}
			preview.RecordingFiles = append(preview.RecordingFiles, path)
			preview.RecordingChecksums[path] = checksum
			preview.RecordingCount++
			preview.RecordingBytes += bytes
		}
	}
	return nil
}

func (service *Service) cleanupCallEligible(
	ctx context.Context,
	settings Settings,
	row CallRecord,
	recordingSafe bool,
) (string, bool, error) {
	if !settings.IncludeCalls {
		return "", false, nil
	}
	object, archived, err := service.callArchived(ctx, row)
	if err != nil || !archived {
		return "", false, err
	}
	if strings.TrimSpace(row.RecordingName) == "" {
		return object.Checksum, true, nil
	}
	return object.Checksum, recordingSafe, nil
}

func currentRecordingNames(rows []CallRecord, cutoff time.Time) map[string]struct{} {
	result := make(map[string]struct{})
	for _, row := range rows {
		name := strings.TrimSpace(row.RecordingName)
		if name != "" && !endedBefore(row, cutoff) {
			result[name] = struct{}{}
		}
	}
	return result
}

func (service *Service) callArchived(ctx context.Context, row CallRecord) (Object, bool, error) {
	data, err := marshalArchiveJSON(row)
	if err != nil {
		return Object{}, false, err
	}
	item := newDataItem(KindCall, strconv.FormatUint(row.ID, 10), row.StartedAt, data)
	matches, err := service.objectMatches(ctx, item.object)
	return item.object, matches, err
}

func (service *Service) cleanupRecording(
	ctx context.Context,
	settings Settings,
	row CallRecord,
) ([]string, int64, string, bool, error) {
	name := strings.TrimSpace(row.RecordingName)
	if name == "" {
		return nil, 0, "", true, nil
	}
	if !settings.IncludeRecordings {
		return nil, 0, "", false, nil
	}
	path, err := safeRecordingPath(service.recordingDir, name)
	if err != nil {
		return nil, 0, "", false, nil
	}
	checksum, size, err := hashFile(path)
	if os.IsNotExist(err) {
		_, found, findErr := service.store.FindObject(ctx, KindRecording, name)
		return nil, 0, "", found, findErr
	}
	if err != nil {
		return nil, 0, "", false, nil
	}
	object := Object{Kind: KindRecording, LocalKey: name, Checksum: checksum}
	matches, err := service.objectMatches(ctx, object)
	if err != nil || !matches {
		return nil, 0, "", false, err
	}
	return []string{path}, size, checksum, true, nil
}

func (service *Service) objectMatches(ctx context.Context, expected Object) (bool, error) {
	stored, found, err := service.store.FindObject(ctx, expected.Kind, expected.LocalKey)
	if err != nil {
		return false, fmt.Errorf("读取归档台账失败: %w", err)
	}
	return found && stored.Checksum == expected.Checksum, nil
}

func endedBefore(row CallRecord, cutoff time.Time) bool {
	return row.EndedAt != nil && row.EndedAt.Before(cutoff)
}
