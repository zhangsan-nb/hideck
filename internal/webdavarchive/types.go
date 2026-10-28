package webdavarchive

import (
	"context"
	"errors"
	"time"
)

var ErrRunInProgress = errors.New("WebDAV 归档任务正在运行")
var ErrCleanupPreviewChanged = errors.New("本地数据已发生变化，请重新预览后再清理")

const (
	KindSMS       = "sms"
	KindCall      = "call"
	KindRecording = "recording"
)

type Settings struct {
	IncludeSMS        bool
	IncludeCalls      bool
	IncludeRecordings bool
	RetentionDays     int
}

type RuntimeConfig struct {
	Enabled           bool
	Endpoint          string
	Username          string
	Password          string
	BasePath          string
	Interval          time.Duration
	RetentionDays     int
	AutoCleanup       bool
	IncludeSMS        bool
	IncludeCalls      bool
	IncludeRecordings bool
}

func (config RuntimeConfig) ContentSettings() Settings {
	return Settings{
		IncludeSMS: config.IncludeSMS, IncludeCalls: config.IncludeCalls,
		IncludeRecordings: config.IncludeRecordings, RetentionDays: config.RetentionDays,
	}
}

type SMSRecord struct {
	ID         uint      `json:"id"`
	IMSI       string    `json:"imsi"`
	ICCID      string    `json:"iccid"`
	Peer       string    `json:"peer"`
	LocalPhone string    `json:"local_phone"`
	Sender     string    `json:"sender"`
	Recipient  string    `json:"recipient"`
	Content    string    `json:"content"`
	Type       int       `json:"type"`
	Status     int       `json:"status"`
	Timestamp  time.Time `json:"timestamp"`
	Incomplete bool      `json:"incomplete,omitempty"`
}

type CallRecord struct {
	ID              uint64     `json:"id"`
	CallID          string     `json:"call_id"`
	DeviceID        string     `json:"device_id"`
	ICCID           string     `json:"iccid"`
	Direction       string     `json:"direction"`
	Peer            string     `json:"peer"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	AnsweredAt      *time.Time `json:"answered_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	DurationSeconds int64      `json:"duration_seconds"`
	EndReason       string     `json:"end_reason"`
	Codec           string     `json:"codec"`
	RecordingName   string     `json:"recording_name,omitempty"`
	RecordingError  string     `json:"recording_error,omitempty"`
}

type Object struct {
	Kind       string
	LocalKey   string
	Checksum   string
	RemotePath string
	SizeBytes  int64
	SourceTime time.Time
	ArchivedAt time.Time
}

type DeleteRequest struct {
	SMSIDs        []uint
	CallIDs       []uint64
	SMSChecksums  map[uint]string
	CallChecksums map[uint64]string
}

type Store interface {
	ListSMS(context.Context) ([]SMSRecord, error)
	ListCalls(context.Context) ([]CallRecord, error)
	FindObject(context.Context, string, string) (Object, bool, error)
	UpsertObject(context.Context, Object) error
	DeleteRecords(context.Context, DeleteRequest) error
}

type Remote interface {
	Put(context.Context, Upload) error
	PutFile(context.Context, FileUpload) error
}

type ProbeRemote interface {
	Remote
	Probe(context.Context) error
}

type RunSummary struct {
	Uploaded int   `json:"uploaded"`
	Skipped  int   `json:"skipped"`
	Bytes    int64 `json:"bytes"`
}

type Status struct {
	Running         bool       `json:"running"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	LastSummary     RunSummary `json:"last_summary"`
}

type CleanupPreview struct {
	Token              string            `json:"token"`
	Before             time.Time         `json:"before"`
	SMSCount           int               `json:"sms_count"`
	CallCount          int               `json:"call_count"`
	RecordingCount     int               `json:"recording_count"`
	RecordingBytes     int64             `json:"recording_bytes"`
	EligibleSMSIDs     []uint            `json:"-"`
	EligibleCallIDs    []uint64          `json:"-"`
	RecordingFiles     []string          `json:"-"`
	SMSChecksums       map[uint]string   `json:"-"`
	CallChecksums      map[uint64]string `json:"-"`
	RecordingChecksums map[string]string `json:"-"`
}
