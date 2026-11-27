package server

import (
	"context"
	mrand "math/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gopcua/opcua/ua"
)

type session struct {
	cfg sessionConfig

	ID                *ua.NodeID
	AuthTokenID       *ua.NodeID
	serverNonce       []byte
	remoteCertificate []byte

	// activated is set by ActivateSession after identity checks pass.
	// CreateSession alone must not authorize Read/Write/Browse (P15).
	activated bool

	// lastActivity is when the session last carried a request. It is read by
	// the expiry sweep that Server.Start runs and is guarded by the mutex of
	// the session broker.
	lastActivity time.Time

	// subscriptions is how many subscriptions created in this session are
	// still alive. expireIdle reads it to tell a client that is only waiting
	// for notifications from an abandoned session. It is guarded by the mutex
	// of the session broker and kept up to date by subscriptionAdded and
	// subscriptionRemoved.
	subscriptions int

	PublishRequests chan PubReq
}

type sessionConfig struct {
	sessionTimeout time.Duration
}

type sessionBroker struct {
	// mu protects concurrent modification of s
	mu sync.Mutex

	// s contains all sessions watched by the session broker
	s      map[string]*session
	logger Logger
}

func newSessionBroker(logger Logger) *sessionBroker {
	return &sessionBroker{
		s:      make(map[string]*session),
		logger: logger,
	}
}

// maxSessions is how many sessions the server holds at once, the ones that
// were created but not activated included. OPC UA Part 4, 5.6.2 names
// Bad_TooManySessions as the answer to CreateSession above it. Nothing asks a
// client to authenticate before it creates a session, so without a limit any
// client could hold sessions open without end.
const maxSessions = 64

// sessionSweepInterval is how often the server looks for sessions that timed
// out. A variable so that tests can shorten it.
var sessionSweepInterval = 10 * time.Second

// NewSession registers a session with the given timeout and returns it, or
// nil when the server already holds maxSessions of them.
//
// The timeout has to be known at this point and the session starts its clock
// here: it is visible to the expiry sweep the moment it is in the broker, so
// it must not look idle before the client gets to activate it.
func (sb *sessionBroker) NewSession(timeout time.Duration) *session {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if len(sb.s) >= maxSessions {
		if sb.logger != nil {
			sb.logger.Warn("sessionBroker.NewSession: refusing a session, the limit of %d is reached", maxSessions)
		}
		return nil
	}

	s := &session{
		cfg:             sessionConfig{sessionTimeout: timeout},
		ID:              ua.NewGUIDNodeID(1, uuid.New().String()),
		AuthTokenID:     ua.NewNumericNodeID(0, uint32(mrand.Int31())),
		PublishRequests: make(chan PubReq, 100),
		lastActivity:    time.Now(),
	}
	sb.s[s.AuthTokenID.String()] = s

	return s
}

func (sb *sessionBroker) Close(authToken *ua.NodeID) error {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.s[authToken.String()] == nil {
		if sb.logger != nil {
			sb.logger.Warn("sessionBroker.Close: error looking up session %v", authToken)
		}
	}
	delete(sb.s, authToken.String())

	return nil
}

// lookup returns the session with the given authentication token, or nil.
func (sb *sessionBroker) lookup(authToken *ua.NodeID) *session {
	if authToken == nil {
		return nil
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()

	return sb.s[authToken.String()]
}

// touch records that the session just carried a request, which starts its
// timeout over. Calling it for a session that is gone is harmless.
func (sb *sessionBroker) touch(s *session) {
	sb.mu.Lock()
	s.lastActivity = time.Now()
	sb.mu.Unlock()
}

// subscriptionAdded records that a subscription was created in s. It is called
// with the lock of the subscription service held, which is the order the
// handlers already use when they look the session up; nothing takes that lock
// while holding the one of the broker.
func (sb *sessionBroker) subscriptionAdded(s *session) {
	sb.mu.Lock()
	s.subscriptions++
	sb.mu.Unlock()
}

// subscriptionRemoved is the counterpart of subscriptionAdded. A session that
// has already been retired may still have a subscription deleted afterwards;
// counting on it is harmless.
func (sb *sessionBroker) subscriptionRemoved(s *session) {
	sb.mu.Lock()
	if s.subscriptions > 0 {
		s.subscriptions--
	}
	sb.mu.Unlock()
}

func (sb *sessionBroker) Session(authToken *ua.NodeID) *session {
	s := sb.lookup(authToken)
	if s == nil {
		if sb.logger != nil {
			sb.logger.Warn("sessionBroker.Session: error looking up session %v", authToken)
		}
	}

	return s
}

// expireIdle removes the sessions that have not carried a request for longer
// than their own timeout and returns them. Deciding and removing happen under
// one lock, so a request that turns up in between is either served, because it
// found the session, or refused, because the session is gone for good.
//
// A session that is waiting to have a Publish request answered and still holds
// a subscription is left alone, however long it has been quiet. A client that
// only waits for notifications sends one Publish request and then nothing until
// it is answered, and the answer, a keepalive, comes after publishing interval
// × max keep alive count. That can be longer than the session timeout, so
// going by requests alone retired the session, and its subscriptions, while
// the client was still waiting. A session without a subscription never gets an
// answer, so it is given no protection. Once the client goes away its queued
// requests are used up by keepalives, the subscription times out, and the
// session follows.
func (sb *sessionBroker) expireIdle(now time.Time) []*session {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	var idle []*session
	for token, sess := range sb.s {
		timeout := sess.cfg.sessionTimeout
		if timeout <= 0 {
			timeout = sessionTimeoutDefault
		}
		if now.Sub(sess.lastActivity) <= timeout {
			continue
		}
		if sess.subscriptions > 0 && len(sess.PublishRequests) > 0 {
			continue
		}
		delete(sb.s, token)
		idle = append(idle, sess)
	}
	return idle
}

// sweepSessions retires every session that timed out, together with the
// subscriptions that were created in it. now is passed in to keep the decision
// testable; reapSessions calls this every sessionSweepInterval.
func (s *Server) sweepSessions(now time.Time) {
	for _, sess := range s.sb.expireIdle(now) {
		s.deleteSubscriptionsOf(sess)
	}
}

// reapSessions sweeps for timed-out sessions until ctx ends.
func (s *Server) reapSessions(ctx context.Context) {
	ticker := time.NewTicker(sessionSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sweepSessions(now)
		}
	}
}

// deleteSubscriptionsOf removes every subscription that belongs to sess.
//
// The ids are collected under the lock of the subscription service and the
// subscriptions are deleted outside of it. DeleteSubscription takes that lock
// itself, and the one of the monitored item service, so holding it here would
// deadlock.
func (s *Server) deleteSubscriptionsOf(sess *session) {
	svc := s.SubscriptionService
	if svc == nil {
		return
	}

	svc.Mu.Lock()
	var ids []uint32
	for id, sub := range svc.Subs {
		if sub.Session == sess {
			ids = append(ids, id)
		}
	}
	svc.Mu.Unlock()

	for _, id := range ids {
		svc.DeleteSubscription(id)
	}
}
