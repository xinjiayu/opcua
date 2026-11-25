package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// P7: sessions.
//
// Upstream v0.8.0 handed out sessions without limit and never retired one:
// the timeout negotiated in CreateSession was stored but nothing read it, so
// every session and everything created in it lived until the process ended.
// Any client could open as many sessions as it liked, without authenticating.
//
// The expiry sweep needs the real timeout, so it depends on P7a, which fixes
// the unit of the stored value.

// sessionCount returns how many sessions the server currently holds.
func sessionCount(s *Server) int {
	s.sb.mu.Lock()
	defer s.sb.mu.Unlock()

	return len(s.sb.s)
}

// subscriptionCount returns how many subscriptions the server currently
// holds.
func subscriptionCount(s *Server) int {
	return len(subscriptionIDs(s.SubscriptionService))
}

// connectWithTimeout opens a session that asks for the given session timeout.
func connectWithTimeout(t *testing.T, addr string, timeout time.Duration) *opcua.Client {
	t.Helper()

	c, err := opcua.NewClient("opc.tcp://"+addr, opcua.SessionTimeout(timeout))
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect with a session timeout of %v: %v", timeout, err)
	}
	return c
}

// waitForUpTo polls cond until it holds or the deadline passes.
func waitForUpTo(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSessionBrokerLimit checks the limit where it is enforced.
func TestSessionBrokerLimit(t *testing.T) {
	s := newTestServer(t)

	var first *session
	for i := 1; i <= 64; i++ {
		sess := s.sb.NewSession(time.Minute)
		if sess == nil {
			t.Fatalf("session %d was refused although the limit is 64", i)
		}
		if first == nil {
			first = sess
		}
	}
	if sess := s.sb.NewSession(time.Minute); sess != nil {
		t.Fatalf("session 65 was handed out although the limit is 64")
	}

	// a slot that is freed can be used again
	if err := s.sb.Close(first.AuthTokenID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sess := s.sb.NewSession(time.Minute); sess == nil {
		t.Fatalf("no session after one of the 64 was closed")
	}
}

// TestSessionLimitReached drives the limit through CreateSession.
func TestSessionLimitReached(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	for i := 1; i <= 64; i++ {
		connectWithTimeout(t, addr, time.Minute)
	}

	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = c.Connect(ctx)
	if !errors.Is(err, ua.StatusBadTooManySessions) {
		t.Fatalf("the 65th session got %v, want %v", err, ua.StatusBadTooManySessions)
	}
}

// TestIdleSessionExpires lets a session with a subscription go quiet and
// expects the server to retire both.
func TestIdleSessionExpires(t *testing.T) {
	quickSweep(t)
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectWithTimeout(t, addr, 300*time.Millisecond)

	// the client does not learn of this subscription, so it sends no publish
	// request and the session really is idle from here on
	s.sb.mu.Lock()
	var token *ua.NodeID
	for _, sess := range s.sb.s {
		token = sess.AuthTokenID
	}
	s.sb.mu.Unlock()
	if token == nil {
		t.Fatalf("the server does not hold the session")
	}
	if err := send(t, c, &ua.CreateSubscriptionRequest{RequestedPublishingInterval: 3600000, RequestedLifetimeCount: 1000, RequestedMaxKeepAliveCount: 20}, token, func(ua.Response) error { return nil }); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if sessionCount(s) != 1 || subscriptionCount(s) != 1 {
		t.Fatalf("expected one session and one subscription, got %d and %d", sessionCount(s), subscriptionCount(s))
	}

	waitForUpTo(t, "the idle session to be retired", 15*time.Second, func() bool {
		return sessionCount(s) == 0 && subscriptionCount(s) == 0
	})

	// and the token of the retired session is worth nothing anymore
	if got := sendFault(t, c, &ua.PublishRequest{}, token); got != ua.StatusBadSessionIDInvalid {
		t.Fatalf("a request with the token of the retired session got %v, want %v", got, ua.StatusBadSessionIDInvalid)
	}
}

// TestActiveSessionIsKept sends requests on a session with a short timeout
// for longer than that timeout. The session must survive.
func TestActiveSessionIsKept(t *testing.T) {
	quickSweep(t)
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectWithTimeout(t, addr, 300*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	read := &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	}
	for start := time.Now(); time.Since(start) < time.Second; time.Sleep(50 * time.Millisecond) {
		if _, err := c.Read(ctx, read); err != nil {
			t.Fatalf("Read on a session that is kept in use: %v", err)
		}
	}
	if sessionCount(s) != 1 {
		t.Fatalf("the server holds %d sessions, want the one in use", sessionCount(s))
	}
}

// TestUnactivatedSessionSurvivesUntilTimeout creates a session without
// activating it. It must not be retired before its own timeout has passed,
// and it must be retired afterwards.
func TestUnactivatedSessionSurvivesUntilTimeout(t *testing.T) {
	quickSweep(t)
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

	const timeout = 2 * time.Second
	sess, err := c.CreateSession(ctx, &uasc.SessionConfig{
		SessionName:    "patch-test",
		SessionTimeout: timeout,
		ClientDescription: &ua.ApplicationDescription{
			ApplicationURI:  "urn:gopcua:patch-test",
			ApplicationName: ua.NewLocalizedText("patch test"),
			ApplicationType: ua.ApplicationTypeClient,
			DiscoveryURLs:   []string{},
		},
		// what the client's own configuration has by default. The encoder
		// writes nothing for the nil signature and the server then cannot
		// decode the ActivateSessionRequest.
		LocaleIDs:          []string{},
		UserIdentityToken:  &ua.AnonymousIdentityToken{},
		UserTokenSignature: &ua.SignatureData{},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// a good part of the timeout goes by without a single request
	time.Sleep(timeout / 4)
	if sessionCount(s) != 1 {
		t.Fatalf("the unactivated session was retired %v after it was created", timeout/4)
	}

	// it is still good: the session can be activated late
	if err := c.ActivateSession(ctx, sess); err != nil {
		t.Fatalf("ActivateSession on the unactivated session: %v", err)
	}

	// and from here on it is idle like any other
	waitForUpTo(t, "the unactivated session to be retired after its timeout", 15*time.Second, func() bool {
		return sessionCount(s) == 0
	})
}

// quickSweep makes the server look for timed-out sessions every 20 ms for the
// duration of the test.
func quickSweep(t *testing.T) {
	t.Helper()

	previous := sessionSweepInterval
	sessionSweepInterval = 20 * time.Millisecond
	t.Cleanup(func() { sessionSweepInterval = previous })
}

// TestNewSessionStartsItsClock checks that a session counts as active from
// the moment it is created. Otherwise the sweep would retire it at once for
// lacking a last request.
func TestNewSessionStartsItsClock(t *testing.T) {
	s := newTestServer(t)

	before := time.Now()
	sess := s.sb.NewSession(time.Second)
	if sess == nil {
		t.Fatalf("no session")
	}
	s.sb.mu.Lock()
	lastActivity := sess.lastActivity
	s.sb.mu.Unlock()

	if lastActivity.Before(before) || time.Since(lastActivity) > time.Second {
		t.Fatalf("a new session counts as last active at %v, created around %v", lastActivity, before)
	}
}

// TestSweepRetiresIdleSessionWithSubscriptions drives one sweep by hand: an
// idle session goes together with its subscriptions and monitored items, a
// session that was used recently stays.
func TestSweepRetiresIdleSessionWithSubscriptions(t *testing.T) {
	s := newTestServer(t)
	svc := s.SubscriptionService
	items := s.MonitoredItemService

	idle, busy := newTestSession(s), newTestSession(s)
	idle.cfg.sessionTimeout = time.Second
	busy.cfg.sessionTimeout = time.Second

	idleSub := createSubscription(t, s, idle, 3600000)
	busySub := createSubscription(t, s, busy, 3600000)

	// one monitored item in each subscription, registered the way
	// CreateMonitoredItems does it. DeleteMonitoredItem needs the request of
	// an item to look up where it is registered.
	for i, subID := range []uint32{idleSub, busySub} {
		svc.Mu.Lock()
		sub := svc.Subs[subID]
		svc.Mu.Unlock()
		if sub == nil {
			t.Fatalf("subscription %d was not registered", subID)
		}
		mi := &MonitoredItem{
			ID:  uint32(i + 1),
			Sub: sub,
			Req: &ua.MonitoredItemCreateRequest{ItemToMonitor: &ua.ReadValueID{
				NodeID:      ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime+uint32(i)),
				AttributeID: ua.AttributeIDValue,
			}},
		}
		items.Mu.Lock()
		items.Items[mi.ID] = mi
		items.Nodes[mi.Req.ItemToMonitor.NodeID.String()] = []*MonitoredItem{mi}
		items.Subs[subID] = []*MonitoredItem{mi}
		items.Mu.Unlock()
	}

	// the idle session has not been used for longer than its timeout, the
	// other one just now
	now := time.Now()
	s.sb.mu.Lock()
	idle.lastActivity = now.Add(-2 * time.Second)
	busy.lastActivity = now
	s.sb.mu.Unlock()

	s.sweepSessions(now)

	if s.sb.lookup(idle.AuthTokenID) != nil {
		t.Errorf("the idle session is still in the broker")
	}
	if s.sb.lookup(busy.AuthTokenID) != busy {
		t.Errorf("the session that was just used is gone")
	}
	if hasSubscription(svc, idleSub) {
		t.Errorf("subscription %d of the retired session is still there", idleSub)
	}
	if !hasSubscription(svc, busySub) {
		t.Errorf("subscription %d of the kept session is gone", busySub)
	}
	items.Mu.Lock()
	left := len(items.Items)
	items.Mu.Unlock()
	if left != 1 {
		t.Errorf("%d monitored items are left, want the one of the kept session", left)
	}
}
