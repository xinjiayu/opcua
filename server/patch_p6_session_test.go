package server

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P6: every service that is not part of discovery or of the session set-up
// required no session at all upstream. handleService dispatched whatever a
// client sent on a bare secure channel: a Read, Write or Browse without
// CreateSession returned the same data a session would have got.
//
// handleService now demands a session the server knows for every service
// outside an allowlist and answers ServiceFault(BadSessionIDInvalid) without
// running the handler. A request that names a valid session also records
// activity on it, which is what lets P7 expire idle sessions.

// describeResponse names what the server answered with: for a ServiceFault
// its type and status, for anything else its type, service result and, when
// it is a Read, the first value, to make plain what was handed out.
func describeResponse(resp ua.Response) string {
	if fault, ok := resp.(*ua.ServiceFault); ok {
		return fmt.Sprintf("%T %v", fault, fault.ResponseHeader.ServiceResult)
	}
	s := fmt.Sprintf("%T", resp)
	if r, ok := resp.(*ua.ReadResponse); ok && len(r.Results) > 0 && r.Results[0].Value != nil {
		s += fmt.Sprintf(" carrying the value %v", r.Results[0].Value.Value())
	}
	if resp.Header() != nil {
		s += fmt.Sprintf(" (service result %v)", resp.Header().ServiceResult)
	}
	return s
}

func TestRequestsWithoutSessionAreRejected(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := dialTestClient(t, addr)

	currentTime := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)
	read := &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        []*ua.ReadValueID{{NodeID: currentTime, AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	}

	// The subscription and monitored item ids name nothing. Upstream then
	// answered with a per-service error instead of a crash, which keeps the
	// failure of this test readable.
	requests := []struct {
		name string
		req  ua.Request
	}{
		{"read", read},
		{"write", &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
			NodeID: ua.NewNumericNodeID(0, 0x7fffffff), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{},
		}}}},
		{"browse", &ua.BrowseRequest{
			// the default view, as the client library fills it in
			View: &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
			NodesToBrowse: []*ua.BrowseDescription{{
				NodeID: ua.NewNumericNodeID(0, id.ObjectsFolder), BrowseDirection: ua.BrowseDirectionForward,
				ReferenceTypeID: ua.NewTwoByteNodeID(0), IncludeSubtypes: true, ResultMask: 0x3f,
			}},
		}},
		{"delete-subscriptions", &ua.DeleteSubscriptionsRequest{SubscriptionIDs: []uint32{424242}}},
		{"create-monitored-items", &ua.CreateMonitoredItemsRequest{
			SubscriptionID:     424242,
			TimestampsToReturn: ua.TimestampsToReturnBoth,
			ItemsToCreate: []*ua.MonitoredItemCreateRequest{{
				ItemToMonitor:       &ua.ReadValueID{NodeID: currentTime, AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}},
				RequestedParameters: &ua.MonitoringParameters{ClientHandle: 1},
			}},
		}},
		{"delete-monitored-items", &ua.DeleteMonitoredItemsRequest{SubscriptionID: 424242, MonitoredItemIDs: []uint32{424242}}},
		{"set-monitoring-mode", &ua.SetMonitoringModeRequest{SubscriptionID: 424242, MonitoringMode: ua.MonitoringModeDisabled, MonitoredItemIDs: []uint32{424242}}},
		{"publish", &ua.PublishRequest{SubscriptionAcknowledgements: []*ua.SubscriptionAcknowledgement{}}},
		{"close-session", &ua.CloseSessionRequest{DeleteSubscriptions: true}},
		{"call", &ua.CallRequest{MethodsToCall: []*ua.CallMethodRequest{}}},
		{"add-nodes", &ua.AddNodesRequest{NodesToAdd: []*ua.AddNodesItem{}}},
		{"register-nodes", &ua.RegisterNodesRequest{NodesToRegister: []*ua.NodeID{}}},
		// last: upstream did create this subscription, with a nil session.
		{"create-subscription", &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 3600000, RequestedLifetimeCount: 10, RequestedMaxKeepAliveCount: 3}},
	}

	tokens := []struct {
		name  string
		token *ua.NodeID
	}{
		// nil is the null token a client sends before it has a session.
		{"null-token", nil},
		{"unknown-token", unknownToken()},
	}

	for _, r := range requests {
		requireRoundTrips(t, r.req)
		for _, tok := range tokens {
			t.Run(tok.name+"/"+r.name, func(t *testing.T) {
				var answered string
				err := send(t, c, r.req, tok.token, func(resp ua.Response) error {
					answered = describeResponse(resp)
					return nil
				})
				// The client hands the response to the callback and then
				// returns its service result as the error, so err alone says
				// nothing about what the server answered.
				if answered == "" {
					t.Fatalf("%T got no response: %v", r.req, err)
				}
				want := "*ua.ServiceFault " + ua.StatusBadSessionIDInvalid.Error()
				if answered != want {
					t.Fatalf("%T was answered with %s, want %s", r.req, answered, want)
				}
			})
		}
	}

	// none of the rejected requests may have left anything behind
	s.SubscriptionService.Mu.Lock()
	subs := len(s.SubscriptionService.Subs)
	s.SubscriptionService.Mu.Unlock()
	s.MonitoredItemService.Mu.Lock()
	items := len(s.MonitoredItemService.Items)
	s.MonitoredItemService.Mu.Unlock()
	s.sb.mu.Lock()
	sessions := len(s.sb.s)
	s.sb.mu.Unlock()

	if subs != 0 || items != 0 || sessions != 0 {
		t.Fatalf("requests without a session left %d subscriptions, %d monitored items and %d sessions behind", subs, items, sessions)
	}
}

