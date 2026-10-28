//go:build linux

package volte

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestIsALSAXrunUnwrapsPathError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"bare EPIPE", unix.EPIPE, true},
		{"bare ESTRPIPE", unix.ESTRPIPE, true},
		{"PathError EPIPE", &os.PathError{Op: "write", Path: "/dev/snd/pcmC3D0p", Err: unix.EPIPE}, true},
		{"wrapped PathError", fmt.Errorf("volte: %w", &os.PathError{Op: "write", Path: "/dev/snd/pcmC3D0p", Err: unix.EPIPE}), true},
		{"ENODEV not xrun", &os.PathError{Op: "write", Path: "/dev/snd/pcmC3D0p", Err: unix.ENODEV}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isALSAXrun(tc.err); got != tc.want {
			t.Errorf("%s: isALSAXrun(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestPCMStatisticsAreScopedToOnePCM(t *testing.T) {
	first := &alsaPCM{}
	second := &alsaPCM{}
	first.xrun.Add(2)
	first.recovered.Add(1)

	if got := first.pcmDiagnostics(); got != (pcmDiagnostics{XRUN: 2, Recovered: 1}) {
		t.Fatalf("first diagnostics = %+v", got)
	}
	if got := second.pcmDiagnostics(); got != (pcmDiagnostics{}) {
		t.Fatalf("second diagnostics leaked another call: %+v", got)
	}
}

func TestALSARecoveryErrorExposesActualFailure(t *testing.T) {
	initialErr := &os.PathError{Op: "write", Path: "/dev/snd/pcmC3D0p", Err: unix.EPIPE}
	recoveryErr := unix.ENODEV
	err := alsaRecoveryError("PREPARE", recoveryErr, initialErr)

	if !errors.Is(err, recoveryErr) {
		t.Fatalf("recovery error is not exposed: %v", err)
	}
	if !strings.Contains(err.Error(), "PREPARE") || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("recovery error lost operation or initial failure: %v", err)
	}
}
