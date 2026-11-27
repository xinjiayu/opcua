package uasc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"testing"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uapolicy"
)

func TestDecryptUserPasswordRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sc := &SecureChannel{cfg: &Config{
		SecurityPolicyURI: ua.SecurityPolicyURIBasic256Sha256,
		LocalKey:          key,
		SecurityMode:      ua.MessageSecurityModeSignAndEncrypt,
	}}

	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}

	// Encrypt the way the client does.
	enc, err := uapolicy.Asymmetric(ua.SecurityPolicyURIBasic256Sha256, key, &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	password := "s3cret"
	l := len(password) + len(nonce)
	secret := make([]byte, 4)
	binary.LittleEndian.PutUint32(secret, uint32(l))
	secret = append(secret, []byte(password)...)
	secret = append(secret, nonce...)
	cipher, err := enc.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}

	got, err := sc.DecryptUserPassword(ua.SecurityPolicyURIBasic256Sha256, cipher, nonce)
	if err != nil {
		t.Fatalf("DecryptUserPassword: %v", err)
	}
	if got != password {
		t.Fatalf("got %q want %q", got, password)
	}
}

func TestDecryptUserPasswordPlaintext(t *testing.T) {
	sc := &SecureChannel{cfg: &Config{SecurityPolicyURI: ua.SecurityPolicyURINone}}
	got, err := sc.DecryptUserPassword(ua.SecurityPolicyURINone, []byte("plain"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestDecryptUserPasswordBadNonce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sc := &SecureChannel{cfg: &Config{
		SecurityPolicyURI: ua.SecurityPolicyURIBasic256Sha256,
		LocalKey:          key,
	}}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	enc, err := uapolicy.Asymmetric(ua.SecurityPolicyURIBasic256Sha256, key, &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 4)
	binary.LittleEndian.PutUint32(secret, uint32(5+32))
	secret = append(secret, []byte("hello")...)
	secret = append(secret, nonce...)
	cipher, err := enc.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), nonce...)
	bad[0] ^= 0xff
	if _, err := sc.DecryptUserPassword(ua.SecurityPolicyURIBasic256Sha256, cipher, bad); err == nil {
		t.Fatal("expected nonce mismatch error")
	}
}
