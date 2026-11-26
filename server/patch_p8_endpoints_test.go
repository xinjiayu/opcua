package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P8: the address a client asks about in GetEndpoints.
//
// Upstream v0.8.0 (server/discovery_service.go GetEndpoints) returned only
// the endpoints whose configured URL equalled the EndpointURL of the request
// character for character, and an empty list otherwise. A server that listens
// on opc.tcp://0.0.0.0:4840 never sees that string from a client, which
// reaches it by a host name or by its address, so every client got an empty
// endpoint list. A request without an address got an empty list as well.
//
// The address now selects by scheme and port only. The host of the returned
// URLs is the one the client used, because that is the address it reached the
// server by. Without an address the configured endpoints come back unchanged.
//
// The list of endpoints in the CreateSession response was matched the same way
// in session_service.go and goes through the same selection now.

// endpointServer returns a server that is not listening but has its
// endpoints built, configured to listen on the given ports of 0.0.0.0.
func endpointServer(t *testing.T, ports ...int) *Server {
	t.Helper()

	opts := []Option{EnableSecurity("None", ua.MessageSecurityModeNone), EnableAuthMode(ua.UserTokenTypeAnonymous)}
	for _, port := range ports {
		opts = append(opts, EndPoint("0.0.0.0", port))
	}
	s := New(opts...)
	s.initHandlers()
	s.initEndpoints()
	return s
}

// endpointURLs asks the GetEndpoints handler about the given address and
// returns the endpoint URLs it answers with.
func endpointURLs(t *testing.T, s *Server, address string) []string {
	t.Helper()

	resp := call(t, s, id.GetEndpointsRequest_Encoding_DefaultBinary, &ua.GetEndpointsRequest{RequestHeader: &ua.RequestHeader{}, EndpointURL: address})
	ge, ok := resp.(*ua.GetEndpointsResponse)
	if !ok {
		t.Fatalf("expected *ua.GetEndpointsResponse, got %T", resp)
	}
	urls := make([]string, 0, len(ge.Endpoints))
	for _, ep := range ge.Endpoints {
		urls = append(urls, ep.EndpointURL)
	}
	return urls
}

func TestGetEndpointsMatchesByPortNotByHost(t *testing.T) {
	s := endpointServer(t, 4840, 4841)

	cases := []struct {
		name    string
		address string
		want    []string
	}{
		// a client reaching the server by an address
		{"by-address", "opc.tcp://192.168.1.10:4840", []string{"opc.tcp://192.168.1.10:4840"}},
		{"by-host-name", "opc.tcp://localhost:4840", []string{"opc.tcp://localhost:4840"}},
		{"by-other-port", "opc.tcp://192.168.1.10:4841", []string{"opc.tcp://192.168.1.10:4841"}},
		// the configured address itself, as before
		{"as-configured", "opc.tcp://0.0.0.0:4840", []string{"opc.tcp://0.0.0.0:4840"}},
		{"as-configured-upper-case", "OPC.TCP://0.0.0.0:4840", []string{"opc.tcp://0.0.0.0:4840"}},
		// 4840 is the port when the address carries none
		{"default-port", "opc.tcp://192.168.1.10", []string{"opc.tcp://192.168.1.10:4840"}},
		{"ipv6-address", "opc.tcp://[::1]:4840", []string{"opc.tcp://[::1]:4840"}},
		// nothing to match against
		{"without-address", "", []string{"opc.tcp://0.0.0.0:4840", "opc.tcp://0.0.0.0:4841"}},
		// no endpoint on that port or with that scheme
		{"other-port", "opc.tcp://192.168.1.10:9999", []string{}},
		{"other-scheme", "http://192.168.1.10:4840", []string{}},
		{"garbage", "not a url", []string{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := endpointURLs(t, s, c.address); !slices.Equal(got, c.want) {
				t.Fatalf("GetEndpoints for %q answered %v, want %v", c.address, got, c.want)
			}
		})
	}
}

