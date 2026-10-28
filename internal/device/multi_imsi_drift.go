package device

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/pkg/logger"
)

// Multi-IMSI SIMs (DITO, Lebara UK) rewrite EF_IMSI to a sponsored roaming IMSI
// when they see a foreign network. VoWiFi must use the home IMSI; with the roaming
// one HiDeck would pick the sponsor's preset and the wrong ePDG. DITO went back to
// its home IMSI on its own within 1-3 minutes while in airplane mode, so the start
// is refused and the desired-state retry (30s, 1m, then every 2m) waits for it.
// Nothing cycles the profile automatically: on an eSTK card that could enable a
// different profile, so the user handles a drift that does not clear.
var roamingSponsorIMSIPrefixes = []string{"20404"}

// multiIMSIHomePLMNs are home PLMNs confirmed to drift to a sponsor IMSI. Lebara UK
// (234-87) keeps its own handling in lebara_uk.go.
var multiIMSIHomePLMNs = []string{"51566"}

const (
	multiIMSIDriftWarnAfter = 4 * time.Minute
	// A drift not seen for this long is a new one: the device stopped retrying
	// (VoWiFi disabled, worker removed) without ever reading the home IMSI.
	multiIMSIDriftForgetAfter = 10 * time.Minute
)

var ErrMultiIMSIDrift = errors.New("多 IMSI 卡当前是漫游 IMSI，暂不启动 VoWiFi")

type multiIMSIDriftState struct {
	iccid    string
	since    time.Time
	lastSeen time.Time
	warned   bool
}

var (
	multiIMSIDriftMu sync.Mutex
	multiIMSIDrifts  = map[string]*multiIMSIDriftState{} // device ID
	// listIMSIsForICCID is swapped in tests.
	listIMSIsForICCID = db.ListIMSIsForICCID
)

func isRoamingSponsorIMSI(imsi string) bool {
	for _, prefix := range roamingSponsorIMSIPrefixes {
		if strings.HasPrefix(imsi, prefix) {
			return true
		}
	}
	return false
}

// multiIMSIHomePrefix returns the confirmed home PLMN this card was read with
// before, when the live IMSI is a sponsored roaming IMSI. A card only ever seen
// with the roaming IMSI, or with an unlisted home PLMN, is not treated as drifted.
func multiIMSIHomePrefix(iccid, imsi string, known func(string) ([]string, error)) (string, bool, error) {
	imsi = strings.TrimSpace(imsi)
	if len(imsi) < 5 || !isRoamingSponsorIMSI(imsi) {
		return "", false, nil
	}
	imsis, err := known(iccid)
	if err != nil {
		return "", false, err
	}
	for _, other := range imsis {
		other = strings.TrimSpace(other)
		for _, home := range multiIMSIHomePLMNs {
			if strings.HasPrefix(other, home) {
				return home, true, nil
			}
		}
	}
	return "", false, nil
}

// checkMultiIMSIDrift refuses the software IMS start while the card sits on its
// roaming IMSI.
func checkMultiIMSIDrift(deviceID, iccid, imsi, traceID string, now time.Time) error {
	home, drifted, err := multiIMSIHomePrefix(iccid, imsi, listIMSIsForICCID)
	if err != nil {
		// Only a sponsor IMSI reaches the history query; starting on it is what
		// this check prevents, so wait for the next retry instead.
		logger.Warn("读取卡的历史 IMSI 失败，暂不启动 VoWiFi",
			"trace_id", traceID, "device", deviceID, "live_imsi", maskIMSIPrefix(imsi), "err", err)
		return fmt.Errorf("%w（读取历史 IMSI 失败: %v）", ErrMultiIMSIDrift, err)
	}
	multiIMSIDriftMu.Lock()
	defer multiIMSIDriftMu.Unlock()
	state := multiIMSIDrifts[deviceID]
	if !drifted {
		if state != nil {
			delete(multiIMSIDrifts, deviceID)
			logger.Info("多 IMSI 卡已恢复本国 IMSI",
				"trace_id", traceID, "device", deviceID, "live_imsi", maskIMSIPrefix(imsi),
				"waited", now.Sub(state.since).Round(time.Second).String())
		}
		return nil
	}
	if state == nil || state.iccid != iccid || now.Sub(state.lastSeen) > multiIMSIDriftForgetAfter {
		// Another card in the same modem, or a drift long after the last one:
		// restart the wait instead of inheriting it.
		state = &multiIMSIDriftState{iccid: iccid, since: now}
		multiIMSIDrifts[deviceID] = state
	}
	state.lastSeen = now
	waited := now.Sub(state.since).Round(time.Second)
	logger.Warn("多 IMSI 卡处于漫游 IMSI，暂不启动 VoWiFi，等待恢复本国 IMSI",
		"trace_id", traceID, "device", deviceID,
		"live_imsi", maskIMSIPrefix(imsi), "home_imsi", home, "waited", waited.String())
	if waited >= multiIMSIDriftWarnAfter && !state.warned {
		state.warned = true
		logger.Error("多 IMSI 卡超过 4 分钟仍未恢复本国 IMSI，请手动处理：保持飞行，停用再启用该 Profile",
			"device", deviceID, "live_imsi", maskIMSIPrefix(imsi), "home_imsi", home)
	}
	return fmt.Errorf("%w（当前 %s，本国 %s，已等待 %s）", ErrMultiIMSIDrift, maskIMSIPrefix(imsi), home, waited)
}
