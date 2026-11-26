package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P11: closing a session with DeleteSubscriptions set.
//
// OPC UA Part 4, 5.6.4: a client that closes its session with
// DeleteSubscriptions set to true expects the server to delete every
// subscription of that session, monitored items included.
//
// Upstream v0.8.0 (server/session_service.go CloseSession) never looked at the
// flag. The subscriptions stayed registered, with their monitored items and
// their publishing goroutines, until each one had run through its lifetime
// count. For a client that has hung up that is many publishing intervals.
// The client library of this module always sends the flag.

// monitoredItemOn is a monitored item on the current time node.
func monitoredItemOn(node *ua.NodeID) *ua.MonitoredItemCreateRequest {
	return &ua.MonitoredItemCreateRequest{
		ItemToMonitor:       &ua.ReadValueID{NodeID: node, AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}},
		MonitoringMode:      ua.MonitoringModeReporting,
		RequestedParameters: &ua.MonitoringParameters{ClientHandle: 1, SamplingInterval: -1, QueueSize: 10, DiscardOldest: true},
	}
}

// closedSessionFixture is what a session leaves behind when it closes: two
// subscriptions with a monitored item each. A second session with one
// subscription of its own shows that only the closed session is affected.
type closedSessionFixture struct {
	s         *Server
	sess      *session
	subs      []uint32
	items     []uint32
	other     *session
	otherSub  uint32
	otherItem uint32
}

func newClosedSessionFixture(t *testing.T) closedSessionFixture {
	t.Helper()

	s := newTestServer(t)
	node := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)

	// monitor registers a monitored item in the subscription through the
	// handler, so that it is registered the way a real one is, and returns its id
	monitor := func(sess *session, subID uint32) uint32 {
		t.Helper()

		resp := call(t, s, id.CreateMonitoredItemsRequest_Encoding_DefaultBinary, &ua.CreateMonitoredItemsRequest{
			RequestHeader:      header(sess),
			SubscriptionID:     subID,
			TimestampsToReturn: ua.TimestampsToReturnBoth,
			ItemsToCreate:      []*ua.MonitoredItemCreateRequest{monitoredItemOn(node)},
		})
		results := resp.(*ua.CreateMonitoredItemsResponse).Results
		if len(results) != 1 || results[0].StatusCode != ua.StatusOK {
			t.Fatalf("CreateMonitoredItems in subscription %d returned %v", subID, results)
		}
		return results[0].MonitoredItemID
	}

	f := closedSessionFixture{s: s, sess: newTestSession(s), other: newTestSession(s)}
	for i := 0; i < 2; i++ {
		subID := createSubscription(t, s, f.sess, 3600000)
		f.subs = append(f.subs, subID)
		f.items = append(f.items, monitor(f.sess, subID))
	}
	f.otherSub = createSubscription(t, s, f.other, 3600000)
	f.otherItem = monitor(f.other, f.otherSub)
	return f
}

// counts reports how many of the closed session's subscriptions and monitored
// items are still registered, and how many of the other session's.
func (f closedSessionFixture) counts() (subs, items, otherSubs, otherItems int) {
	svc, mis := f.s.SubscriptionService, f.s.MonitoredItemService

	svc.Mu.Lock()
	for _, subID := range f.subs {
		if _, ok := svc.Subs[subID]; ok {
			subs++
		}
	}
	if _, ok := svc.Subs[f.otherSub]; ok {
		otherSubs = 1
	}
	svc.Mu.Unlock()

	mis.Mu.Lock()
	for _, itemID := range f.items {
		if _, ok := mis.Items[itemID]; ok {
			items++
		}
	}
	if _, ok := mis.Items[f.otherItem]; ok {
		otherItems = 1
	}
	mis.Mu.Unlock()

	return subs, items, otherSubs, otherItems
}

// closeSession sends CloseSession for the session of the fixture.
func (f closedSessionFixture) closeSession(t *testing.T, deleteSubscriptions bool) {
	t.Helper()

	resp := call(t, f.s, id.CloseSessionRequest_Encoding_DefaultBinary, &ua.CloseSessionRequest{
		RequestHeader:       header(f.sess),
		DeleteSubscriptions: deleteSubscriptions,
	})
	if resp.(*ua.CloseSessionResponse).ResponseHeader.ServiceResult != ua.StatusOK {
		t.Fatalf("CloseSession did not succeed")
	}
}

func TestCloseSessionDeletesSubscriptionsWhenAsked(t *testing.T) {
	f := newClosedSessionFixture(t)
	if subs, items, _, _ := f.counts(); subs != 2 || items != 2 {
		t.Fatalf("test setup: %d subscriptions and %d monitored items are registered, want 2 and 2", subs, items)
	}

	f.closeSession(t, true)

	if f.s.sb.lookup(f.sess.AuthTokenID) != nil {
		t.Fatalf("the session is still in the broker")
	}
	waitFor(t, "the subscriptions and monitored items of the closed session to go", func() bool {
		subs, items, _, _ := f.counts()
		return subs == 0 && items == 0
	})

	// the other session is not affected
	if _, _, otherSubs, otherItems := f.counts(); otherSubs != 1 || otherItems != 1 {
		t.Fatalf("closing the session removed the other session's subscription (%d left) or monitored item (%d left)", otherSubs, otherItems)
	}
}

func TestCloseSessionKeepsSubscriptionsWhenNotAsked(t *testing.T) {
	f := newClosedSessionFixture(t)

	f.closeSession(t, false)

	if f.s.sb.lookup(f.sess.AuthTokenID) != nil {
		t.Fatalf("the session is still in the broker")
	}
	// a removal would run in the background: give it time before checking
	time.Sleep(100 * time.Millisecond)
	if subs, items, _, _ := f.counts(); subs != 2 || items != 2 {
		t.Fatalf("without the flag the closed session kept %d subscriptions and %d monitored items, want 2 and 2", subs, items)
	}
}

// TestClientCloseClearsItsSubscriptions closes a session the way the client
// library does, with DeleteSubscriptions set.
func TestClientCloseClearsItsSubscriptions(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectWithTimeout(t, addr, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	notifications := make(chan *opcua.PublishNotificationData, 16)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notifications)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, monitoredItemOn(ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime))); err != nil {
		t.Fatalf("Monitor: %v", err)
	}
	if sessionCount(s) != 1 || subscriptionCount(s) != 1 {
		t.Fatalf("expected one session and one subscription, got %d and %d", sessionCount(s), subscriptionCount(s))
	}

	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	waitForUpTo(t, "the closed session, its subscription and its monitored item to go", 5*time.Second, func() bool {
		s.MonitoredItemService.Mu.Lock()
		items := len(s.MonitoredItemService.Items)
		s.MonitoredItemService.Mu.Unlock()
		return sessionCount(s) == 0 && subscriptionCount(s) == 0 && items == 0
	})
}
