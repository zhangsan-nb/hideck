package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/modem"
	"github.com/yibaiba/hideck/pkg/logger"
)

func (p *Pool) buildVoWiFiStartProfile(ctx context.Context, worker *Worker, traceID string) (identity.Profile, error) {
	if worker == nil {
		return identity.Profile{}, fmt.Errorf("worker_nil")
	}
	if worker.Backend == nil {
		return identity.Profile{}, fmt.Errorf("backend_not_available")
	}

	// Persisting also reads the modem (MSISDN, no deadline); do it after the
	// ownership lock is released. The start itself, and a Disable queued behind
	// it, still wait for that read.
	persistIdentity := false
	defer func() {
		if persistIdentity {
			p.PersistIdentityState(worker)
		}
	}()
	owner, releaseOwnership, err := p.lockVoWiFiStartOwnership(ctx, worker, traceID)
	if err != nil {
		return identity.Profile{}, err
	}
	defer releaseOwnership()
	liveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	iccid, imsi, err := readVoWiFiStartIdentity(liveCtx, worker)
	if err != nil {
		return identity.Profile{}, err
	}
	persistIdentity, err = syncVoWiFiStartIdentity(worker, iccid, imsi, traceID)
	if err != nil {
		return identity.Profile{}, err
	}
	if !persistIdentity && db.DB != nil {
		// The cache can already match the card while the database does not: an
		// earlier write failed, or a status refresh updated only the cache after
		// a drift wait recorded the roaming IMSI.
		held, heldErr := db.SIMCardHoldsIMSI(iccid, imsi)
		if heldErr != nil {
			logger.Warn("查询卡的当前 IMSI 失败", "trace_id", traceID, "device", worker.ID, "err", heldErr)
		}
		persistIdentity = heldErr == nil && !held
	}
	// Before the SMSC read and every later step: a drifted card waits here on
	// both the WiFi and cellular start paths.
	if err := checkMultiIMSIDrift(worker.ID, iccid, imsi, traceID, time.Now()); err != nil {
		return identity.Profile{}, err
	}

	status := worker.ProjectDeviceStatus()
	mcc, mnc, plmnSource := resolveVoWiFiProfileMCCMNC(liveCtx, worker, status, imsi, traceID)
	if mcc == "" || mnc == "" {
		return identity.Profile{}, fmt.Errorf("缺少 SIM 归属 MCC/MNC，无法构建 VoWiFi 启动画像: %s", imsi)
	}

	imei := strings.TrimSpace(status.IMEI)

	smsc := p.resolveVoWiFiSMSC(worker, imsi, iccid, traceID)
	if err := p.validatePolicyOwner(owner, p.policyTransitionFor(worker.ID)); err != nil {
		return identity.Profile{}, fmt.Errorf("VoWiFi 启动期间设备或 SIM 已更换: %w", err)
	}

	logger.Info("VoWiFi 启动画像将基于实时 IMSI 构建",
		"trace_id", traceID,
		"device", worker.ID,
		"source", "live_imsi",
		"plmn_source", plmnSource,
		"native_mcc", strings.TrimSpace(status.NativeMCC),
		"native_mnc", strings.TrimSpace(status.NativeMNC),
		"iccid", iccid,
		"imsi", imsi,
		"mcc", mcc,
		"mnc", mnc,
		"imei", imei)

	return buildVoWiFiRawProfile(identity.Profile{
		ICCID: iccid, IMSI: imsi, MCC: mcc, MNC: mnc, IMEI: imei, SMSC: smsc,
	}), nil
}

func (p *Pool) lockVoWiFiStartOwnership(ctx context.Context, worker *Worker, traceID string) (policyOwner, func(), error) {
	transition := p.policyTransitionFor(worker.ID)
	if err := lockTransitionForVoWiFiStart(ctx, worker.ID, traceID, transition); err != nil {
		return policyOwner{}, nil, err
	}
	if p.GetWorker(worker.ID) != worker {
		transition.Unlock()
		return policyOwner{}, nil, fmt.Errorf("VoWiFi 启动设备已被移除或替换")
	}
	owner := capturePolicyOwner(worker, transition)
	if err := p.validatePolicyOwner(owner, transition); err != nil {
		transition.Unlock()
		return policyOwner{}, nil, fmt.Errorf("VoWiFi 启动设备或 SIM 状态已失效: %w", err)
	}
	return owner, transition.Unlock, nil
}

var errVoWiFiStartSuperseded = errors.New("VoWiFi 启动等待卡策略锁期间已被停用或抢占")

