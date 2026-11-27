package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// P14: ActivateSession must validate UserNameIdentityToken and refuse
// identity tokens the endpoint did not advertise.
//
// Upstream ActivateSession verifies the session signature and rotates the
// nonce, but never looks at UserIdentityToken. A server that declared
// username auth (or disabled anonymous) still accepted every connection.

func TestActivateSessionRejectsAnonymousWhenDisabled(t *testing.T) {
	srvCert, srvKey := genServerCert(t)
	cliCert, cliKey := genServerCert(t)
	port := freePort(t)

	s := New(
		EndPoint("127.0.0.1", port),
		PrivateKey(srvKey),
		Certificate(srvCert),
		EnableSecurity("None", ua.MessageSecurityModeNone),
		EnableSecurity("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt),
		EnableAuthMode(ua.UserTokenTypeUserName),
		UserAuth(func(user, pass string) error {
			if user == "u" && pass == "p" {
				return nil
			}
			return fmt.Errorf("bad credentials")
		}),
	)
	addr := startTestServer(t, s)

	// Endpoint must not advertise Anonymous.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eps, err := waitEndpoints(ctx, "opc.tcp://"+addr)
	if err != nil {
		t.Fatalf("GetEndpoints: %v", err)
	}
	for _, ep := range eps {
		for _, tok := range ep.UserIdentityTokens {
			if tok.TokenType == ua.UserTokenTypeAnonymous {
				t.Fatalf("endpoint %s/%s still advertises Anonymous", ep.SecurityPolicyURI, ep.SecurityMode)
			}
		}
	}

	// Anonymous connect against a SignAndEncrypt endpoint must fail.
	var ep *ua.EndpointDescription
	for _, e := range eps {
		if e.SecurityMode == ua.MessageSecurityModeSignAndEncrypt {
			ep = e
			break
		}
	}
	if ep == nil {
		t.Fatal("no SignAndEncrypt endpoint")
	}
	c, err := opcua.NewClient("opc.tcp://"+addr,
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous),
		opcua.Certificate(cliCert),
		opcua.PrivateKey(cliKey),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	if err := c.Connect(ctx); err == nil {
		t.Fatal("anonymous Connect succeeded; want rejection")
	}
}

func TestActivateSessionUsernamePassword(t *testing.T) {
	srvCert, srvKey := genServerCert(t)
	cliCert, cliKey := genServerCert(t)
	port := freePort(t)

	s := New(
		EndPoint("127.0.0.1", port),
		PrivateKey(srvKey),
		Certificate(srvCert),
		EnableSecurity("Basic256Sha256", ua.MessageSecurityModeSignAndEncrypt),
		EnableAuthMode(ua.UserTokenTypeUserName),
		UserAuth(func(user, pass string) error {
			if user == "alice" && pass == "secret" {
				return nil
			}
			return fmt.Errorf("denied")
		}),
	)
	addr := startTestServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eps, err := waitEndpoints(ctx, "opc.tcp://"+addr)
	if err != nil {
		t.Fatalf("GetEndpoints: %v", err)
	}
	var ep *ua.EndpointDescription
	for _, e := range eps {
		if e.SecurityMode == ua.MessageSecurityModeSignAndEncrypt {
			ep = e
			break
		}
	}
	if ep == nil {
		t.Fatal("no SignAndEncrypt endpoint")
	}

	t.Run("wrong-password", func(t *testing.T) {
		c, err := opcua.NewClient("opc.tcp://"+addr,
			opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
			opcua.AuthUsername("alice", "wrong"),
			opcua.Certificate(cliCert),
			opcua.PrivateKey(cliKey),
		)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		if err := c.Connect(ctx); err == nil {
			t.Fatal("Connect with wrong password succeeded")
		}
	})

	t.Run("correct-password", func(t *testing.T) {
		c, err := opcua.NewClient("opc.tcp://"+addr,
			opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName),
			opcua.AuthUsername("alice", "secret"),
			opcua.Certificate(cliCert),
			opcua.PrivateKey(cliKey),
		)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		if err := c.Connect(ctx); err != nil {
			t.Fatalf("Connect with correct password: %v", err)
		}
		if _, err := c.GetEndpoints(ctx); err != nil {
			t.Fatalf("GetEndpoints after auth: %v", err)
		}
	})
}

func TestActivateSessionAnonymousStillWorksWhenEnabled(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("anonymous Connect: %v", err)
	}
}

func waitEndpoints(ctx context.Context, addr string) ([]*ua.EndpointDescription, error) {
	for {
		eps, err := opcua.GetEndpoints(ctx, addr)
		if err == nil {
			return eps, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func genServerCert(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gopcua-patch-p14"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return der, key
}
