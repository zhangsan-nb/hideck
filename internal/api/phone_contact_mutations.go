package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/db"
	"gorm.io/gorm"
)

type phoneContactBatchRequest struct {
	DeviceID string                `json:"device_id"`
	Contacts []phoneContactRequest `json:"contacts"`
	Atomic   bool                  `json:"atomic"`
}

type phoneContactGroupRequest struct {
	ContactID string `json:"contact_id"`
	Name      string `json:"name"`
}

func (s *Server) handlePhoneContactsBatch(c *gin.Context) {
	s.usePhoneContactsBatch(c)
}

func (s *Server) usePhoneContactsBatch(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	var req phoneContactBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "code": "invalid_json", "message": "请求格式不正确"})
		return
	}
	if len(req.Contacts) == 0 || len(req.Contacts) > maxContactBatch {
		writePhoneContactBatchSizeError(c, len(req.Contacts))
		return
	}
	region := s.phoneNumberRegion(req.DeviceID)
	inputs := phoneContactInputs(req.Contacts, region)
	if req.Atomic {
		saveAtomicPhoneContactBatch(c, inputs, region)
		return
	}
	imported, skipped, err := db.UpsertPhoneContactsWithRegion(businessRequestContext(c), inputs, region)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error", "code": "contact_batch_failed", "message": "批量保存失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": imported, "skipped": skipped})
}

func writePhoneContactBatchSizeError(c businessContext, size int) {
	if size == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "code": "empty_batch", "message": "没有可添加的联系人"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"status": "error", "code": "batch_too_large", "message": "一次最多导入 5000 条",
	})
}

func phoneContactInputs(contacts []phoneContactRequest, region string) []db.PhoneContactInput {
	inputs := make([]db.PhoneContactInput, 0, len(contacts))
	for _, item := range contacts {
		inputs = append(inputs, db.PhoneContactInput{
			Number: item.Number, Name: item.Name, Region: region,
			ContactID: item.ContactID, GroupKey: item.GroupKey,
		})
	}
	return inputs
}

func saveAtomicPhoneContactBatch(c businessContext, inputs []db.PhoneContactInput, region string) {
	rows, err := db.UpsertPhoneContactBatchWithRegion(businessRequestContext(c), inputs, region)
	if err != nil {
		c.JSON(phoneContactMutationStatus(err), gin.H{
			"status": "error", "code": "contact_batch_failed", "message": "批量保存失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"imported": len(rows), "skipped": 0, "contacts": phoneContactIdentities(rows),
	})
}

func phoneContactMutationStatus(err error) int {
	if errors.Is(err, db.ErrInvalidPhoneContact) {
		return http.StatusBadRequest
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, gorm.ErrInvalidDB) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func (s *Server) handlePhoneContactGroupUpdate(c *gin.Context) {
	s.usePhoneContactGroupUpdate(c)
}

func (s *Server) usePhoneContactGroupUpdate(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	var req phoneContactGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "code": "invalid_json", "message": "请求格式不正确"})
		return
	}
	rows, err := db.UpdatePhoneContactGroupName(businessRequestContext(c), req.ContactID, req.Name)
	if err != nil {
		c.JSON(phoneContactMutationStatus(err), gin.H{
			"status": "error", "code": "contact_group_update_failed", "message": "更新联系人失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": phoneContactIdentities(rows)})
}

func (s *Server) handlePhoneContactGroupDelete(c *gin.Context) {
	s.usePhoneContactGroupDelete(c)
}

func (s *Server) usePhoneContactGroupDelete(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	numbers, err := db.DeletePhoneContactGroup(businessRequestContext(c), c.Query("contact_id"))
	if err != nil {
		c.JSON(phoneContactMutationStatus(err), gin.H{
			"status": "error", "code": "contact_group_delete_failed", "message": "删除联系人失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": len(numbers), "numbers": numbers})
}
