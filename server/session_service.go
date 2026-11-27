package server

import (
	"crypto/rand"
	"log"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

const (
	sessionTimeoutMin     = 100 * time.Millisecond
	sessionTimeoutMax     = 1 * time.Hour
	sessionTimeoutDefault = 60 * time.Second

	sessionNonceLength = 32
)

// SessionService implements the Session Service Set.
//
// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6
type SessionService struct {
	srv *Server
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.2
func (s *SessionService) CreateSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CreateSessionRequest](r)
	if err != nil {
		return nil, err
	}

	// Ensure session timeout is reasonable
	timeout := time.Duration(req.RequestedSessionTimeout) * time.Millisecond
	if timeout > sessionTimeoutMax || timeout < sessionTimeoutMin {
		timeout = sessionTimeoutDefault
	}

	nonce := make([]byte, sessionNonceLength)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("error creating session nonce")
		return nil, ua.StatusBadInternalError
	}

	sig, alg, err := sc.NewSessionSignature(req.ClientCertificate, req.ClientNonce)
	if err != nil {
		log.Printf("error creating session signature")
		return nil, ua.StatusBadInternalError
	}

	// New session, only now that nothing else can fail. A session left in the
	// broker would count against the session limit until it times out, and a
	// client can make the steps above fail.
	sess := s.srv.sb.NewSession(timeout)
	if sess == nil {
		return nil, ua.StatusBadTooManySessions
	}
	sess.serverNonce = nonce
	sess.remoteCertificate = req.ClientCertificate

	// The address selects by scheme and port, as in GetEndpoints. Upstream
	// compared it with the configured endpoint URL character for character,
	// which left a client arriving by a host name with an empty list (P8).
	matching_endpoints := s.srv.endpointsFor(req.EndpointURL)

	response := &ua.CreateSessionResponse{
		ResponseHeader:        responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		SessionID:             sess.ID,
		AuthenticationToken:   sess.AuthTokenID,
		RevisedSessionTimeout: float64(sess.cfg.sessionTimeout / time.Millisecond),
		MaxRequestMessageSize: 0, // Not used
		ServerSignature: &ua.SignatureData{
			Signature: sig,
			Algorithm: alg,
		},
		ServerCertificate: s.srv.cfg.certificate,
		ServerNonce:       nonce,
		ServerEndpoints:   matching_endpoints,
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.3
func (s *SessionService) ActivateSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.ActivateSessionRequest](r)
	if err != nil {
		return nil, err
	}

	sess := s.srv.sb.Session(req.RequestHeader.AuthenticationToken)
	if sess == nil {
		return nil, ua.StatusBadSessionIDInvalid
	}

	err = sc.VerifySessionSignature(sess.remoteCertificate, sess.serverNonce, req.ClientSignature.Signature)
	if err != nil {
		if s.srv.cfg.logger != nil {
			s.srv.cfg.logger.Warn("error verifying session signature with nonce: %s", err)
		}
		return nil, ua.StatusBadSecurityChecksFailed
	}

	// Authenticate before rotating the server nonce: the password ciphertext
	// (Part 4, UserNameIdentityToken) is sealed with the CreateSession nonce.
	if err := s.authenticate(sc, sess, req); err != nil {
		return nil, err
	}
	sess.activated = true

	nonce := make([]byte, sessionNonceLength)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("error creating session nonce")
		return nil, ua.StatusBadInternalError
	}
	sess.serverNonce = nonce

	response := &ua.ActivateSessionResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		ServerNonce:    nonce,
		// Results:         []ua.StatusCode{},
		// DiagnosticInfos: []*ua.DiagnosticInfo{},
	}

	return response, nil
}

