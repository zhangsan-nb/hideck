package swu

import (
	"fmt"
	"strings"

	"github.com/iniwex5/vowifi-go/engine/ikev2"
)

// createChildSAOffer describes a peer CREATE_CHILD_SA request for the log:
// every proposal with its protocol, SPI size and transforms (type=id, /key
// bits), plus notify types and the other payload types. No key material.
func createChildSAOffer(payloads []ikev2.Payload) string {
	parts := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		switch value := payload.(type) {
		case nil:
			parts = append(parts, "nil")
		case *ikev2.EncryptedPayloadSA:
			if len(value.Proposals) == 0 {
				parts = append(parts, "SA(none)")
			}
			for _, proposal := range value.Proposals {
				parts = append(parts, describeProposal(proposal))
			}
		case *ikev2.EncryptedPayloadNotify:
			parts = append(parts, fmt.Sprintf("N(%d)", value.NotifyType))
		default:
			parts = append(parts, fmt.Sprintf("%d", payload.Type()))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func describeProposal(proposal *ikev2.Proposal) string {
	if proposal == nil {
		return "SA#?(nil)"
	}
	transforms := make([]string, 0, len(proposal.Transforms))
	for _, transform := range proposal.Transforms {
		if transform == nil {
			transforms = append(transforms, "nil")
			continue
		}
		item := fmt.Sprintf("%d=%d", transform.Type, transform.ID)
		for _, attribute := range transform.Attributes {
			if attribute != nil && attribute.Type == transformAttributeKeyLength {
				item += fmt.Sprintf("/%d", attribute.Val)
			}
		}
		transforms = append(transforms, item)
	}
	return fmt.Sprintf("SA#%d(proto=%d spi=%d %s)",
		proposal.ProposalNum, proposal.ProtocolID, len(proposal.SPI), strings.Join(transforms, ","))
}
