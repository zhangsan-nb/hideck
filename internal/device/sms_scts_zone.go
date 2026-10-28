package device

import (
	"strings"
	"time"

	"github.com/yibaiba/hideck/pkg/logger"
)

// smscLocalTimeZones lists home PLMNs whose SMSC writes local wall time into
// TP-SCTS with a zero timezone field (TS 23.040 reads zero as UTC). DITO 515-66:
// SCTS 17:26:32+00:00 on an SMS received at 17:26:34+08:00. SCTS comes from the
// SMSC that handled the message, which may not be DITO's, so a zero zone is only
// re-read as local time when that lands near the receive time; a real UTC stamp
// or an old backlog stays as received.
var smscLocalTimeZones = map[string]*time.Location{
	"51566": time.FixedZone("PHT", 8*60*60),
}

const smscLocalTimeTolerance = time.Hour

func correctSMSCTimestamp(deviceID, imsi string, ts, received time.Time) time.Time {
	imsi = strings.TrimSpace(imsi)
	if ts.IsZero() || len(imsi) < 5 {
		return ts
	}
	loc, ok := smscLocalTimeZones[imsi[:5]]
	if !ok {
		return ts
	}
	if _, offset := ts.Zone(); offset != 0 {
		return ts
	}
	year, month, day := ts.Date()
	hour, minute, second := ts.Clock()
	corrected := time.Date(year, month, day, hour, minute, second, ts.Nanosecond(), loc)
	if gap := received.Sub(corrected); gap > smscLocalTimeTolerance || gap < -smscLocalTimeTolerance {
		logger.Info("短信中心时间戳时区为 0，按本地时区解读与收到时间不符，保留原值",
			"device", deviceID, "plmn", imsi[:5],
			"scts", ts.Format(time.RFC3339), "received", received.Format(time.RFC3339))
		return ts
	}
	logger.Info("短信中心时间戳时区为 0，按运营商本地时区更正",
		"device", deviceID, "plmn", imsi[:5],
		"scts", ts.Format(time.RFC3339), "corrected", corrected.Format(time.RFC3339))
	return corrected
}
