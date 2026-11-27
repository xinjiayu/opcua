// Copyright 2018-2020 opcua authors. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be
// found in the LICENSE file.

package uasc

import (
	"crypto/rsa"
	"encoding/binary"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uapolicy"
)

// NewSessionSignature issues a new signature for the client to send on the next ActivateSessionRequest
func (s *SecureChannel) NewSessionSignature(cert, nonce []byte) ([]byte, string, error) {
	if s.cfg.SecurityMode == ua.MessageSecurityModeNone {
		return nil, "", nil
	}

	remoteX509Cert, err := uapolicy.ParseCertificate(cert)
	if err != nil {
		return nil, "", err
	}
	remoteKey := remoteX509Cert.PublicKey.(*rsa.PublicKey)

	enc, err := uapolicy.Asymmetric(s.cfg.SecurityPolicyURI, s.cfg.LocalKey, remoteKey)
	if err != nil {
		return nil, "", err
	}

	sig, err := enc.Signature(append(cert, nonce...))
	if err != nil {
		return nil, "", err
	}
	sigAlg := enc.SignatureURI()

	return sig, sigAlg, nil
}

// VerifySessionSignature checks the integrity of a Create/Activate Session response's signature
func (s *SecureChannel) VerifySessionSignature(cert, nonce, signature []byte) error {
	if s.cfg.SecurityMode == ua.MessageSecurityModeNone {
		return nil
	}

	remoteX509Cert, err := uapolicy.ParseCertificate(cert)
	if err != nil {
		return err
	}
	remoteKey := remoteX509Cert.PublicKey.(*rsa.PublicKey)

	enc, err := uapolicy.Asymmetric(s.cfg.SecurityPolicyURI, s.cfg.LocalKey, remoteKey)
	if err != nil {
		return err
	}
	err = enc.VerifySignature(append(s.cfg.Certificate, nonce...), signature)
	if err != nil {
		return err
	}

	return nil
}

// EncryptUserPassword issues a new signature for the client to send in ActivateSessionRequest
func (s *SecureChannel) EncryptUserPassword(policyURI, password string, cert, nonce []byte) ([]byte, string, error) {
	// If the User ID Token's policy was null, then default to the secure channel's policy
	if policyURI == "" {
		policyURI = s.cfg.SecurityPolicyURI
	}

	if policyURI == ua.SecurityPolicyURINone {
		return []byte(password), "", nil
	}

	remoteX509Cert, err := uapolicy.ParseCertificate(cert)
	if err != nil {
		return nil, "", err
	}
	remoteKey := remoteX509Cert.PublicKey.(*rsa.PublicKey)

	enc, err := uapolicy.Asymmetric(policyURI, s.cfg.LocalKey, remoteKey)
	if err != nil {
		return nil, "", err
	}

	l := len(password) + len(nonce)
	secret := make([]byte, 4)
	binary.LittleEndian.PutUint32(secret, uint32(l))
	secret = append(secret, []byte(password)...)
	secret = append(secret, nonce...)
	pass, err := enc.Encrypt(secret)
	if err != nil {
		return nil, "", err
	}
	passAlg := enc.EncryptionURI()

	return pass, passAlg, nil
}

// NewUserTokenSignature issues a new signature for the client to send in ActivateSessionRequest
// The security policy for the SecureChannel is used if policyURI value is null or empty
// https://reference.opcfoundation.org/Core/Part4/v104/docs/7.37
func (s *SecureChannel) NewUserTokenSignature(policyURI string, cert, nonce []byte) ([]byte, string, error) {
	if policyURI == "" {
		policyURI = s.cfg.SecurityPolicyURI
	}

	if policyURI == ua.SecurityPolicyURINone {
		return nil, "", nil
	}

	remoteX509Cert, err := uapolicy.ParseCertificate(cert)
	if err != nil {
		return nil, "", err
	}
	remoteKey := remoteX509Cert.PublicKey.(*rsa.PublicKey)

	enc, err := uapolicy.Asymmetric(policyURI, s.cfg.UserKey, remoteKey)
	if err != nil {
		return nil, "", err
	}

	sig, err := enc.Signature(append(cert, nonce...))
	if err != nil {
		return nil, "", err
	}
	sigAlg := enc.SignatureURI()

	return sig, sigAlg, nil
}

// DecryptUserPassword recovers the plaintext password from a UserNameIdentityToken.
// policyURI is the token policy's SecurityPolicyURI; empty falls back to the channel policy.
// An empty EncryptionAlgorithm on the wire is represented by ciphertext that is already
// plaintext (policy None, or a client that sent the password unencrypted).
func (s *SecureChannel) DecryptUserPassword(policyURI string, ciphertext, serverNonce []byte) (string, error) {
	if policyURI == "" {
		policyURI = s.cfg.SecurityPolicyURI
	}

	if policyURI == ua.SecurityPolicyURINone || len(ciphertext) == 0 {
		return string(ciphertext), nil
	}

	enc, err := uapolicy.Asymmetric(policyURI, s.cfg.LocalKey, nil)
	if err != nil {
		return "", err
	}
	plaintext, err := enc.Decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	if len(plaintext) < 4 {
		return "", ua.StatusBadIdentityTokenInvalid
	}
	secretLen := binary.LittleEndian.Uint32(plaintext[:4])
	if int(secretLen) != len(plaintext)-4 {
		return "", ua.StatusBadIdentityTokenInvalid
	}
	body := plaintext[4:]
	if len(serverNonce) > 0 {
		if len(body) < len(serverNonce) {
			return "", ua.StatusBadIdentityTokenInvalid
		}
		pass, nonce := body[:len(body)-len(serverNonce)], body[len(body)-len(serverNonce):]
		for i := range serverNonce {
			if nonce[i] != serverNonce[i] {
				return "", ua.StatusBadIdentityTokenInvalid
			}
		}
		return string(pass), nil
	}
	return string(body), nil
}
