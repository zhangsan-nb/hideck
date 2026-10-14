package device

import (
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/eventhost"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/smsnotify"
)

const vowifiReceivedSMSDuplicateWindow = 30 * time.Minute

type vowifiSMSHistoryRecorder struct {
	pool *Pool
}

type vowifiSMSRecordResult struct {
	Stored     bool
	Duplicate  bool
	Suppressed bool
	Reconciled bool
}

type receivedMultipartContext struct {
	Identity   SMSIdentity
	LocalPhone string
	Timestamp  time.Time
}

func RecordVoWiFiSMSSendFailure(p *Pool, deviceID, target, content string, at time.Time) error {
	return vowifiSMSHistoryRecorder{pool: p}.RecordSendFailure(deviceID, target, content, at)
}

func (r vowifiSMSHistoryRecorder) resolveIMSI(devID, fallbackIMSI string) string {
	imsi := strings.TrimSpace(fallbackIMSI)
	if imsi != "" {
		return imsi
	}
	if r.pool == nil {
		return ""
	}
	w := r.pool.GetWorker(strings.TrimSpace(devID))
	if w == nil {
		return ""
	}
	if cached := strings.TrimSpace(w.GetCachedIMSI()); cached != "" {
		return cached
	}
	return strings.TrimSpace(w.GetIMSI())
}

func (r vowifiSMSHistoryRecorder) resolveICCID(devID string) string {
	if r.pool == nil {
		return ""
	}
	w := r.pool.GetWorker(strings.TrimSpace(devID))
	if w == nil {
		return ""
	}
	if iccid := strings.TrimSpace(w.GetCachedDeviceStatus().ICCID); iccid != "" {
		return iccid
	}
	iccid, err := db.ResolveICCIDForIMSI(r.resolveIMSI(devID, ""))
	if err != nil || strings.HasPrefix(iccid, "imsi:") {
		return ""
	}
	return strings.TrimSpace(iccid)
}

func (r vowifiSMSHistoryRecorder) resolveReceivingIdentity(devID string) SMSIdentity {
	return normalizeSMSIdentity(SMSIdentity{
		ICCID: r.resolveICCID(devID),
		IMSI:  r.resolveIMSI(devID, ""),
	})
}

func (r vowifiSMSHistoryRecorder) localPhone(imsi string) string {
	phone, _ := db.GetSIMCardPhoneNumberByIMSI(strings.TrimSpace(imsi))
	return strings.TrimSpace(phone)
}

func (r vowifiSMSHistoryRecorder) eventTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

func (r vowifiSMSHistoryRecorder) RecordReceived(e eventhost.SMSReceived) (vowifiSMSRecordResult, error) {
	return r.recordReceived(e, r.resolveReceivingIdentity(e.DevID))
}

func (r vowifiSMSHistoryRecorder) recordReceived(
	e eventhost.SMSReceived,
	identity SMSIdentity,
) (vowifiSMSRecordResult, error) {
	if smsnotify.ShouldSuppressReceivedSMS(e.Content) {
		return vowifiSMSRecordResult{Suppressed: true}, nil
	}
	identity = normalizeSMSIdentity(identity)
	if identity.IMSI == "" {
		return vowifiSMSRecordResult{}, nil
	}
	if identity.ICCID == "" {
		identity.ICCID = db.GetICCIDForIMSI(identity.IMSI)
	}
	localPhone := r.localPhone(identity.IMSI)
	ts := r.eventTime(e.Time)
	if strings.TrimSpace(e.FragmentSessionKey) != "" {
		return r.recordReceivedMultipart(e, receivedMultipartContext{
			Identity: identity, LocalPhone: localPhone, Timestamp: ts,
		})
	}

	dup, err := db.HasDuplicateReceivedSMSForIdentity(db.ReceivedSMSDuplicate{
		Identity:   db.SMSIdentity{ICCID: identity.ICCID, IMSI: identity.IMSI},
		LocalPhone: localPhone, Sender: e.Sender, Recipient: localPhone, Content: e.Content,
		Timestamp: ts, Window: vowifiReceivedSMSDuplicateWindow,
	})
	if err != nil {
		return vowifiSMSRecordResult{}, err
	}
	if dup {
		return vowifiSMSRecordResult{Duplicate: true}, nil
	}

	err = db.SaveSMSForIdentity(db.SMSRecord{
		Identity:   db.SMSIdentity{ICCID: identity.ICCID, IMSI: identity.IMSI},
		LocalPhone: localPhone, Sender: strings.TrimSpace(e.Sender), Recipient: localPhone,
		Content: e.Content, Type: 1, Status: 0, Timestamp: ts,
	})
	if err != nil {
		return vowifiSMSRecordResult{}, err
	}
	return vowifiSMSRecordResult{Stored: true}, nil
}

func (r vowifiSMSHistoryRecorder) recordReceivedMultipart(
	e eventhost.SMSReceived,
	ctx receivedMultipartContext,
) (vowifiSMSRecordResult, error) {
	result, err := db.SaveReceivedMultipartSMS(db.ReceivedMultipartSMS{
		ICCID: ctx.Identity.ICCID, IMSI: ctx.Identity.IMSI,
		LocalPhone: ctx.LocalPhone, Sender: strings.TrimSpace(e.Sender),
		Recipient: ctx.LocalPhone, Content: e.Content, Timestamp: ctx.Timestamp,
		FragmentSessionKey: strings.TrimSpace(e.FragmentSessionKey),
		Incomplete:         e.Incomplete,
	})
	if err != nil {
		return vowifiSMSRecordResult{}, err
	}
	return vowifiSMSRecordResult{
		Stored:    result.Created || result.Reconciled,
		Duplicate: result.Duplicate, Reconciled: result.Reconciled,
	}, nil
}

func (r vowifiSMSHistoryRecorder) RecordSent(e eventhost.SMSSent) error {
	imsi := r.resolveIMSI(e.DevID, "")
	if imsi == "" {
		return nil
	}
	localPhone := r.localPhone(imsi)
	return db.SaveSMSWithLocalPhone(imsi, localPhone, localPhone, strings.TrimSpace(e.TargetURI), e.Content, 2, 2, r.eventTime(e.Time))
}

func (r vowifiSMSHistoryRecorder) RecordSendFailure(devID, target, content string, at time.Time) error {
	imsi := r.resolveIMSI(devID, "")
	if imsi == "" {
		return nil
	}
	localPhone := r.localPhone(imsi)
	return db.SaveSMSWithLocalPhone(imsi, localPhone, localPhone, strings.TrimSpace(target), content, 2, 3, r.eventTime(at))
}

func (r vowifiSMSHistoryRecorder) RecordLocalNumberLearned(e eventhost.LocalNumberLearned) error {
	imsi := r.resolveIMSI(e.DevID, e.IMSI)
	number := strings.TrimSpace(e.Number)
	if number == "" {
		return nil
	}
	iccid := r.resolveICCID(e.DevID)
	if imsi == "" && iccid == "" {
		return nil
	}
	return db.RecordVoWiFiPhoneNumber(imsi, iccid, number)
}
