package volte

import (
	"context"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

type Registration struct {
	Registered     bool
	VoiceAvailable bool
	Raw            string
}

// DTMFTransport is bound to one QMI owner, never re-resolved by device ID
// between START, STOP or the unsupported-command fallback.
type DTMFTransport interface {
	VOICEBurstDTMF(context.Context, uint8, string) (uint8, error)
	VOICEStartContinuousDTMF(context.Context, uint8, uint8) (uint8, error)
	VOICEStopContinuousDTMF(context.Context, uint8) (uint8, error)
}

type Host interface {
	BindVoiceDTMF(deviceID string) (DTMFTransport, error)
	ExecuteAT(deviceID, cmd string, timeout time.Duration) (string, error)
	StopSoftwareIMS(deviceID string) error
	SetNativeIMS(ctx context.Context, deviceID string, enabled bool) error
	EnsureIMSClients(ctx context.Context, deviceID string) error
	EnsureVoiceIndications(ctx context.Context, deviceID string) error
	ReleaseIMSClients(deviceID string) error
	SubscribeIMSRegistration(deviceID string, handler func(*qmi.IMSARegistrationStatus)) (func(), error)
	SubscribeIMSServices(deviceID string, handler func(*qmi.IMSAServicesStatus)) (func(), error)
	IMSAStatus(ctx context.Context, deviceID string) (Registration, error)
	AudioDevice(deviceID string) string
	VOICEDial(ctx context.Context, deviceID, number string) (uint8, error)
	VOICEAnswer(ctx context.Context, deviceID string, callID uint8) error
	VOICEHangup(ctx context.Context, deviceID string, callID uint8) error
	VOICEManageCalls(ctx context.Context, deviceID string, req qmi.VoiceManageCallsRequest) error
	VOICEBurstDTMF(ctx context.Context, deviceID string, callID uint8, digits string) error // digits is a single DTMF key
	VOICEStartContinuousDTMF(ctx context.Context, deviceID string, callID, digit uint8) error
	VOICEStopContinuousDTMF(ctx context.Context, deviceID string, callID uint8) error
	VOICEGetAllCallInfo(ctx context.Context, deviceID string) (*qmi.VoiceAllCallInfo, error)
	SubscribeVoiceStatus(deviceID string, handler func(*qmi.VoiceAllCallInfo)) (func(), error)
}
