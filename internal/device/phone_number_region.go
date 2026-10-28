package device

import (
	"strings"

	"github.com/yibaiba/hideck/internal/phonelookup"
	"github.com/yibaiba/hideck/internal/upstreamproxy"
)

func (p *Pool) PhoneNumberRegion(deviceID string) string {
	if p == nil {
		return ""
	}
	return phoneNumberRegionForWorker(p.GetWorker(strings.TrimSpace(deviceID)))
}

func phoneNumberRegionForWorker(worker *Worker) string {
	if worker == nil {
		return ""
	}
	status := worker.GetCachedDeviceStatus()
	mcc := strings.TrimSpace(status.NativeMCC)
	if mcc == "" {
		imsi := strings.TrimSpace(status.IMSI)
		if len(imsi) >= 3 {
			mcc = imsi[:3]
		}
	}
	return phoneNumberRegionForMCC(mcc)
}

// PhoneNumberRegionForIMSI returns the SIM home region captured with an event.
func PhoneNumberRegionForIMSI(imsi string) string {
	imsi = strings.TrimSpace(imsi)
	if len(imsi) < 3 {
		return ""
	}
	return phoneNumberRegionForMCC(imsi[:3])
}

func phoneNumberRegionForMCC(mcc string) string {
	if region, ok := upstreamproxy.CountryCodeFromHomeMCC(mcc); ok {
		return region
	}
	return phonelookup.RegionForMCC(mcc)
}