// authenticate checks the UserIdentityToken against enabled auth modes and,
// for username tokens, the configured UserAuth callback.
func (s *SessionService) authenticate(sc *uasc.SecureChannel, sess *session, req *ua.ActivateSessionRequest) error {
	tokenType, username, password, err := s.identityFrom(sc, sess, req)
	if err != nil {
		return err
	}
	if !s.authModeEnabled(tokenType) {
		return ua.StatusBadIdentityTokenRejected
	}
	switch tokenType {
	case ua.UserTokenTypeAnonymous:
		return nil
	case ua.UserTokenTypeUserName:
		fn := s.srv.cfg.userAuth
		if fn == nil {
			return ua.StatusBadUserAccessDenied
		}
		if err := fn(username, password); err != nil {
			return ua.StatusBadUserAccessDenied
		}
		return nil
	default:
		return ua.StatusBadIdentityTokenRejected
	}
}

func (s *SessionService) authModeEnabled(tokenType ua.UserTokenType) bool {
	for _, a := range s.srv.cfg.enabledAuth {
		if a.tokenType == tokenType {
			return true
		}
	}
	return false
}

// identityFrom unwraps the ActivateSession UserIdentityToken and, for
// UserNameIdentityToken, decrypts the password with the channel key material.
func (s *SessionService) identityFrom(sc *uasc.SecureChannel, sess *session, req *ua.ActivateSessionRequest) (ua.UserTokenType, string, string, error) {
	if req.UserIdentityToken == nil || req.UserIdentityToken.Value == nil {
		return 0, "", "", ua.StatusBadIdentityTokenInvalid
	}
	switch tok := req.UserIdentityToken.Value.(type) {
	case *ua.AnonymousIdentityToken:
		return ua.UserTokenTypeAnonymous, "", "", nil
	case *ua.UserNameIdentityToken:
		// Empty EncryptionAlgorithm means the password is in the clear
		// (None policy, or a client that skipped encryption).
		policyURI := ua.SecurityPolicyURINone
		if tok.EncryptionAlgorithm != "" {
			policyURI = s.userTokenPolicyURI(tok.PolicyID)
			// empty Policy SecurityPolicyURI → Decrypt falls back to channel policy
		}
		pass, err := sc.DecryptUserPassword(policyURI, tok.Password, sess.serverNonce)
		if err != nil {
			return 0, "", "", ua.StatusBadIdentityTokenInvalid
		}
		return ua.UserTokenTypeUserName, tok.UserName, pass, nil
	default:
		return 0, "", "", ua.StatusBadIdentityTokenInvalid
	}
}

// userTokenPolicyURI looks up the SecurityPolicyURI advertised for a PolicyID.
// Unknown IDs return "" so DecryptUserPassword falls back to the channel policy.
func (s *SessionService) userTokenPolicyURI(policyID string) string {
	if policyID == "" {
		return ""
	}
	s.srv.mu.Lock()
	defer s.srv.mu.Unlock()
	for _, ep := range s.srv.endpoints {
		for _, uit := range ep.UserIdentityTokens {
			if uit != nil && uit.PolicyID == policyID {
				return uit.SecurityPolicyURI
			}
		}
	}
	return ""
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.4
func (s *SessionService) CloseSession(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CloseSessionRequest](r)
	if err != nil {
		return nil, err
	}

	// The subscriptions of the session hang off the session object, which the
	// close that follows takes out of the broker, so look it up first.
	sess := s.srv.sb.lookup(req.RequestHeader.AuthenticationToken)

	err = s.srv.sb.Close(req.RequestHeader.AuthenticationToken)
	if err != nil {
		return nil, ua.StatusBadSessionIDInvalid
	}

	// OPC UA Part 4, 5.6.4: with DeleteSubscriptions set, a closed session
	// takes its subscriptions and monitored items with it. Upstream left them
	// registered until each had run through its lifetime count (P11).
	//
	// The session is out of the broker by now, so nothing can be added to it
	// any more and the removal sees everything that belonged to it. The
	// removal runs in the background, as the DeleteSubscriptions handler does
	// it: it takes locks that a stalled change delivery may hold, and the
	// request closing a session should not wait on those.
	if req.DeleteSubscriptions && sess != nil {
		go s.srv.deleteSubscriptionsOf(sess)
	}

	response := &ua.CloseSessionResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.6.5
func (s *SessionService) Cancel(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.CancelRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}
