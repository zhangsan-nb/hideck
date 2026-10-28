package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrSMSIdentityUnknown  = errors.New("sms identity unknown")
	ErrSMSIdentityConflict = errors.New("sms identity conflict")
)

type SMSIdentity struct {
	ICCID string
	IMSI  string
}

type SMSRecord struct {
	Identity   SMSIdentity
	LocalPhone string
	Sender     string
	Recipient  string
	Content    string
	Type       int
	Status     int
	Timestamp  time.Time
}

// SMSDeviceSelector pins a lookup to the physical modem when its IMEI is known.
// DeviceID is only used for legacy bindings without a physical identity.
type SMSDeviceSelector struct {
	DeviceID string
	IMEI     string
}

func LookupDeviceSMSIdentity(deviceID string) (SMSIdentity, bool, error) {
	return LookupSMSIdentityForDevice(SMSDeviceSelector{DeviceID: deviceID})
}

func LookupSMSIdentityForDevice(selector SMSDeviceSelector) (SMSIdentity, bool, error) {
	deviceID := strings.TrimSpace(selector.DeviceID)
	imei := strings.TrimSpace(selector.IMEI)
	if DB == nil || (deviceID == "" && imei == "") {
		return SMSIdentity{}, false, nil
	}

	query := DB.Where("alias = ? OR imei = ?", deviceID, deviceID)
	if imei != "" {
		query = DB.Where("imei = ?", imei)
	}
	var devices []Device
	if err := query.Select("iccid").Find(&devices).Error; err != nil {
		return SMSIdentity{}, false, err
	}
	identity, found, err := uniqueBoundSMSIdentity(devices)
	if err != nil || !found {
		return identity, found, err
	}

	var sim SIMCard
	err = DB.Where("iccid = ?", identity.ICCID).First(&sim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return identity, true, nil
	}
	if err != nil {
		return SMSIdentity{}, false, err
	}
	identity.IMSI = strings.TrimSpace(sim.IMSI)
	return identity, true, nil
}

func uniqueBoundSMSIdentity(devices []Device) (SMSIdentity, bool, error) {
	var resolved SMSIdentity
	for _, device := range devices {
		if device.CurrentICCID == nil {
			continue
		}
		iccid := CanonicalICCID(*device.CurrentICCID)
		if iccid == "" {
			continue
		}
		if resolved.ICCID != "" && resolved.ICCID != iccid {
			return SMSIdentity{}, false, fmt.Errorf("%w: device query matched multiple ICCID bindings", ErrSMSIdentityConflict)
		}
		resolved.ICCID = iccid
	}
	return resolved, resolved.ICCID != "", nil
}

func ResolveICCIDForIMSI(imsi string) (string, error) {
	imsi = strings.TrimSpace(imsi)
	if imsi == "" {
		return "", ErrSMSIdentityUnknown
	}
	if DB == nil {
		return "", ErrSMSIdentityUnknown
	}

	var rows []SIMCard
	if err := DB.Select("iccid").Where("imsi = ?", imsi).Find(&rows).Error; err != nil {
		return "", err
	}
	unique := make(map[string]struct{})
	for _, row := range rows {
		iccid := CanonicalICCID(row.ICCID)
		if iccid == "" || strings.HasPrefix(iccid, "reader-imsi-") {
			continue
		}
		unique[iccid] = struct{}{}
	}
	if len(unique) == 0 {
		return "imsi:" + imsi, nil
	}
	if len(unique) > 1 {
		return "", fmt.Errorf("%w: IMSI maps to %d ICCIDs", ErrSMSIdentityConflict, len(unique))
	}
	for iccid := range unique {
		return iccid, nil
	}
	return "", ErrSMSIdentityUnknown
}

