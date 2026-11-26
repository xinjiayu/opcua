package server

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uacp"
)

// P10: ReverseHello.
//
// OPC UA Part 6 defines the ReverseHello message for reverse connect: a
// server set up for it accepts the message and connects to the address the
// client names in it.
//
// Upstream v0.8.0 (uacp/conn.go srvhandshake) accepted the message on every
// listener, with nothing to switch it on. When the EndpointUrl in the message
// equalled the endpoint URL the server was configured with, it closed the
// connection to the client and dialled the ServerUri the client had sent. Any
// client that could reach the listening port could make the server open a TCP
// connection to any host and port.
//
// This server is not set up for reverse connect, so it refuses the message
// with BadTcpEndpointUrlInvalid and closes the connection. It does not dial.

// uacpHeaderLen is the size of a UACP message header: type, chunk type and
// message size.
const uacpHeaderLen = 8

// sendReverseHello opens a connection to the server and sends it a
// ReverseHello that asks it to connect to target.
func sendReverseHello(t *testing.T, server, configured, target string) net.Conn {
	t.Helper()

	conn, err := net.DialTimeout("tcp", server, 5*time.Second)
	if err != nil {
		t.Fatalf("cannot connect to the server: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	body, err := (&uacp.ReverseHello{ServerURI: target, EndpointURL: configured}).Encode()
	if err != nil {
		t.Fatalf("cannot encode the ReverseHello: %v", err)
	}
	head, err := (&uacp.Header{MessageType: uacp.MessageTypeReverseHello, ChunkType: uacp.ChunkTypeFinal, MessageSize: uint32(uacpHeaderLen + len(body))}).Encode()
	if err != nil {
		t.Fatalf("cannot encode the message header: %v", err)
	}
	if _, err := conn.Write(append(head, body...)); err != nil {
		t.Fatalf("cannot send the ReverseHello: %v", err)
	}
	return conn
}

// isTimeout reports whether err is a network timeout.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// TestReverseHelloIsRefusedAndNothingIsDialed offers the server an address of
// the test's choosing and checks that it neither connects to it nor keeps the
// connection open.
func TestReverseHelloIsRefusedAndNothingIsDialed(t *testing.T) {
	// the decoy stands in for whatever address a client might name
	decoy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen for the decoy: %v", err)
	}
	t.Cleanup(func() { decoy.Close() })

	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	cases := []struct {
		name       string
		configured string // EndpointUrl in the ReverseHello
	}{
		// upstream dialled for this one
		{"naming-the-configured-endpoint", s.URLs()[0]},
		// upstream already refused this one
		{"naming-another-endpoint", "opc.tcp://127.0.0.1:1"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn := sendReverseHello(t, addr, c.configured, decoy.Addr().String())

			// Did the server connect to the decoy? The accept waits on
			// purpose: only a timeout shows that no connection is coming.
			if err := decoy.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("cannot set a deadline on the decoy: %v", err)
			}
			if dialled, err := decoy.Accept(); err == nil {
				dialled.Close()
				t.Fatalf("the server connected to %s, the address the client supplied", decoy.Addr())
			} else if !isTimeout(err) {
				t.Fatalf("waiting on the decoy: %v", err)
			}

			// The server answers with the refusal ...
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("cannot set a deadline on the connection: %v", err)
			}
			reply := make([]byte, 256)
			n, err := conn.Read(reply)
			if err != nil {
				t.Fatalf("the server did not answer the ReverseHello: %v", err)
			}
			var head uacp.Header
			if _, err := head.Decode(reply[:n]); err != nil || head.MessageType != "ERR" {
				t.Fatalf("the server answered %q, want an ERR message", reply[:n])
			}
			var refusal uacp.Error
			if _, err := refusal.Decode(reply[uacpHeaderLen:n]); err != nil {
				t.Fatalf("cannot decode the refusal: %v", err)
			}
			if got := ua.StatusCode(refusal.ErrorCode); got != ua.StatusBadTCPEndpointURLInvalid {
				t.Fatalf("the server refused with %v, want %v", got, ua.StatusBadTCPEndpointURLInvalid)
			}

			// ... and closes the connection
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("cannot set a deadline on the connection: %v", err)
			}
			if _, err := conn.Read(reply); !errors.Is(err, io.EOF) {
				t.Fatalf("the connection is still open after the refusal, reading returned %v", err)
			}
		})
	}
}

// TestClientIsUnaffectedByRefusingReverseHello runs the client library of this
// module through everything a normal client does: transport handshake, secure
// channel, session, Browse and a subscription that delivers.
func TestClientIsUnaffectedByRefusingReverseHello(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectTestClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := c.Browse(ctx, &ua.BrowseRequest{
		View:                          &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
		RequestedMaxReferencesPerNode: 0,
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          ua.NewNumericNodeID(0, id.ObjectsFolder),
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewTwoByteNodeID(0),
			IncludeSubtypes: true,
			ResultMask:      0x3f, // every field of a reference
		}},
	})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].StatusCode != ua.StatusOK || len(resp.Results[0].References) == 0 {
		t.Fatalf("Browse of ObjectsFolder returned %v", resp.Results)
	}

	node := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)
	notifications := make(chan *opcua.PublishNotificationData, 16)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notifications)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, &ua.MonitoredItemCreateRequest{
		ItemToMonitor:       &ua.ReadValueID{NodeID: node, AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}},
		MonitoringMode:      ua.MonitoringModeReporting,
		RequestedParameters: &ua.MonitoringParameters{ClientHandle: 1, SamplingInterval: -1, QueueSize: 10, DiscardOldest: true},
	}); err != nil {
		t.Fatalf("Monitor: %v", err)
	}
	s.ChangeNotification(node)
	expectNotification(t, "the subscription to deliver", notifications, sub)
}
