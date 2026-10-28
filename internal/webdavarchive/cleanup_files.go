package webdavarchive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const cleanupStagingMarker = ".hideck-cleanup-"

type stagedRecording struct {
	original string
	staged   string
}

func stageRecordingFiles(paths []string, expected map[string]string) ([]stagedRecording, error) {
	staged := make([]stagedRecording, 0, len(paths))
	for _, original := range paths {
		entry, err := stageRecordingFile(original, expected[original])
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.Join(err, restoreStagedRecordings(staged))
		}
		staged = append(staged, entry)
	}
	return staged, nil
}

func stageRecordingFile(original, expectedChecksum string) (stagedRecording, error) {
	if expectedChecksum == "" {
		return stagedRecording{}, fmt.Errorf("录音 %q 缺少清理校验信息", original)
	}
	stagedPath, err := cleanupStagingPath(original)
	if err != nil {
		return stagedRecording{}, err
	}
	if err := os.Rename(original, stagedPath); err != nil {
		return stagedRecording{}, err
	}
	entry := stagedRecording{original: original, staged: stagedPath}
	checksum, _, hashErr := hashFile(stagedPath)
	if hashErr == nil && checksum == expectedChecksum {
		return entry, nil
	}
	restoreErr := os.Rename(stagedPath, original)
	if hashErr == nil {
		hashErr = fmt.Errorf("录音 %q 在清理前发生变化，请重新预览", original)
	}
	return stagedRecording{}, errors.Join(hashErr, restoreErr)
}

func cleanupStagingPath(original string) (string, error) {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("生成录音清理暂存名失败: %w", err)
	}
	return original + cleanupStagingMarker + hex.EncodeToString(token[:]), nil
}

func restoreStagedRecordings(entries []stagedRecording) error {
	var result error
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if err := os.Rename(entry.staged, entry.original); err != nil {
			result = errors.Join(result, fmt.Errorf("恢复录音 %q 失败: %w", entry.original, err))
		}
	}
	return result
}

func discardStagedRecordings(entries []stagedRecording) error {
	for _, entry := range entries {
		if err := os.Remove(entry.staged); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除已归档录音 %q 失败: %w", entry.original, err)
		}
	}
	return nil
}

func recoverStagedRecordings(ctx context.Context, store Store, directory string) error {
	staged, err := listStagedRecordings(directory)
	if err != nil || len(staged) == 0 {
		return err
	}
	calls, err := store.ListCalls(ctx)
	if err != nil {
		return fmt.Errorf("恢复录音清理暂存文件时读取通话记录失败: %w", err)
	}
	referenced := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.RecordingName); name != "" {
			referenced[name] = struct{}{}
		}
	}
	for _, entry := range staged {
		name := filepath.Base(entry.original)
		_, remains := referenced[name]
		if !remains {
			archived, archiveErr := stagedRecordingArchived(ctx, store, entry, name)
			if archiveErr != nil {
				return archiveErr
			}
			if !archived {
				return fmt.Errorf("录音暂存文件 %q 没有匹配的归档台账，请人工确认", entry.staged)
			}
		}
		if err := recoverStagedRecording(entry, remains); err != nil {
			return err
		}
	}
	return nil
}

func stagedRecordingArchived(
	ctx context.Context,
	store Store,
	entry stagedRecording,
	localKey string,
) (bool, error) {
	checksum, _, err := hashFile(entry.staged)
	if err != nil {
		return false, err
	}
	object, found, err := store.FindObject(ctx, KindRecording, localKey)
	if err != nil {
		return false, fmt.Errorf("读取录音暂存文件归档台账失败: %w", err)
	}
	return found && object.Checksum == checksum, nil
}

func listStagedRecordings(directory string) ([]stagedRecording, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]stagedRecording, 0)
	for _, entry := range entries {
		name := entry.Name()
		marker := strings.LastIndex(name, cleanupStagingMarker)
		if entry.IsDir() || marker <= 0 || !validStagingToken(name[marker+len(cleanupStagingMarker):]) {
			continue
		}
		result = append(result, stagedRecording{
			original: filepath.Join(directory, name[:marker]), staged: filepath.Join(directory, name),
		})
	}
	return result, nil
}

func validStagingToken(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func recoverStagedRecording(entry stagedRecording, referenced bool) error {
	if !referenced {
		if err := os.Remove(entry.staged); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理残留录音暂存文件 %q 失败: %w", entry.staged, err)
		}
		return nil
	}
	if _, err := os.Lstat(entry.original); err == nil {
		return fmt.Errorf("录音 %q 与清理暂存文件同时存在，请人工确认", entry.original)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(entry.staged, entry.original); err != nil {
		return fmt.Errorf("恢复录音 %q 失败: %w", entry.original, err)
	}
	return nil
}
