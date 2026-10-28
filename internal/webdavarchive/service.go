package webdavarchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServiceOptions struct {
	Store        Store
	Remote       Remote
	RecordingDir string
	Now          func() time.Time
}

type Service struct {
	store        Store
	remote       Remote
	recordingDir string
	now          func() time.Time
}

type archiveItem struct {
	object      Object
	data        []byte
	localPath   string
	contentType string
}

func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil || options.Remote == nil {
		return nil, errors.New("WebDAV 归档存储未初始化")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store: options.Store, remote: options.Remote,
		recordingDir: strings.TrimSpace(options.RecordingDir), now: now,
	}, nil
}

func (service *Service) Run(ctx context.Context, settings Settings) (RunSummary, error) {
	var summary RunSummary
	if !settings.IncludeSMS && !settings.IncludeCalls && !settings.IncludeRecordings {
		return summary, errors.New("至少选择一种归档内容")
	}
	if settings.IncludeSMS {
		if err := service.archiveSMS(ctx, &summary); err != nil {
			return summary, err
		}
	}
	if settings.IncludeCalls || settings.IncludeRecordings {
		calls, err := service.store.ListCalls(ctx)
		if err != nil {
			return summary, fmt.Errorf("读取通话记录失败: %w", err)
		}
		if settings.IncludeCalls {
			if err := service.archiveCalls(ctx, calls, &summary); err != nil {
				return summary, err
			}
		}
		if settings.IncludeRecordings {
			if err := service.archiveRecordings(ctx, calls, &summary); err != nil {
				return summary, err
			}
		}
	}
	return summary, nil
}

func (service *Service) archiveSMS(ctx context.Context, summary *RunSummary) error {
	rows, err := service.store.ListSMS(ctx)
	if err != nil {
		return fmt.Errorf("读取短信记录失败: %w", err)
	}
	for _, row := range rows {
		data, err := marshalArchiveJSON(row)
		if err != nil {
			return fmt.Errorf("编码短信 %d 失败: %w", row.ID, err)
		}
		item := newDataItem(KindSMS, strconv.FormatUint(uint64(row.ID), 10), row.Timestamp, data)
		if err := service.archiveItem(ctx, item, summary); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) archiveCalls(ctx context.Context, rows []CallRecord, summary *RunSummary) error {
	for _, row := range rows {
		data, err := marshalArchiveJSON(row)
		if err != nil {
			return fmt.Errorf("编码通话记录 %d 失败: %w", row.ID, err)
		}
		item := newDataItem(KindCall, strconv.FormatUint(row.ID, 10), row.StartedAt, data)
		if err := service.archiveItem(ctx, item, summary); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) archiveRecordings(ctx context.Context, rows []CallRecord, summary *RunSummary) error {
	seen := make(map[string]struct{})
	for _, row := range rows {
		name := strings.TrimSpace(row.RecordingName)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		item, err := service.recordingItem(row)
		if err != nil {
			return fmt.Errorf("准备录音 %q 失败: %w", name, err)
		}
		if err := service.archiveItem(ctx, item, summary); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) archiveItem(ctx context.Context, item archiveItem, summary *RunSummary) error {
	stored, found, err := service.store.FindObject(ctx, item.object.Kind, item.object.LocalKey)
	if err != nil {
		return fmt.Errorf("读取归档台账失败: %w", err)
	}
	if found && stored.Checksum == item.object.Checksum && stored.RemotePath == item.object.RemotePath {
		summary.Skipped++
		return nil
	}
	if item.localPath != "" {
		err = service.remote.PutFile(ctx, FileUpload{
			RemotePath: item.object.RemotePath, LocalPath: item.localPath, ContentType: item.contentType,
		})
		if err == nil {
			err = verifyFileUnchanged(item.localPath, item.object)
		}
	} else {
		err = service.remote.Put(ctx, Upload{
			RemotePath: item.object.RemotePath, Data: item.data, ContentType: item.contentType,
		})
	}
	if err != nil {
		return fmt.Errorf("上传 %s/%s 失败: %w", item.object.Kind, item.object.LocalKey, err)
	}
	item.object.ArchivedAt = service.now().UTC()
	if err := service.store.UpsertObject(ctx, item.object); err != nil {
		return fmt.Errorf("保存归档台账失败: %w", err)
	}
	summary.Uploaded++
	summary.Bytes += item.object.SizeBytes
	return nil
}

func newDataItem(kind, localKey string, sourceTime time.Time, data []byte) archiveItem {
	return archiveItem{
		object: Object{
			Kind: kind, LocalKey: localKey, Checksum: checksumBytes(data),
			RemotePath: datedRemotePath(kind, sourceTime, kind+"-"+localKey+".json"),
			SizeBytes:  int64(len(data)), SourceTime: sourceTime.UTC(),
		},
		data: data, contentType: "application/json; charset=utf-8",
	}
}

func checksumBytes(data []byte) string {
	checksum := sha256.Sum256(data)
	return hex.EncodeToString(checksum[:])
}

func (service *Service) recordingItem(call CallRecord) (archiveItem, error) {
	name := strings.TrimSpace(call.RecordingName)
	localPath, err := safeRecordingPath(service.recordingDir, name)
	if err != nil {
		return archiveItem{}, err
	}
	checksum, size, err := hashFile(localPath)
	if err != nil {
		return archiveItem{}, err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return archiveItem{
		object: Object{
			Kind: KindRecording, LocalKey: name, Checksum: checksum,
			RemotePath: datedRemotePath(KindRecording, call.StartedAt, name),
			SizeBytes:  size, SourceTime: call.StartedAt.UTC(),
		},
		localPath: localPath, contentType: contentType,
	}, nil
}

func marshalArchiveJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func datedRemotePath(kind string, at time.Time, name string) string {
	if at.IsZero() {
		return kind + "/unknown/" + name
	}
	return fmt.Sprintf("%s/%04d/%02d/%s", kind, at.Year(), at.Month(), name)
}

func safeRecordingPath(directory, name string) (string, error) {
	if directory == "" {
		return "", errors.New("录音目录未配置")
	}
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return "", errors.New("录音文件名无效")
	}
	return filepath.Join(directory, name), nil
}

func hashFile(path string) (string, int64, error) {
	file, info, err := openRegularFile(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func verifyFileUnchanged(path string, expected Object) error {
	checksum, size, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("上传后重新读取录音失败: %w", err)
	}
	if checksum != expected.Checksum || size != expected.SizeBytes {
		return errors.New("录音在上传期间发生变化，请等待录音完成后重试")
	}
	return nil
}

func openRegularFile(path string) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, errors.New("归档路径不是普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("归档文件在打开时发生变化")
	}
	return file, openedInfo, nil
}
