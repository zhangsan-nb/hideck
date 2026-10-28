package swu

import (
	"strings"
	"testing"

	"github.com/iniwex5/vowifi-go/engine/ikev2"
)

func TestCreateChildSAOfferDescribesEveryProposal(t *testing.T) {
	first := &ikev2.Proposal{ProposalNum: 1, ProtocolID: ikev2.ProtoESP, SPI: []byte{1, 2, 3, 4}}
	first.AddTransform(ikev2.TransformTypeEncr, 12, 256)
	first.AddTransform(ikev2.TransformTypeInteg, 12, 0)
	second := &ikev2.Proposal{ProposalNum: 2, ProtocolID: ikev2.ProtoESP, SPI: []byte{1, 2, 3, 4}}
	second.AddTransform(ikev2.TransformTypeEncr, 20, 128)
	payloads := []ikev2.Payload{
		&ikev2.EncryptedPayloadSA{Proposals: []*ikev2.Proposal{first, second}},
		&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.REKEY_SA},
	}
	got := createChildSAOffer(payloads)
	for _, want := range []string{"SA#1(proto=3 spi=4 1=12/256,3=12)", "SA#2(proto=3 spi=4 1=20/128)", "N(16393)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("offer %q missing %q", got, want)
		}
	}
	if protocol, err := createChildSAProtocol(payloads); err != nil || protocol != ikev2.ProtoESP {
		t.Fatalf("multi-proposal protocol = %d, %v", protocol, err)
	}
	// RFC 7296 2.7: an unsupported AH proposal does not hide the ESP one after it.
	ah := &ikev2.Proposal{ProposalNum: 1, ProtocolID: ikev2.ProtoAH, SPI: []byte{1, 2, 3, 4}}
	if protocol, err := createChildSAProtocol([]ikev2.Payload{
		&ikev2.EncryptedPayloadSA{Proposals: []*ikev2.Proposal{ah, second}},
	}); err != nil || protocol != ikev2.ProtoESP {
		t.Fatalf("AH then ESP protocol = %d, %v", protocol, err)
	}
	// Only AH: handled on the CHILD_SA path, which refuses it with a notify.
	if protocol, err := createChildSAProtocol([]ikev2.Payload{
		&ikev2.EncryptedPayloadSA{Proposals: []*ikev2.Proposal{ah}},
	}); err != nil || protocol != ikev2.ProtoESP {
		t.Fatalf("AH-only protocol = %d, %v", protocol, err)
	}
	if _, err := createChildSAProtocol([]ikev2.Payload{&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.REKEY_SA}}); err == nil ||
		!strings.Contains(err.Error(), "payloads: N(16393)") {
		t.Fatalf("createChildSAProtocol error = %v", err)
	}
}

func TestNarrowPeerProposalEdgeCases(t *testing.T) {
	esp := func(build func(*ikev2.Proposal)) *ikev2.Proposal {
		proposal := &ikev2.Proposal{ProposalNum: 2, ProtocolID: ikev2.ProtoESP, SPI: []byte{1, 2, 3, 4}}
		build(proposal)
		return proposal
	}
	noPFS := map[ikev2.TransformType]ikev2.AlgorithmType{
		ikev2.TransformTypeEncr: 12, ikev2.TransformTypeInteg: 12, ikev2.TransformTypeDH: 0, ikev2.TransformTypeESN: 0,
	}
	pfs := map[ikev2.TransformType]ikev2.AlgorithmType{
		ikev2.TransformTypeEncr: 12, ikev2.TransformTypeInteg: 12, ikev2.TransformTypeDH: 14, ikev2.TransformTypeESN: 0,
	}
	aead := map[ikev2.TransformType]ikev2.AlgorithmType{
		ikev2.TransformTypeEncr: 20, ikev2.TransformTypeDH: 0, ikev2.TransformTypeESN: 0,
	}
	cases := []struct {
		name     string
		proposal *ikev2.Proposal
		want     map[ikev2.TransformType]ikev2.AlgorithmType
		bits     uint16
		ok       bool
		count    int
	}{
		{"no PFS with DH NONE", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeDH, 0, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), noPFS, 256, true, 3},
		{"no PFS with DH omitted", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), noPFS, 256, true, 3},
		{"PFS but DH omitted", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), pfs, 256, false, 0},
		{"PFS with several groups", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeDH, 19, 0)
			p.AddTransform(ikev2.TransformTypeDH, 14, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), pfs, 256, true, 4},
		{"no PFS but only DH 14", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeDH, 14, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), noPFS, 256, false, 0},
		{"AEAD with INTEG NONE", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 20, 128)
			p.AddTransform(ikev2.TransformTypeInteg, 0, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), aead, 128, true, 2},
		{"AES-CBC key length alternatives", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 128)
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), noPFS, 256, true, 3},
		{"only a different key length", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 128)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
			p.AddTransform(ikev2.TransformTypeESN, 0, 0)
		}), noPFS, 256, false, 0},
		{"ESN missing", esp(func(p *ikev2.Proposal) {
			p.AddTransform(ikev2.TransformTypeEncr, 12, 256)
			p.AddTransform(ikev2.TransformTypeInteg, 12, 0)
		}), noPFS, 256, false, 0},
	}
	for _, tc := range cases {
		narrowed, ok := narrowPeerProposal(tc.proposal, tc.want, tc.bits)
		if ok != tc.ok || ok && len(narrowed.Transforms) != tc.count {
			t.Errorf("%s: ok=%v transforms=%v", tc.name, ok, narrowed)
		}
	}
	mixed := []ikev2.Payload{&ikev2.EncryptedPayloadSA{Proposals: []*ikev2.Proposal{
		{ProposalNum: 1, ProtocolID: ikev2.ProtoIKE, SPI: make([]byte, 8)},
		cases[0].proposal,
	}}}
	if _, number, ok := narrowPeerRekeyOffer(mixed, ikev2.ProtoESP, noPFS, 256); !ok || number != 2 {
		t.Fatalf("mixed-protocol offer: number=%d ok=%v", number, ok)
	}
}