// lockTransitionForVoWiFiStart gives up when the start is cancelled while it
// waits. Card policy projection and eSIM switching hold this lock while they
// stop software IMS and wait for the running start to drain; a plain Lock here
// deadlocked both. Disable and a preempting lifecycle
// command both cancel the start's context before they wait.
// Polls every 20ms; a lock with a wake channel would avoid it if this shows up in profiles.
func lockTransitionForVoWiFiStart(ctx context.Context, deviceID, traceID string, transition *policyTransition) error {
	waitStart := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			logger.Warn("VoWiFi 启动等待卡策略锁时被取消，放弃本次启动",
				"trace_id", traceID, "device", deviceID, "waited", time.Since(waitStart).Round(time.Millisecond).String(), "err", err)
			return fmt.Errorf("%w: %w", errVoWiFiStartSuperseded, err)
		}
		if transition.TryLock() {
			// A cancel that lands while the lock is taken must not run the start.
			if ctx.Err() == nil {
				return nil
			}
			transition.Unlock()
			continue
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readVoWiFiStartIdentity(ctx context.Context, worker *Worker) (string, string, error) {
	if reader, ok := worker.Backend.(liveSIMIdentitySnapshotReader); ok {
		snapshot, err := reader.ReadSIMIdentityLive(ctx)
		if err != nil {
			return "", "", fmt.Errorf("实时读取 SIM 身份快照失败: %w", err)
		}
		return validateVoWiFiStartIdentity(snapshot.ICCID, snapshot.IMSI)
	}
	reader, ok := worker.Backend.(liveSIMIdentityReader)
	if !ok {
		return "", "", fmt.Errorf("live_identity_not_supported")
	}
	imsi, err := reader.GetIMSILive(ctx)
	if err != nil {
		return "", "", fmt.Errorf("实时读取 IMSI 失败: %w", err)
	}
	iccid, err := reader.GetICCIDLive(ctx)
	if err != nil {
		return "", "", fmt.Errorf("实时读取 ICCID 失败: %w", err)
	}
	return validateVoWiFiStartIdentity(iccid, imsi)
}

// syncVoWiFiStartIdentity makes the profile and the worker cache share the identity
// read under the start ownership lock; it reports whether the caller must persist
// it. Without this a multi-IMSI SIM could register IMS with one IMSI while
// sim_cards still holds the other, and inbound SMS were rejected as a conflict.
func syncVoWiFiStartIdentity(worker *Worker, iccid, imsi, traceID string) (bool, error) {
	worker.cacheMu.Lock()
	cachedICCID := strings.TrimSpace(worker.state.Identity.ICCID)
	cachedIMSI := strings.TrimSpace(worker.state.Identity.IMSI)
	if cachedICCID != "" && normalizeSIMIdentityForCompare(cachedICCID) != normalizeSIMIdentityForCompare(iccid) {
		worker.cacheMu.Unlock()
		logger.Warn("VoWiFi 启动实时 ICCID 与缓存不一致，放弃本次启动",
			"trace_id", traceID, "device", worker.ID)
		return false, fmt.Errorf("VoWiFi 启动期间 SIM 已更换")
	}
	// An empty cache is filled by the identity refresh; writing the ICCID here
	// would invalidate the policy owner captured for this start.
	if cachedICCID == "" || cachedIMSI == imsi {
		worker.cacheMu.Unlock()
		return false, nil
	}
	worker.state.Identity.IMSI = imsi
	// Home MCC/MNC were derived from the old IMSI.
	worker.state.Identity.NativeMCC = ""
	worker.state.Identity.NativeMNC = ""
	worker.cacheMu.Unlock()
	logger.Warn("VoWiFi 启动实时 IMSI 与缓存不一致，按实时值更新",
		"trace_id", traceID, "device", worker.ID,
		"cached_imsi", maskIMSIPrefix(cachedIMSI), "live_imsi", maskIMSIPrefix(imsi))
	return true, nil
}

func validateVoWiFiStartIdentity(iccid, imsi string) (string, string, error) {
	imsi = strings.TrimSpace(imsi)
	if imsi == "" {
		return "", "", fmt.Errorf("实时 IMSI 为空")
	}
	iccid = strings.TrimSpace(iccid)
	if iccid == "" {
		return "", "", fmt.Errorf("实时 ICCID 为空")
	}
	return iccid, imsi, nil
}

func (p *Pool) resolveVoWiFiSMSC(worker *Worker, imsi, iccid, traceID string) string {
	if worker == nil {
		return ""
	}
	cacheKey := strings.TrimSpace(imsi)
	if cacheKey == "" {
		cacheKey = strings.TrimSpace(iccid)
	}
	read := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, err := worker.getSMSCWithContext(ctx)
		return strings.TrimSpace(value), err
	}
	smsc, err := read()
	if smsc == "" {
		smsc, err = read()
	}
	if smsc != "" {
		if p != nil && cacheKey != "" {
			p.smscCacheMu.Lock()
			if p.smscCache == nil {
				p.smscCache = make(map[string]string)
			}
			p.smscCache[cacheKey] = smsc
			p.smscCacheMu.Unlock()
		}
		logger.Info("VoWiFi 启动前获取 SMSC 成功", "trace_id", traceID, "device", worker.ID)
		return smsc
	}
	if p != nil && cacheKey != "" {
		p.smscCacheMu.RLock()
		cached := p.smscCache[cacheKey]
		p.smscCacheMu.RUnlock()
		if cached != "" {
			logger.Warn("VoWiFi 启动前读取 SMSC 失败，使用缓存", "trace_id", traceID, "device", worker.ID, "err", err)
			return cached
		}
	}
	if err != nil {
		logger.Warn("VoWiFi 启动前获取 SMSC 失败，将以空 SMSC 继续启动", "trace_id", traceID, "device", worker.ID, "err", err)
	} else {
		logger.Warn("VoWiFi 启动前未获取到 SMSC，将以空 SMSC 继续启动", "trace_id", traceID, "device", worker.ID)
	}
	return ""
}