func SaveSMSForIdentity(input SMSRecord) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	input.Identity.ICCID = CanonicalICCID(input.Identity.ICCID)
	input.Identity.IMSI = strings.TrimSpace(input.Identity.IMSI)
	if input.Identity.ICCID == "" || input.Identity.IMSI == "" {
		return ErrSMSIdentityUnknown
	}
	sender := strings.TrimSpace(input.Sender)
	recipient := strings.TrimSpace(input.Recipient)
	sms := SMS{
		IMSI: input.Identity.IMSI, ICCID: input.Identity.ICCID,
		Peer: normalizeSMSPeer(input.Type, sender, recipient),
		LocalPhone: normalizeSMSLocalPhoneForIdentity(smsLocalPhoneInput{
			Identity: input.Identity, Type: input.Type,
			LocalPhone: input.LocalPhone, Sender: sender, Recipient: recipient,
		}),
		Sender: sender, Recipient: recipient, Content: input.Content,
		Type: input.Type, Status: input.Status, Timestamp: input.Timestamp.Truncate(time.Second),
	}
	smsPublicationMu.Lock()
	defer smsPublicationMu.Unlock()
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := validateSMSIdentityMapping(tx, input.Identity); err != nil {
			return err
		}
		if err := tx.Create(&sms).Error; err != nil {
			return err
		}
		if sms.Peer == "" {
			return nil
		}
		return upsertSMSContactFromSMS(tx, &sms)
	})
	if err == nil {
		publishIncomingSMS(sms)
	}
	return err
}

func validateSMSIdentityMapping(tx *gorm.DB, identity SMSIdentity) error {
	var sim SIMCard
	err := tx.Select("imsi").Where("iccid = ?", identity.ICCID).First(&sim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	storedIMSI := strings.TrimSpace(sim.IMSI)
	if storedIMSI == "" || storedIMSI == identity.IMSI {
		return nil
	}
	known, err := imsiKnownForICCIDTx(tx, identity.ICCID, identity.IMSI)
	if err != nil {
		return err
	}
	if known {
		return nil
	}
	return fmt.Errorf("%w: stored ICCID and received IMSI do not match (stored %s, received %s)",
		ErrSMSIdentityConflict, imsiPrefix(storedIMSI), imsiPrefix(identity.IMSI))
}

// SIMCardHoldsIMSI reports whether sim_cards records imsi as this card's current
// IMSI. A multi-IMSI card can have both IMSIs in sim_subscriptions while
// sim_cards still holds the other one.
func SIMCardHoldsIMSI(iccid, imsi string) (bool, error) {
	if DB == nil {
		return false, nil
	}
	iccid = CanonicalICCID(iccid)
	imsi = strings.TrimSpace(imsi)
	if iccid == "" || imsi == "" {
		return false, nil
	}
	var sim SIMCard
	err := DB.Select("imsi").Where("iccid = ?", iccid).First(&sim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("查询卡的当前 IMSI 失败: %w", err)
	}
	return strings.TrimSpace(sim.IMSI) == imsi, nil
}

// IMSIKnownForICCID reports whether imsi was already observed live on this card.
// Multi-IMSI SIMs (DITO, Lebara UK) rewrite EF_IMSI without changing the ICCID,
// so sim_cards.imsi only holds the latest value; sim_subscriptions keeps every
// IMSI with the ICCID it was last read on. An IMSI never read on this card stays
// a conflict.
func IMSIKnownForICCID(iccid, imsi string) (bool, error) {
	if DB == nil {
		return false, nil
	}
	return imsiKnownForICCIDTx(DB, iccid, imsi)
}

func imsiKnownForICCIDTx(tx *gorm.DB, iccid, imsi string) (bool, error) {
	iccid = CanonicalICCID(iccid)
	imsi = strings.TrimSpace(imsi)
	if iccid == "" || imsi == "" {
		return false, nil
	}
	var count int64
	err := tx.Model(&SIMSubscription{}).
		Where("imsi = ? AND current_iccid IN ?", imsi, iccidLookupVariants(iccid)).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("查询 ICCID 已知 IMSI 失败: %w", err)
	}
	return count > 0, nil
}

// imsiPrefix keeps MCC+MNC for logs and errors; the subscriber part is never printed.
func imsiPrefix(imsi string) string {
	imsi = strings.TrimSpace(imsi)
	if len(imsi) <= 5 {
		return imsi
	}
	return imsi[:5] + "…"
}
