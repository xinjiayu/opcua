package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// P15: CreateSession alone must not authorize Read/Write. Identity is only
// established in ActivateSession; until then the session is half-open.
func TestUnactivatedSessionCannotRead(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Dial(ctx); err != nil {
		t.Fatalf("Dial: %v", err)
	}

	clientSess, err := c.CreateSession(ctx, &uasc.SessionConfig{
		SessionName:    "p15-unactivated",
		SessionTimeout: time.Minute,
		ClientDescription: &ua.ApplicationDescription{
			ApplicationURI:  "urn:gopcua:p15",
			ApplicationName: ua.NewLocalizedText("p15"),
			ApplicationType: ua.ApplicationTypeClient,
			DiscoveryURLs:   []string{},
		},
		LocaleIDs:          []string{},
		UserIdentityToken:  &ua.AnonymousIdentityToken{},
		UserTokenSignature: &ua.SignatureData{},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	s.sb.mu.Lock()
	var token *ua.NodeID
	for _, sess := range s.sb.s {
		token = sess.AuthTokenID
	}
	s.sb.mu.Unlock()
	if token == nil {
		t.Fatal("CreateSession left no session in the broker")
	}

	got := sendFault(t, c, &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead: []*ua.ReadValueID{{
			NodeID:       ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime),
			AttributeID:  ua.AttributeIDValue,
			DataEncoding: &ua.QualifiedName{},
		}},
	}, token)
	if got != ua.StatusBadSessionNotActivated {
		t.Fatalf("Read on unactivated session: %v, want StatusBadSessionNotActivated", got)
	}

	got = sendFault(t, c, &ua.WriteRequest{
		NodesToWrite: []*ua.WriteValue{{
			NodeID:      ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime),
			AttributeID: ua.AttributeIDValue,
			Value:       &ua.DataValue{Value: ua.MustVariant(true)},
		}},
	}, token)
	if got != ua.StatusBadSessionNotActivated {
		t.Fatalf("Write on unactivated session: %v, want StatusBadSessionNotActivated", got)
	}

	// Activate then Read must succeed (anonymous is enabled on the test server).
	if err := c.ActivateSession(ctx, clientSess); err != nil {
		t.Fatalf("ActivateSession: %v", err)
	}
	if err := send(t, c, &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead: []*ua.ReadValueID{{
			NodeID:       ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime),
			AttributeID:  ua.AttributeIDValue,
			DataEncoding: &ua.QualifiedName{},
		}},
	}, token, func(resp ua.Response) error {
		r, ok := resp.(*ua.ReadResponse)
		if !ok {
			return fmt.Errorf("expected ReadResponse, got %T", resp)
		}
		if len(r.Results) != 1 || r.Results[0].Status != ua.StatusOK {
			return fmt.Errorf("Read after ActivateSession: %v", r.Results)
		}
		return nil
	}); err != nil {
		t.Fatalf("Read after ActivateSession: %v", err)
	}
}
