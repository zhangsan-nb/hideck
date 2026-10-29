package ipsec3gpp

import (
	"bytes"
	"net"
	"testing"
)

// TS 33.203 7.4.1a: until the old SA set is deleted it keeps protecting the
// flows it selects, while the new set protects the new port_uc and port_pc.
func TestTransportKeepsPreviousSetForItsOwnFlows(t *testing.T) {
	oldUE, oldServer := newTransportPair(t, EncryptionAES, nil, nil)
	policy := testPolicy(nil, nil, EncryptionAES)
	policy.LocalPortC, policy.RemotePortC = 42000, 52000
	policy.FlowC.LocalPort, policy.FlowC.OutboundSPI, policy.FlowC.InboundSPI = 42000, 0x66666666, 0x55555555
	policy.FlowS.RemotePort, policy.FlowS.OutboundSPI, policy.FlowS.InboundSPI = 52000, 0x88888888, 0x77777777
	newUE, newServer := transportPairFromPolicy(t, policy)
	newUE.SetPrevious(oldUE)
	ueIP, pcscfIP := net.IPv4(10, 0, 0, 2), net.IPv4(10, 0, 0, 1)

	for _, tc := range []struct {
		name             string
		fromPort, toPort uint16
		spi              uint32
		peer             *Transport
	}{
		{"old port_uc to port_ps", 41000, 51001, 0x44444444, oldServer},
		{"new port_uc to port_ps", 42000, 51001, 0x66666666, newServer},
		{"port_us to old port_pc", 41001, 51000, 0x33333333, oldServer},
		{"port_us to new port_pc", 41001, 52000, 0x88888888, newServer},
	} {
		packet := udpPacket(t, ueIP, tc.fromPort, pcscfIP, tc.toPort, []byte(tc.name))
		protected, transformed, err := newUE.TransformOutbound(packet)
		if err != nil || !transformed {
			t.Fatalf("%s: transformed=%t err=%v", tc.name, transformed, err)
		}
		assertESP(t, protected, tc.spi)
		if decoded, _, err := tc.peer.TransformInbound(protected); err != nil || !bytes.Equal(decoded, packet) {
			t.Fatalf("%s: peer decode err=%v", tc.name, err)
		}
	}
	for _, tc := range []struct {
		name             string
		fromPort, toPort uint16
		peer             *Transport
	}{
		{"old set inbound", 51001, 41000, oldServer},
		{"new set inbound", 51001, 42000, newServer},
	} {
		packet := udpPacket(t, pcscfIP, tc.fromPort, ueIP, tc.toPort, []byte(tc.name))
		protected, _, err := tc.peer.TransformOutbound(packet)
		if err != nil {
			t.Fatal(err)
		}
		if decoded, transformed, err := newUE.TransformInbound(protected); err != nil || !transformed || !bytes.Equal(decoded, packet) {
			t.Fatalf("%s: transformed=%t err=%v", tc.name, transformed, err)
		}
	}

	newUE.SetPrevious(nil)
	stale := udpPacket(t, ueIP, 41000, pcscfIP, 51001, []byte("after retire"))
	if _, transformed, err := newUE.TransformOutbound(stale); err != nil || transformed {
		t.Fatalf("retired flow outbound = transformed:%t err:%v", transformed, err)
	}
	reply := udpPacket(t, pcscfIP, 51001, ueIP, 41000, []byte("after retire"))
	protected, _, err := oldServer.TransformOutbound(reply)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := newUE.TransformInbound(protected); err == nil || err.Error() != "ipsec3gpp: unknown inbound ESP SPI" {
		t.Fatalf("retired SPI inbound err = %v", err)
	}
}
