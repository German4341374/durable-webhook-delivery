package cryptoutil

import (
	"bytes"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	cipher, err := NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := cipher.Encrypt([]byte("development-secret"), "channel-id")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(ciphertext, nonce, "channel-id")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "development-secret" {
		t.Fatalf("unexpected plaintext: %q", plaintext)
	}
}

func TestCipherRejectsWrongContext(t *testing.T) {
	cipher, _ := NewCipher(bytes.Repeat([]byte{7}, 32))
	ciphertext, nonce, _ := cipher.Encrypt([]byte("secret"), "channel-one")
	if _, err := cipher.Decrypt(ciphertext, nonce, "channel-two"); err == nil {
		t.Fatal("expected authenticated context failure")
	}
}

func TestCipherRequires256BitKey(t *testing.T) {
	if _, err := NewCipher([]byte("short")); err == nil {
		t.Fatal("expected key length error")
	}
}

func TestSignatureVerification(t *testing.T) {
	secret, body := []byte("long-development-secret"), []byte(`{"event":"created"}`)
	if !VerifySignature(secret, body, Sign(secret, body)) {
		t.Fatal("valid signature rejected")
	}
}

func TestSignatureRejectsTampering(t *testing.T) {
	secret := []byte("long-development-secret")
	if VerifySignature(secret, []byte("changed"), Sign(secret, []byte("original"))) {
		t.Fatal("tampered body accepted")
	}
}

func TestSignatureRejectsMalformedHex(t *testing.T) {
	if VerifySignature([]byte("secret"), []byte("body"), "sha256=not-hex") {
		t.Fatal("malformed signature accepted")
	}
}
