package server

import (
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// DiscoveryService implements the Discovery Service Set
//
// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4
type DiscoveryService struct {
	srv *Server
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.2
func (s *DiscoveryService) FindServers(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.FindServersRequest](r)
	if err != nil {
		return nil, err
	}

	// A server started without any security policy has no endpoint and so no
	// application description to hand out. Upstream took the first endpoint
	// regardless and ran off the end of the empty list, which killed the
	// process (P13). As in GetEndpoints, an empty list is the answer when
	// there is nothing to list.
	servers := []*ua.ApplicationDescription{}
	if endpoints := s.srv.Endpoints(); len(endpoints) > 0 {
		servers = []*ua.ApplicationDescription{endpoints[0].Server}
	}

	response := &ua.FindServersResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		Servers:        servers,
	}

	return response, nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.3
func (s *DiscoveryService) FindServersOnNetwork(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.FindServersOnNetworkRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.4
func (s *DiscoveryService) GetEndpoints(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.GetEndpointsRequest](r)
	if err != nil {
		return nil, err
	}

	response := &ua.GetEndpointsResponse{
		ResponseHeader: responseHeader(req.RequestHeader.RequestHandle, ua.StatusOK),
		Endpoints:      s.srv.endpointsFor(req.EndpointURL),
	}

	return response, nil
}

// defaultEndpointPort is the port an endpoint address names when it carries
// none.
const defaultEndpointPort = "4840"

// endpointsFor returns the endpoints the server offers at the address a client
// asked about, as an empty list when there are none. An empty list is what it
// has always been on the wire, as opposed to a null array.
//
// The address selects by scheme and port only. Upstream demanded that it equal
// the configured endpoint URL character for character, which it never does
// when the server listens on 0.0.0.0 or the client arrives by a host name, and
// answered with an empty list (P8).
//
// The host of the returned URLs is the one the client used, since that is the
// address it reached the server by. An address without a host gets the
// configured endpoints unchanged.
func (s *Server) endpointsFor(address string) []*ua.EndpointDescription {
	s.mu.Lock()
	configured := s.endpoints
	s.mu.Unlock()

	if address == "" {
		return slices.Clone(configured)
	}
	asked, err := url.Parse(address)
	if err != nil || asked.Hostname() == "" {
		return []*ua.EndpointDescription{}
	}

	matching := make([]*ua.EndpointDescription, 0, len(configured))
	for _, ep := range configured {
		u, err := url.Parse(ep.EndpointURL)
		if err != nil || !strings.EqualFold(u.Scheme, asked.Scheme) || endpointPort(u) != endpointPort(asked) {
			continue
		}
		matching = append(matching, endpointAtHost(ep, u, asked.Hostname()))
	}
	return matching
}

// endpointPort is the port the endpoint URL names, the default one if it
// names none.
func endpointPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return defaultEndpointPort
}

// endpointAtHost returns a copy of the endpoint with its URL pointing at host
// instead. The endpoint itself is shared with every other answer and is not
// touched. u is the parsed EndpointURL of the endpoint.
func endpointAtHost(e *ua.EndpointDescription, u *url.URL, host string) *ua.EndpointDescription {
	if strings.EqualFold(u.Hostname(), host) {
		return e
	}

	at := *u
	if port := u.Port(); port != "" {
		at.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		at.Host = "[" + host + "]"
	} else {
		at.Host = host
	}

	reachable := *e
	reachable.EndpointURL = at.String()
	return &reachable
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.5
func (s *DiscoveryService) RegisterServer(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.RegisterServerRequest](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}

// https://reference.opcfoundation.org/Core/Part4/v105/docs/5.4.6
func (s *DiscoveryService) RegisterServer2(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
	if s.srv.cfg.logger != nil {
		s.srv.cfg.logger.Debug("Handling %T", r)
	}

	req, err := safeReq[*ua.RegisterServer2Request](r)
	if err != nil {
		return nil, err
	}
	return serviceUnsupported(req.RequestHeader), nil
}