func buildVoWiFiRawProfile(profile identity.Profile) identity.Profile {
	return identity.Profile{
		ICCID:     strings.TrimSpace(profile.ICCID),
		IMSI:      strings.TrimSpace(profile.IMSI),
		MCC:       strings.TrimSpace(profile.MCC),
		MNC:       strings.TrimSpace(profile.MNC),
		IMEI:      strings.TrimSpace(profile.IMEI),
		UserAgent: strings.TrimSpace(profile.UserAgent),
		SMSC:      strings.TrimSpace(profile.SMSC),
		IMSDomain: strings.TrimSpace(profile.IMSDomain),
	}
}

func resolveVoWiFiProfileMCCMNC(ctx context.Context, worker *Worker, status modem.DeviceStatus, imsi, traceID string) (mcc, mnc, source string) {
	imsi = strings.TrimSpace(imsi)
	if worker != nil && worker.Backend != nil {
		if liveMCC, liveMNC, err := worker.Backend.GetNativeMCCMNC(ctx); err == nil {
			liveMCC = strings.TrimSpace(liveMCC)
			liveMNC = strings.TrimSpace(liveMNC)
			// The backend derives MCC/MNC from its snapshot IMSI, which can lag a
			// multi-IMSI SIM; re-parse the live IMSI instead of trusting it.
			if liveMCC != "" && liveMNC != "" && imsi != "" && !strings.HasPrefix(imsi, liveMCC+liveMNC) {
				parsedMCC, parsedMNC, _, _, parseErr := modem.HomeMCCMNCFromIMSIAndEFAD(imsi, nil)
				logger.Warn("VoWiFi 启动 SIM 归属 MCC/MNC 与实时 IMSI 不一致，按实时 IMSI 重算",
					"trace_id", traceID, "device", worker.ID,
					"backend_plmn", liveMCC+liveMNC, "live_imsi", maskIMSIPrefix(imsi), "err", parseErr)
				liveMCC, liveMNC = parsedMCC, parsedMNC
			}
			if liveMCC != "" && liveMNC != "" {
				cacheVoWiFiProfileMCCMNC(worker, liveMCC, liveMNC)
				return liveMCC, liveMNC, "sim_home"
			}
		} else {
			logger.Debug("VoWiFi 启动前读取 SIM 归属 MCC/MNC 失败，将回退已缓存 SIM 归属信息",
				"trace_id", traceID, "device", worker.ID, "err", err)
		}
	}

	statusIMSI := strings.TrimSpace(status.IMSI)
	if statusIMSI == imsi && strings.TrimSpace(status.NativeMCC) != "" && strings.TrimSpace(status.NativeMNC) != "" {
		return strings.TrimSpace(status.NativeMCC), strings.TrimSpace(status.NativeMNC), "sim_home_cache"
	}

	return vowifiProfileMCCMNC(status)
}

func cacheVoWiFiProfileMCCMNC(worker *Worker, mcc, mnc string) {
	if worker == nil || strings.TrimSpace(mcc) == "" || strings.TrimSpace(mnc) == "" {
		return
	}
	worker.cacheMu.Lock()
	worker.state.Identity.NativeMCC = strings.TrimSpace(mcc)
	worker.state.Identity.NativeMNC = strings.TrimSpace(mnc)
	worker.cacheMu.Unlock()
}

func vowifiProfileMCCMNC(status modem.DeviceStatus) (mcc, mnc, source string) {
	mcc = strings.TrimSpace(status.NativeMCC)
	mnc = strings.TrimSpace(status.NativeMNC)
	if mcc != "" && mnc != "" {
		return mcc, mnc, "sim_home_cache"
	}
	return "", "", ""
}

func newVoWiFiSIMReadyStartupState(deviceID, dataplaneMode, networkMode string, now time.Time) runtimehost.State {
	return runtimehost.State{
		Phase:         runtimehost.PhaseSIMReady,
		DeviceID:      deviceID,
		DataplaneMode: dataplaneMode,
		NetworkMode:   strings.TrimSpace(networkMode),
		SIMReady:      true,
		LastReason:    "sim_ready",
		UpdatedAt:     now,
	}
}

func (p *Pool) BuildVoWiFiStartProfile(worker *Worker) (identity.Profile, error) {
	return p.buildVoWiFiStartProfile(context.Background(), worker, "")
}
