package imscore

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

func TestSubscriptionTimerNStartsAfterQueueAndOnlyClosesUsage(t *testing.T) {
	for _, preset := range []string{vodafoneUKCarrierPresetID, "2degrees_nz_53024", "CTEUK_23433"} {
		for _, mwi := range []bool{false, true} {
			s := newSubscriptionLifecycleTestServiceForPreset(t, nil, preset)
			result := beginProtocolSubscription(t, s, mwi)
			f := s.subscriptionFieldsLocked(mwi)
			if got := f.lifecycle.notifyDeadline.Sub(f.lifecycle.sentAt); got != 64*s.transport.transactionTimers().t1 {
				t.Fatalf("Timer N = %s", got)
			}
			completeProtocolSubscription(t, s, mwi, result)
			connection := s.registrationTCP
			s.mu.Lock()
			s.expireSubscriptionTimersLocked(f.lifecycle.notifyDeadline)
			s.mu.Unlock()
			if !*f.closed || f.dialog.ready() || *f.lastErr == "" || f.lifecycle.retryAt.IsZero() {
				t.Fatal("Timer N did not terminate and diagnose the subscription")
			}
			if s.RegState() != regRegistered || s.registrationTCP != connection || s.pcscfRecoveryPending.Load() {
				t.Fatal("subscription timeout disturbed the IMS registration")
			}
			select {
			case err := <-s.RegistrationErrors():
				t.Fatalf("Timer N requested a runtime rebuild: %v", err)
			default:
			}
			// A queued attempt must not count queueing time towards Timer N.
			f.lifecycle.retryAt = time.Time{}
			if err := s.recordSubscriptionUsageAttempt(result, mwi); !errors.Is(err, errSubscriptionUsageChanged) {
				t.Fatalf("expired usage reused after Timer N: %v", err)
			}
			queued := buildProtocolSubscription(t, s, mwi)
			if err := s.recordSubscriptionUsageAttempt(queued, mwi); err != nil {
				t.Fatal(err)
			}
			if !f.lifecycle.notifyDeadline.IsZero() || !f.lifecycle.sentAt.IsZero() {
				t.Fatal("Timer N started before the queued request was sent")
			}
		}
	}
}

func TestSubscriptionTimerNOnRefreshIsCancelledByNotify(t *testing.T) {
	for _, mwi := range []bool{false, true} {
		s := newSubscriptionLifecycleTestService(t, nil)
		initial := beginProtocolSubscription(t, s, mwi)
		completeProtocolSubscription(t, s, mwi, initial)
		_, _ = s.acceptSubscriptionNotification(notifyForSubscription(initial.request, "active;expires=120", 1))
		refresh := beginProtocolSubscription(t, s, mwi)
		f := s.subscriptionFieldsLocked(mwi)
		if f.lifecycle.notifyDeadline.IsZero() {
			t.Fatal("refresh did not start Timer N")
		}
		completeProtocolSubscription(t, s, mwi, refresh)
		_, status := s.acceptSubscriptionNotification(notifyForSubscription(refresh.request, "pending;expires=90", 2))
		if status != 200 || !f.lifecycle.notifyDeadline.IsZero() || *f.expires != 90*time.Second {
			t.Fatal("matching refresh NOTIFY did not cancel Timer N")
		}
	}
}

