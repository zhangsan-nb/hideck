package phone

import (
	"context"
	"errors"
)

func (s *Service) DeleteHistory(ctx context.Context, ids []uint64) (int64, error) {
	ids = uniqueHistoryIDs(ids)
	if len(ids) == 0 {
		return 0, errors.New("请选择要删除的通话记录")
	}
	if s.store == nil {
		return 0, errors.New("电话记录存储未启用")
	}

	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	records, err := s.store.Delete(ctx, ids)
	if err != nil {
		return 0, err
	}
	if s.deletedRecords == nil {
		s.deletedRecords = make(map[string]struct{})
	}
	for _, record := range records {
		s.deletedRecords[record.CallID] = struct{}{}
	}
	return int64(len(records)), nil
}

func uniqueHistoryIDs(ids []uint64) []uint64 {
	unique := make([]uint64, 0, len(ids))
	seen := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}
