package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// P7b: a session that is waiting to have a Publish request answered is not idle.
//
// P7 retires a session that has not carried a request for longer than its
// timeout. A client that only waits for notifications sends one Publish request
// and then nothing until it is answered, and the answer, a keepalive, is due
// after publishing interval × max keep alive count. With a slow subscription
// that is longer than the session timeout, so the sweep retired the session,
// and its subscriptions, while the client was still waiting for the answer.
// Before P7 nothing ever expired, so this was a regression of P7. The sweep now
// leaves such a session alone for as long as it holds a subscription and a
// queued request.

// soleSessionToken returns the authentication token of the only session the
// server holds.
func soleSessionToken(t *testing.T, s *Server) *ua.NodeID {
	t.Helper()

	s.sb.mu.Lock()
	defer s.sb.mu.Unlock()

	var token *ua.NodeID
	for _, sess := range s.sb.s {
		token = sess.AuthTokenID
	}
	if token == nil {
		t.Fatalf("the server does not hold a session")
	}
	return token
}

// queuePublish sends a Publish request and returns without waiting for the
// answer. The server answers a Publish only once it has something to send, so a
// caller that waited would block until the subscription's keepalive is due, or
// forever when the session has no subscription. The returned function gives up
// waiting; call it when the test ends.
func queuePublish(c *opcua.Client, token *ua.NodeID) (giveUp context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = c.SecureChannel().SendRequest(ctx, &ua.PublishRequest{}, token, func(ua.Response) error { return nil })
	}()
	return cancel
}

// waitForQueuedPublishes waits until the session holds n Publish requests.
func waitForQueuedPublishes(t *testing.T, s *Server, n int) {
	t.Helper()

	waitForUpTo(t, "the publish requests to reach the session", 5*time.Second, func() bool {
		s.sb.mu.Lock()
		defer s.sb.mu.Unlock()
		for _, sess := range s.sb.s {
			return len(sess.PublishRequests) == n
		}
		return false
	})
}

// TestSweepSparesSessionWaitingOnPublish drives the sweep by hand. A session
// that is long idle keeps its place while it holds a subscription and a queued
// Publish request, and is retired once the request is gone.
func TestSweepSparesSessionWaitingOnPublish(t *testing.T) {
	s := newTestServer(t)
	sess := newTestSession(s)
	sess.subscriptions = 1
	sess.PublishRequests <- PubReq{}

	s.sb.mu.Lock()
	sess.lastActivity = time.Now().Add(-time.Hour)
	s.sb.mu.Unlock()

	s.sweepSessions(time.Now())
	if sessionCount(s) != 1 {
		t.Fatalf("the sweep retired a session that is waiting to have a Publish request answered")
	}

	// the client is gone: its queued request is used up by a keepalive
	<-sess.PublishRequests
	s.sweepSessions(time.Now())
	if sessionCount(s) != 0 {
		t.Fatalf("the sweep kept a session that has nothing queued and has been idle for an hour")
	}
}

// TestSweepRetiresSessionWaitingWithoutSubscription: a Publish request on a
// session without a subscription is never answered, so it must not keep the
// session alive. Otherwise a client could hold every session slot forever with
// one request each.
func TestSweepRetiresSessionWaitingWithoutSubscription(t *testing.T) {
	s := newTestServer(t)
	sess := newTestSession(s)
	sess.PublishRequests <- PubReq{}

	s.sb.mu.Lock()
	sess.lastActivity = time.Now().Add(-time.Hour)
	s.sb.mu.Unlock()

	s.sweepSessions(time.Now())
	if sessionCount(s) != 0 {
		t.Fatalf("a Publish request on a session without a subscription kept the session alive")
	}
}

// TestWaitingPublishKeepsSlowSubscriptionSessionAlive is the case over the
// wire: the session timeout is 300 ms and the subscription's keepalive is due
// after about 100 s. The client sends one Publish request and waits.
func TestWaitingPublishKeepsSlowSubscriptionSessionAlive(t *testing.T) {
	quickSweep(t)
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	const timeout = 300 * time.Millisecond
	c := connectWithTimeout(t, addr, timeout)

	token := soleSessionToken(t, s)
	if err := send(t, c, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 100, RequestedLifetimeCount: 1000, RequestedMaxKeepAliveCount: 1000}, token, func(ua.Response) error { return nil }); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	giveUp := queuePublish(c, token)
	defer giveUp()
	waitForQueuedPublishes(t, s, 1)

	// several session timeouts pass without a single request
	time.Sleep(1500 * time.Millisecond)
	if sessionCount(s) != 1 || subscriptionCount(s) != 1 {
		t.Fatalf("the server holds %d sessions and %d subscriptions, want the one waiting client and its subscription", sessionCount(s), subscriptionCount(s))
	}

	// and the session was idle all along, so it is the queued request that kept it
	s.sb.mu.Lock()
	var idleFor time.Duration
	for _, sess := range s.sb.s {
		idleFor = time.Since(sess.lastActivity)
	}
	s.sb.mu.Unlock()
	if idleFor < timeout {
		t.Fatalf("the session carried a request %v ago, below its %v timeout, so this test proves nothing", idleFor, timeout)
	}
}

// TestQueuedPublishWithoutSubscriptionStillExpires: the protection needs a
// subscription. A session with a queued Publish request and no subscription
// must still be retired.
func TestQueuedPublishWithoutSubscriptionStillExpires(t *testing.T) {
	quickSweep(t)
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectWithTimeout(t, addr, 300*time.Millisecond)

	token := soleSessionToken(t, s)
	giveUp := queuePublish(c, token)
	defer giveUp()
	waitForQueuedPublishes(t, s, 1)

	waitForUpTo(t, "the session without a subscription to be retired", 15*time.Second, func() bool {
		return sessionCount(s) == 0
	})
}

// TestSessionFollowsItsAbandonedSubscription bounds the protection. The client
// queues one Publish request and goes away. The subscription's keepalive uses
// the request up, the subscription then times out, and the session is retired
// after it: it does not live on just because a subscription was created in it.
func TestSessionFollowsItsAbandonedSubscription(t *testing.T) {
	quickSweep(t)
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectWithTimeout(t, addr, 300*time.Millisecond)

	token := soleSessionToken(t, s)
	// a keepalive every second tick (100 ms), a lifetime of three ticks
	if err := send(t, c, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 100, RequestedLifetimeCount: 3, RequestedMaxKeepAliveCount: 1}, token, func(ua.Response) error { return nil }); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	giveUp := queuePublish(c, token)
	defer giveUp()
	waitForQueuedPublishes(t, s, 1)

	waitForUpTo(t, "the abandoned session and its subscription to be retired", 15*time.Second, func() bool {
		return sessionCount(s) == 0 && subscriptionCount(s) == 0
	})
}
