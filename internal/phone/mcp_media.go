package phone

import (
	"errors"
	"strings"

	"github.com/pion/webrtc/v4"
)

// ValidateBrowserMedia checks the actual WebRTC session and the same ownership
// credentials used by call control. It does not grant ownership or take over calls.
func (s *Service) ValidateBrowserMedia(owner, mediaID, lease string) error {
	if s == nil || s.media == nil {
		return errors.New("phone: media service unavailable")
	}
	media := s.media.Get(mediaID)
	if media == nil || !media.Matches(owner, strings.TrimSpace(lease)) {
		return errors.New("phone: invalid media control lease")
	}
	if media.peer == nil || media.peer.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return errors.New("phone: browser audio is not connected")
	}
	return nil
}
