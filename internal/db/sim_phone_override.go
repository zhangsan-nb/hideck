package db

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const (
	manualPhoneMinDigits = 6
	manualPhoneMaxDigits = 15
)

// NormalizeManualPhoneNumber validates an explicit user override. Unlike
// network-learned numbers, manual input is not repaired by deleting separators.
func NormalizeManualPhoneNumber(raw string) (string, error) {
	phone := strings.TrimSpace(raw)
	if phone == "" {
		return "", nil
	}
	digits := strings.TrimPrefix(phone, "+")
	if len(digits) < manualPhoneMinDigits || len(digits) > manualPhoneMaxDigits {
		return "", fmt.Errorf("本机号码必须包含 %d–%d 位数字", manualPhoneMinDigits, manualPhoneMaxDigits)
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return "", errors.New("本机号码只能包含数字和开头的 +")
		}
	}
	return phone, nil
}

func getCardPolicyPhoneOverride(iccid string) (string, error) {
	iccid = CanonicalICCID(iccid)
	if DB == nil || iccid == "" {
		return "", nil
	}
	var policy CardPolicy
	err := DB.Select("local_phone_override").Where("iccid = ?", iccid).Take(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return NormalizeManualPhoneNumber(policy.LocalPhoneOverride)
}

type SIMPhoneNumberIndex struct {
	ByICCID map[string]string
	ByIMSI  map[string]string
}

// GetSIMPhoneNumberIndex returns effective numbers in bulk for list endpoints.
// Exact ICCID lookups must not fall back to a reused IMSI from another card.
func GetSIMPhoneNumberIndex() (SIMPhoneNumberIndex, error) {
	index := SIMPhoneNumberIndex{ByICCID: map[string]string{}, ByIMSI: map[string]string{}}
	if DB == nil {
		return index, nil
	}
	overrides, err := loadPhoneOverrides()
	if err != nil {
		return index, err
	}
	for iccid, phone := range overrides {
		index.ByICCID[iccid] = phone
	}
	if err := addSubscriptionPhones(&index, overrides); err != nil {
		return index, err
	}
	if err := addPendingPhones(&index, overrides); err != nil {
		return index, err
	}
	return index, nil
}

func loadPhoneOverrides() (map[string]string, error) {
	var policies []CardPolicy
	if err := DB.Select("iccid", "local_phone_override").
		Where("COALESCE(local_phone_override, '') <> ''").Find(&policies).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(policies))
	for _, policy := range policies {
		phone, err := NormalizeManualPhoneNumber(policy.LocalPhoneOverride)
		if err != nil {
			return nil, fmt.Errorf("card %s: %w", CanonicalICCID(policy.ICCID), err)
		}
		result[CanonicalICCID(policy.ICCID)] = phone
	}
	return result, nil
}

func addSubscriptionPhones(index *SIMPhoneNumberIndex, overrides map[string]string) error {
	var rows []SIMSubscription
	if err := DB.Select("imsi", "current_iccid", "phone_number").Order("updated_at DESC").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		iccid := CanonicalICCID(row.CurrentICCID)
		phone := strings.TrimSpace(overrides[iccid])
		if phone == "" {
			phone = strings.TrimSpace(row.PhoneNumber)
		}
		if phone == "" {
			continue
		}
		if iccid != "" {
			if _, exists := index.ByICCID[iccid]; !exists {
				index.ByICCID[iccid] = phone
			}
		}
		if imsi := strings.TrimSpace(row.IMSI); imsi != "" {
			if _, exists := index.ByIMSI[imsi]; !exists {
				index.ByIMSI[imsi] = phone
			}
		}
	}
	return nil
}

func addPendingPhones(index *SIMPhoneNumberIndex, overrides map[string]string) error {
	var rows []PendingPhoneNumber
	if err := DB.Select("iccid", "phone_number").Order("updated_at DESC").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		iccid := CanonicalICCID(row.ICCID)
		if _, exists := index.ByICCID[iccid]; exists {
			continue
		}
		phone := strings.TrimSpace(overrides[iccid])
		if phone == "" {
			phone = strings.TrimSpace(row.PhoneNumber)
		}
		if iccid != "" && phone != "" {
			index.ByICCID[iccid] = phone
		}
	}
	return nil
}
