package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yibaiba/hideck/internal/phone"
	"github.com/yibaiba/hideck/pkg/logger"
)

func (s *Server) installAutoAnswerAudio(temporaryPath, target string) error {
	previous := s.phone.AutoAnswerStatus()
	backupPath, hadPreviousFile, err := moveAutoAnswerAudioAside(target)
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return errors.Join(err, restoreAutoAnswerAudioFile(target, backupPath, hadPreviousFile))
	}
	if err := s.reloadAutoAnswerAudio(target); err != nil {
		fileErr := restoreAutoAnswerAudioFile(target, backupPath, hadPreviousFile)
		runtimeErr := s.restoreAutoAnswerRuntime(previous)
		return errors.Join(err, fileErr, runtimeErr)
	}
	if hadPreviousFile {
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn("清理自动接听录音备份失败", "err", err)
		}
	}
	return nil
}

func moveAutoAnswerAudioAside(target string) (string, bool, error) {
	_, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	backup, err := os.CreateTemp(filepath.Dir(target), ".auto-answer-backup-*.wav")
	if err != nil {
		return "", false, err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", false, err
	}
	if err := os.Remove(backupPath); err != nil {
		return "", false, err
	}
	if err := os.Rename(target, backupPath); err != nil {
		return "", false, err
	}
	return backupPath, true, nil
}

func restoreAutoAnswerAudioFile(target, backupPath string, hadPreviousFile bool) error {
	removeErr := os.Remove(target)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	if !hadPreviousFile {
		return removeErr
	}
	restoreErr := os.Rename(backupPath, target)
	if restoreErr != nil {
		restoreErr = fmt.Errorf("恢复原自动接听录音失败，备份保留在 %s: %w", backupPath, restoreErr)
	}
	return errors.Join(removeErr, restoreErr)
}

func (s *Server) restoreAutoAnswerRuntime(previous phone.AutoAnswerStatus) error {
	config := phone.AutoAnswerConfig{AudioPath: previous.AudioPath, Devices: previous.Devices}
	if err := s.phone.ConfigureAutoAnswer(config); err != nil {
		return fmt.Errorf("恢复自动接听运行时失败: %w", err)
	}
	return nil
}
