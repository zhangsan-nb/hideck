package phone

import (
	"context"
	"testing"
)

func TestDeleteHistoryDeduplicatesIDsAndPreventsLateRecreation(t *testing.T) {
	store := newMemoryCallStore()
	store.records["finished"] = CallRecord{
		ID: 7, CallID: "finished", Status: StatusCompleted,
	}
	service := &Service{store: store, deletedRecords: make(map[string]struct{})}
	deleted, err := service.DeleteHistory(context.Background(), []uint64{0, 7, 7})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	service.persist(CallRecord{ID: 7, CallID: "finished", Status: StatusCompleted})
	if _, exists := store.records["finished"]; exists {
		t.Fatal("late persistence recreated deleted history")
	}
}

func TestDeleteHistoryRejectsEmptySelection(t *testing.T) {
	service := &Service{store: newMemoryCallStore(), deletedRecords: make(map[string]struct{})}
	if _, err := service.DeleteHistory(context.Background(), []uint64{0}); err == nil {
		t.Fatal("DeleteHistory accepted an empty selection")
	}
}
