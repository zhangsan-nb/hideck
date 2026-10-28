package volte

const (
	PhaseIdle        = "idle"
	PhaseEnabling    = "enabling"
	PhaseRegistering = "registering"
	PhaseRegistered  = "registered"
	PhaseUnverified  = "ims_enabled_unverified"
	PhaseFailed      = "failed"
)

type Status struct {
	DeviceID              string `json:"device_id"`
	Phase                 string `json:"phase"`
	IMSEnabled            bool   `json:"ims_enabled"`
	VoLTEEnabled          bool   `json:"volte_enabled"`
	IMSRegistered         bool   `json:"ims_registered"`
	VoiceIndicationsReady bool   `json:"voice_indications_ready"`
	VoiceAvailable        bool   `json:"voice_available"`
	UACEnabled            bool   `json:"uac_enabled"`
	UACUnusable           bool   `json:"uac_unusable,omitempty"`
	AudioDevice           string `json:"audio_device,omitempty"`
	QPCMVFailed           bool   `json:"qpcmv_failed,omitempty"`
	RebootRequired        bool   `json:"reboot_required"`
	QMIIMSUnavailable     bool   `json:"qmi_ims_unavailable"`
	ProvisionStage        string `json:"provision_stage,omitempty"`
	IMEITail              string `json:"imei_tail,omitempty"`
	PLMN                  string `json:"plmn,omitempty"`
	MBNName               string `json:"mbn_name,omitempty"`
	LTERegistered         bool   `json:"lte_registered"`
	IMSPDNActive          bool   `json:"ims_pdn_active"`
	LastError             string `json:"last_error,omitempty"`
}

func (s Status) Ready() bool {
	return s.IMSRegistered && s.VoiceIndicationsReady && s.Phase == PhaseRegistered
}