func TestSubscriptionTerminationReasonsAndLate200(t *testing.T) {
	for _, mwi := range []bool{false, true} {
		for _, reason := range []string{"deactivated", "timeout", "probation", "giveup", "rejected", "noresource", "invariant", "other"} {
			s := newSubscriptionLifecycleTestService(t, nil)
			result := beginProtocolSubscription(t, s, mwi)
			raw := notifyForSubscription(result.request, "terminated;reason="+reason+";retry-after=120", 1)
			before := time.Now()
			_, status := s.acceptSubscriptionNotification(raw)
			if status != 200 {
				t.Fatalf("reason %s: %d", reason, status)
			}
			completeProtocolSubscription(t, s, mwi, result)
			f := s.subscriptionFieldsLocked(mwi)
			if !*f.closed || f.dialog.ready() || !f.lifecycle.notifyDeadline.IsZero() {
				t.Fatalf("late 200 resurrected terminated %s", reason)
			}
			switch reason {
			case "rejected", "noresource", "invariant":
				if f.lifecycle.blockedReason == "" || !f.lifecycle.retryAt.IsZero() {
					t.Fatalf("reason %s allowed retry", reason)
				}
				if start, _ := s.prepareSubscriptionStart(mwi); start {
					t.Fatalf("REGISTER restarted reason %s", reason)
				}
			case "deactivated", "timeout":
				if f.lifecycle.retryAt.IsZero() || f.lifecycle.retryAt.After(time.Now()) {
					t.Fatalf("reason %s did not schedule immediate re-subscription", reason)
				}
				next := beginProtocolSubscription(t, s, mwi)
				if next.request.CallID().Value() == result.request.CallID().Value() {
					t.Fatal("re-subscription reused the terminated dialog")
				}
			default:
				if f.lifecycle.retryAt.Before(before.Add(120 * time.Second)) {
					t.Fatalf("reason %s ignored retry-after", reason)
				}
				if start, _ := s.prepareSubscriptionStart(mwi); start {
					t.Fatalf("REGISTER bypassed reason %s retry-after", reason)
				}
			}
		}
	}
}

func TestSubscriptionExpiryDoesNotShortenRetryAfter(t *testing.T) {
	s := newSubscriptionLifecycleTestService(t, nil)
	result := beginProtocolSubscription(t, s, false)
	completeProtocolSubscription(t, s, false, result)
	refresh := beginProtocolSubscription(t, s, false)
	refresh.response = sip.NewResponse(503, "Service Unavailable")
	refresh.response.AppendHeader(sip.NewHeader("Retry-After", "7200"))
	refresh.err = errors.New("503")
	_ = s.recordSubscriptionResult(refresh)
	retry := s.subscriptionLifecycle.retryAt
	s.mu.Lock()
	s.expireSubscriptionTimersLocked(s.subscriptionLifecycle.expiresAt)
	s.mu.Unlock()
	if !s.subscriptionClosed || !s.subscriptionLifecycle.retryAt.Equal(retry) {
		t.Fatal("subscription expiry shortened Retry-After")
	}
}

func TestSubscriptionMWIRejectionStillWinsOverTimersAndEOF(t *testing.T) {
	for _, status := range []int{405, 489} {
		s := newSubscriptionLifecycleTestServiceForPreset(t, nil, "2degrees_nz_53024")
		result := beginProtocolSubscription(t, s, true)
		result.response, result.err = sip.NewResponse(status, "unsupported"), errors.New("unsupported MWI")
		_ = s.recordMWISubscriptionResult(result)
		s.mu.Lock()
		s.expireSubscriptionTimersLocked(time.Now().Add(time.Hour))
		s.mu.Unlock()
		if !s.mwiSubscriptionLifecycle.notifyDeadline.IsZero() || !s.mwiSubscriptionLifecycle.retryAt.IsZero() {
			t.Fatal("unsupported MWI retained a timer")
		}
		s.recordPortSClosed(nil, io.EOF, time.Now())
		if start, _ := s.prepareSubscriptionStart(true); start {
			t.Fatal("EOF restarted an unsupported MWI subscription")
		}
	}
}

func TestSubscriptionZeroExpiryIsNotReplacedByRequestedLifetime(t *testing.T) {
	response := sip.NewResponse(200, "OK")
	response.AppendHeader(sip.NewHeader("Expires", "0"))
	if got := subscriptionExpires(response, time.Hour); got != 0 {
		t.Fatalf("Expires: 0 became %s", got)
	}
	s := newSubscriptionLifecycleTestService(t, nil)
	result := beginProtocolSubscription(t, s, false)
	_, _ = s.acceptSubscriptionNotification(notifyForSubscription(result.request, "active;expires=0", 1))
	completeProtocolSubscription(t, s, false, result)
	if s.subscriptionExpires != 0 {
		t.Fatal("200 replaced the zero lifetime from NOTIFY")
	}
}
