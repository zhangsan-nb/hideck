package db

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// GetPhoneNumberForSMSIdentity prefers the captured ICCID over an IMSI that
// may be reused across profiles. It falls back to IMSI only without an exact hit.
func GetPhoneNumberForSMSIdentity(identity SMSIdentity) (string, error) {
	iccid := CanonicalICCID(identity.ICCID)
	if phone, err := getSIMSubscriptionPhoneNumberByICCID(iccid); err != nil || phone != "" {
		return phone, err
	}
	if phone, err := getPendingPhoneNumberByICCID(iccid); err != nil || phone != "" {
		return phone, err
	}
	return GetSIMCardPhoneNumberByIMSI(strings.TrimSpace(identity.IMSI))
}

func getPendingPhoneNumberByICCID(iccid string) (string, error) {
	if DB == nil || iccid == "" {
		return "", nil
	}
	var pending PendingPhoneNumber
	err := DB.Select("phone_number").
		Where("iccid = ? AND COALESCE(phone_number, '') <> ''", iccid).
		Limit(1).Take(&pending).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return strings.TrimSpace(pending.PhoneNumber), err
}
