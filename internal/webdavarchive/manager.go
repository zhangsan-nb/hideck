package webdavarchive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yibaiba/hideck/pkg/logger"
)

const (
	minimumArchiveInterval = time.Hour
	maximumArchiveInterval = 30 * 24 * time.Hour
	maximumRetentionDays   = 3650
)

type RemoteFactory func(RuntimeConfig) (ProbeRemote, error)

type ManagerOptions struct {
	Store        Store
	RecordingDir string
	Config       RuntimeConfig
	NewRemote    RemoteFactory
	Now          func() time.Time
}

type Manager struct {
	store        Store
	recordingDir string
	newRemote    RemoteFactory
	now          func() time.Time

	mu     sync.RWMutex
	config RuntimeConfig
	status Status
	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("WebDAV 归档数据库未初始化")
	}
	if err := ValidateRuntimeConfig(options.Config); err != nil {
		return nil, err
	}
	factory := options.NewRemote
	if factory == nil {
		factory = defaultRemoteFactory
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store: options.Store, recordingDir: strings.TrimSpace(options.RecordingDir),
		newRemote: factory, now: now, config: options.Config, wake: make(chan struct{}, 1),
	}, nil
}

func ValidateRuntimeConfig(config RuntimeConfig) error {
	if config.Interval < minimumArchiveInterval || config.Interval > maximumArchiveInterval {
		return fmt.Errorf("归档周期必须在 1 小时到 30 天之间")
	}
	if config.RetentionDays <= 0 || config.RetentionDays > maximumRetentionDays {
		return fmt.Errorf("本地保留天数必须在 1 到 %d 天之间", maximumRetentionDays)
	}
	if !config.IncludeSMS && !config.IncludeCalls && !config.IncludeRecordings {
		return errors.New("至少选择一种归档内容")
	}
	if !config.Enabled && strings.TrimSpace(config.Endpoint) == "" {
		return nil
	}
	_, err := defaultRemoteFactory(config)
	return err
}

func (manager *Manager) Config() RuntimeConfig {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.config
}

func (manager *Manager) Status() Status {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.status
}

func (manager *Manager) Configure(config RuntimeConfig) error {
	if err := ValidateRuntimeConfig(config); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.config = config
	manager.mu.Unlock()
	manager.signalWake()
	return nil
}

func (manager *Manager) Probe(ctx context.Context, config RuntimeConfig) error {
	if err := ValidateRuntimeConfig(config); err != nil {
		return err
	}
	remote, err := manager.newRemote(config)
	if err != nil {
		return err
	}
	return remote.Probe(ctx)
}

func (manager *Manager) RunNow(ctx context.Context) (RunSummary, error) {
	config, err := manager.beginRun()
	if err != nil {
		return RunSummary{}, err
	}
	summary, runErr := manager.runArchive(ctx, config)
	manager.finishRun(summary, runErr)
	return summary, runErr
}

func (manager *Manager) PreviewCleanup(ctx context.Context) (CleanupPreview, error) {
	config, err := manager.beginExclusive()
	if err != nil {
		return CleanupPreview{}, err
	}
	defer manager.finishCleanup()
	service := manager.newCleanupService()
	return service.PreviewCleanup(ctx, config.ContentSettings())
}

func (manager *Manager) Cleanup(ctx context.Context, expectedToken string) (CleanupPreview, error) {
	config, err := manager.beginExclusive()
	if err != nil {
		return CleanupPreview{}, err
	}
	service := manager.newCleanupService()
	preview, cleanupErr := service.Cleanup(ctx, config.ContentSettings(), expectedToken)
	manager.finishCleanup()
	return preview, cleanupErr
}

func (manager *Manager) Start(parent context.Context) {
	manager.mu.Lock()
	if manager.cancel != nil {
		manager.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	manager.cancel = cancel
	manager.done = make(chan struct{})
	done := manager.done
	manager.mu.Unlock()
	go manager.loop(ctx, done)
}

func (manager *Manager) Stop(ctx context.Context) error {
	manager.mu.Lock()
	cancel, done := manager.cancel, manager.done
	manager.cancel = nil
	manager.done = nil
	manager.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	runImmediately := true
	for {
		config := manager.Config()
		if config.Enabled && runImmediately {
			manager.runScheduled(ctx)
		}
		runImmediately = false
		wait := config.Interval
		if !config.Enabled {
			wait = maximumArchiveInterval
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-manager.wake:
			timer.Stop()
			runImmediately = manager.Config().Enabled
		case <-timer.C:
			runImmediately = manager.Config().Enabled
		}
	}
}

func (manager *Manager) runScheduled(ctx context.Context) {
	summary, err := manager.RunNow(ctx)
	if errors.Is(err, ErrRunInProgress) {
		return
	}
	if err != nil {
		logger.Warn("WebDAV 定时归档失败", "err", err)
		return
	}
	config := manager.Config()
	if !config.AutoCleanup {
		return
	}
	preview, err := manager.PreviewCleanup(ctx)
	if err == nil {
		_, err = manager.Cleanup(ctx, preview.Token)
	}
	if err != nil {
		logger.Warn("WebDAV 归档后的本地清理失败", "err", err, "uploaded", summary.Uploaded)
	}
}

func (manager *Manager) beginRun() (RuntimeConfig, error) {
	config, err := manager.beginExclusive()
	if err != nil {
		return RuntimeConfig{}, err
	}
	manager.mu.Lock()
	now := manager.now().UTC()
	manager.status.LastStartedAt = &now
	manager.mu.Unlock()
	return config, nil
}

func (manager *Manager) beginExclusive() (RuntimeConfig, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.status.Running {
		return RuntimeConfig{}, ErrRunInProgress
	}
	manager.status.Running = true
	return manager.config, nil
}

func (manager *Manager) finishRun(summary RunSummary, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now().UTC()
	manager.status.Running = false
	manager.status.LastCompletedAt = &now
	manager.status.LastSummary = summary
	manager.status.LastError = ""
	if err != nil {
		manager.status.LastError = err.Error()
	}
}

func (manager *Manager) finishCleanup() {
	manager.mu.Lock()
	manager.status.Running = false
	manager.mu.Unlock()
}

func (manager *Manager) runArchive(ctx context.Context, config RuntimeConfig) (RunSummary, error) {
	service, err := manager.newService(config)
	if err != nil {
		return RunSummary{}, err
	}
	return service.Run(ctx, config.ContentSettings())
}

func (manager *Manager) newService(config RuntimeConfig) (*Service, error) {
	remote, err := manager.newRemote(config)
	if err != nil {
		return nil, err
	}
	return NewService(ServiceOptions{
		Store: manager.store, Remote: remote, RecordingDir: manager.recordingDir, Now: manager.now,
	})
}

func (manager *Manager) newCleanupService() *Service {
	return &Service{
		store: manager.store, recordingDir: manager.recordingDir, now: manager.now,
	}
}

func (manager *Manager) signalWake() {
	select {
	case manager.wake <- struct{}{}:
	default:
	}
}

func defaultRemoteFactory(config RuntimeConfig) (ProbeRemote, error) {
	return NewClient(ClientConfig{
		Endpoint: config.Endpoint, Username: config.Username, Password: config.Password, BasePath: config.BasePath,
	})
}