// TestSessionlessServicesStillWork sends the services of the allowlist the way
// a real client does, without and with a session.
func TestSessionlessServicesStillWork(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// discovery on a bare secure channel
	discovery := dialTestClient(t, addr)
	endpoints, err := discovery.GetEndpoints(ctx)
	if err != nil {
		t.Fatalf("GetEndpoints without a session: %v", err)
	}
	if len(endpoints.Endpoints) == 0 {
		t.Fatalf("GetEndpoints without a session returned no endpoint")
	}
	servers, err := discovery.FindServers(ctx)
	if err != nil {
		t.Fatalf("FindServers without a session: %v", err)
	}
	if len(servers.Servers) == 0 {
		t.Fatalf("FindServers without a session returned no server")
	}

	// CreateSession and ActivateSession establish the session they need
	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// and with that session the services from the list above answer with data
	resp, err := c.Read(ctx, &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	})
	if err != nil {
		t.Fatalf("Read with a session: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Status != ua.StatusOK || resp.Results[0].Value == nil {
		t.Fatalf("Read with a session returned %v", resp.Results)
	}
}

// connectTestClient opens a secure channel and a session to addr. The client
// is closed when the test ends.
func connectTestClient(t *testing.T, addr string) *opcua.Client {
	t.Helper()

	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

// TestSessionlessServicesAllowlist pins which services may run without a
// session. Anything added to the table has to be a service a client needs
// before it has one.
func TestSessionlessServicesAllowlist(t *testing.T) {
	want := []uint16{
		id.ActivateSessionRequest_Encoding_DefaultBinary,
		id.CreateSessionRequest_Encoding_DefaultBinary,
		id.FindServersOnNetworkRequest_Encoding_DefaultBinary,
		id.FindServersRequest_Encoding_DefaultBinary,
		id.GetEndpointsRequest_Encoding_DefaultBinary,
		id.RegisterServer2Request_Encoding_DefaultBinary,
		id.RegisterServerRequest_Encoding_DefaultBinary,
	}
	got := make([]uint16, 0, len(sessionlessServices))
	for typeID := range sessionlessServices {
		got = append(got, typeID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("services allowed without a session = %v, want %v", got, want)
	}

	// each of them is a service the server actually serves
	s := newTestServer(t)
	for _, typeID := range want {
		if _, ok := s.handlers[typeID]; !ok {
			t.Errorf("service %d is allowed without a session but has no handler", typeID)
		}
	}

	// and the ones that work on a session are not on the list
	for _, typeID := range []uint16{
		id.CloseSessionRequest_Encoding_DefaultBinary,
		id.ReadRequest_Encoding_DefaultBinary,
		id.PublishRequest_Encoding_DefaultBinary,
		id.CreateSubscriptionRequest_Encoding_DefaultBinary,
	} {
		if _, ok := sessionlessServices[typeID]; ok {
			t.Errorf("service %d may run without a session", typeID)
		}
	}
}

// TestRequestRefreshesSessionActivity checks that a request naming a session
// records activity on it, whichever service it is for, and that a request
// without the session does not.
func TestRequestRefreshesSessionActivity(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectTestClient(t, addr)

	// the only session on the server is the one this client opened
	s.sb.mu.Lock()
	var sess *session
	for _, v := range s.sb.s {
		sess = v
	}
	s.sb.mu.Unlock()
	if sess == nil || len(s.sb.s) != 1 {
		t.Fatalf("expected exactly one session, got %d", len(s.sb.s))
	}

	backdate := func() time.Time {
		t.Helper()

		stale := time.Now().Add(-time.Hour)
		s.sb.mu.Lock()
		sess.lastActivity = stale
		s.sb.mu.Unlock()
		return stale
	}
	lastActivity := func() time.Time {
		s.sb.mu.Lock()
		defer s.sb.mu.Unlock()

		return sess.lastActivity
	}
	ignore := func(ua.Response) error { return nil }

	discovery := &ua.GetEndpointsRequest{EndpointURL: s.URLs()[0]}
	read := &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	}

	// a service that needs the session
	stale := backdate()
	if err := send(t, c, read, sess.AuthTokenID, ignore); err != nil {
		t.Fatalf("Read with the session: %v", err)
	}
	if !lastActivity().After(stale) {
		t.Fatalf("a Read did not count as activity on the session")
	}

	// a discovery request that carries the session token counts as well
	stale = backdate()
	if err := send(t, c, discovery, sess.AuthTokenID, ignore); err != nil {
		t.Fatalf("GetEndpoints with the session: %v", err)
	}
	if !lastActivity().After(stale) {
		t.Fatalf("a GetEndpoints carrying the session did not count as activity")
	}

	// a request without the session leaves it alone
	stale = backdate()
	if err := send(t, c, discovery, nil, ignore); err != nil {
		t.Fatalf("GetEndpoints without the session: %v", err)
	}
	if !lastActivity().Equal(stale) {
		t.Fatalf("a GetEndpoints without the session changed the activity time")
	}
}