// TestGetEndpointsLeavesConfiguredEndpointsAlone checks that answering with a
// rewritten address does not alter the endpoints the server is configured
// with.
func TestGetEndpointsLeavesConfiguredEndpointsAlone(t *testing.T) {
	s := endpointServer(t, 4840)
	endpointURLs(t, s, "opc.tcp://192.168.1.10:4840")

	if got := s.URLs(); !slices.Equal(got, []string{"opc.tcp://0.0.0.0:4840"}) {
		t.Fatalf("the configured endpoints are now %v", got)
	}
	for _, ep := range s.Endpoints() {
		if ep.EndpointURL != "opc.tcp://0.0.0.0:4840" {
			t.Fatalf("an endpoint the server hands out was changed to %q", ep.EndpointURL)
		}
	}
}

// TestGetEndpointsOverTheWire asks about an address with another host than
// the one the client is connected to. The connection itself uses the IP
// literal, so the test does not depend on how the host name resolves.
func TestGetEndpointsOverTheWire(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := dialTestClient(t, addr)

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("the server listens on %q: %v", addr, err)
	}

	var got []string
	err = send(t, c, &ua.GetEndpointsRequest{EndpointURL: "opc.tcp://localhost:" + port}, nil, func(resp ua.Response) error {
		for _, ep := range resp.(*ua.GetEndpointsResponse).Endpoints {
			got = append(got, ep.EndpointURL)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("GetEndpoints: %v", err)
	}
	if want := []string{fmt.Sprintf("opc.tcp://localhost:%s", port)}; !slices.Equal(got, want) {
		t.Fatalf("GetEndpoints for a host name answered %v, want %v", got, want)
	}

	// and the client library's own request, which names the address it
	// dialed, still gets the endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := c.GetEndpoints(ctx)
	if err != nil {
		t.Fatalf("GetEndpoints through the client library: %v", err)
	}
	if len(resp.Endpoints) != 1 || !strings.HasPrefix(resp.Endpoints[0].EndpointURL, "opc.tcp://127.0.0.1:") {
		t.Fatalf("GetEndpoints through the client library answered %v", resp.Endpoints)
	}
}

// TestCreateSessionListsEndpointsForAnyHost checks the list of endpoints in
// the CreateSession response. Upstream matched the address of the request
// against the configured endpoint URL character for character in
// session_service.go as well, so a client arriving by a host name was handed
// an empty list, and the client library could not take the anonymous token
// policy from it.
func TestCreateSessionListsEndpointsForAnyHost(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := dialTestClient(t, addr)

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("the server listens on %q: %v", addr, err)
	}

	// createSession asks for a session at the given address and returns the
	// endpoint URLs the response lists
	createSession := func(endpointURL string) []string {
		t.Helper()

		var response *ua.CreateSessionResponse
		err := send(t, c, &ua.CreateSessionRequest{
			ClientDescription: &ua.ApplicationDescription{
				ApplicationURI:  "urn:gopcua:patch-test",
				ApplicationName: ua.NewLocalizedText("patch test"),
				ApplicationType: ua.ApplicationTypeClient,
				DiscoveryURLs:   []string{},
			},
			EndpointURL:             endpointURL,
			SessionName:             "patch-test",
			ClientNonce:             bytes.Repeat([]byte{1}, 32),
			RequestedSessionTimeout: 60000,
		}, nil, func(resp ua.Response) error {
			response, _ = resp.(*ua.CreateSessionResponse)
			return nil
		})
		if err != nil {
			t.Fatalf("CreateSession for %q: %v", endpointURL, err)
		}
		if response == nil {
			t.Fatalf("CreateSession for %q did not answer with a CreateSessionResponse", endpointURL)
		}

		urls := make([]string, 0, len(response.ServerEndpoints))
		for _, ep := range response.ServerEndpoints {
			urls = append(urls, ep.EndpointURL)
		}
		return urls
	}

	byAddress := "opc.tcp://127.0.0.1:" + port
	byName := "opc.tcp://localhost:" + port

	if got := createSession(byAddress); !slices.Equal(got, []string{byAddress}) {
		t.Errorf("CreateSession naming the configured address listed %v, want %v", got, byAddress)
	}
	if got := createSession(byName); !slices.Equal(got, []string{byName}) {
		t.Errorf("CreateSession naming a host name listed %v, want %v", got, byName)
	}
}
