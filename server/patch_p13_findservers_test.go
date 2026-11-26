package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P13: FindServers on a server that has no endpoint.
//
// A server gets its endpoints from the security policies enabled with
// EnableSecurity. Started without any, it listens and answers, but has no
// endpoint. Upstream v0.8.0 (server/discovery_service.go FindServers) took
// the application description from the first endpoint without looking, so a
// single FindServers request was an index out of range on the one goroutine
// that serves requests: process dead.
//
// It takes that misconfiguration to get there, and the gateway enables a
// policy, so this closes a clear out-of-range access rather than a live
// hole. FindServers now answers like GetEndpoints does when nothing matches:
// an empty list and Good.

// serverWithoutEndpoints returns a server that is not listening, has its
// handlers and endpoints set up, and has no endpoint because no security
// policy was enabled.
func serverWithoutEndpoints(t *testing.T) *Server {
	t.Helper()

	s := New(EndPoint("127.0.0.1", 4840))
	s.initHandlers()
	s.initEndpoints()
	if got := len(s.Endpoints()); got != 0 {
		t.Fatalf("test setup: the server has %d endpoints, want none", got)
	}
	return s
}

func TestFindServersWithoutEndpoints(t *testing.T) {
	crashProbe(t, "no-security-policy-enabled", func() {
		s := serverWithoutEndpoints(t)

		resp := call(t, s, id.FindServersRequest_Encoding_DefaultBinary, &ua.FindServersRequest{RequestHeader: &ua.RequestHeader{}})
		fs, ok := resp.(*ua.FindServersResponse)
		if !ok {
			t.Fatalf("expected *ua.FindServersResponse, got %T", resp)
		}
		if fs.ResponseHeader.ServiceResult != ua.StatusOK {
			t.Fatalf("FindServers answered with %v, want %v", fs.ResponseHeader.ServiceResult, ua.StatusOK)
		}
		if len(fs.Servers) != 0 {
			t.Fatalf("FindServers listed %d servers, want none", len(fs.Servers))
		}

		// the answer is a message like any other
		b := encodeOrDie(t, fs)
		var back ua.FindServersResponse
		if _, err := ua.Decode(b, &back); err != nil {
			t.Fatalf("the FindServersResponse cannot be decoded: %v", err)
		}
	})
}

// TestFindServersWithoutEndpointsOverTheWire asks a listening server through
// the client library.
func TestFindServersWithoutEndpointsOverTheWire(t *testing.T) {
	crashProbe(t, "over-the-wire", func() {
		s := New(EndPoint("127.0.0.1", freePort(t)))
		addr := startTestServer(t, s)
		c := dialTestClient(t, addr)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.FindServers(ctx)
		if err != nil {
			t.Fatalf("FindServers: %v", err)
		}
		if len(resp.Servers) != 0 {
			t.Fatalf("FindServers listed %d servers, want none", len(resp.Servers))
		}
	})
}
