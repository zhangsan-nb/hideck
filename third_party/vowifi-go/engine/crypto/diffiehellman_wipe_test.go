package crypto

import "testing"

func TestDiffieHellmanWipeClearsPrivateState(t *testing.T) {
	modp, err := NewDiffieHellman(2)
	if err != nil || modp.GenerateKey() != nil {
		t.Fatalf("modp: %v", err)
	}
	peer, _ := NewDiffieHellman(2)
	_ = peer.GenerateKey()
	if _, err := modp.ComputeSharedSecret(peer.PublicKeyBytes()); err != nil {
		t.Fatal(err)
	}
	words, secret := modp.PrivateKey.Bits(), modp.SharedKey
	modp.Wipe()
	for _, w := range words {
		if w != 0 {
			t.Fatal("MODP private key survived Wipe")
		}
	}
	for _, b := range secret {
		if b != 0 {
			t.Fatal("shared secret survived Wipe")
		}
	}
	if modp.PrivateKey != nil {
		t.Fatal("private key still set after Wipe")
	}
	if _, err := modp.ComputeSharedSecret(peer.PublicKeyBytes()); err == nil {
		t.Fatal("MODP private key still usable after Wipe")
	}

	ec, err := NewDiffieHellman(19)
	if err != nil || ec.GenerateKey() != nil {
		t.Fatalf("ecdh: %v", err)
	}
	ec.Wipe()
	ecPeer, _ := NewDiffieHellman(19)
	_ = ecPeer.GenerateKey()
	if _, err := ec.ComputeSharedSecret(ecPeer.PublicKeyBytes()); err == nil {
		t.Fatal("ECDH private key still usable after Wipe")
	}
	(*DiffieHellman)(nil).Wipe()
}
