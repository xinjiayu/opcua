package server

// Shared helpers for the regression tests of the security patches carried by
// this copy of gopcua. The tests themselves live in patch_p<N>_*_test.go and
// are referenced from PATCHES.md in the module root.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// crashProbeEnv carries the name of the case a crash-probe child process is
// supposed to run. Its presence marks the process as such a child.
const crashProbeEnv = "GOPCUA_PATCH_CRASH_PROBE"

// crashTimeout bounds how long the parent waits for a probe child.
const crashTimeout = 60 * time.Second

// crashProbe runs body in a child process and fails the test if that child
// does not exit cleanly. It is the same idea as the Go standard library's
// TestCrasher: turn "the process died" into an ordinary assertion failure.
//
// Most of the bugs patched here killed the whole process, either with a panic
// on the single goroutine that serves requests or with a panic in a goroutine
// nobody recovers. The test runner cannot catch either, so a plain test would
// take every other test of the package down with it.
//
// name selects the case when a test probes several inputs. The child is
// started with crashProbeEnv set to "<test>/<case>" and only executes the
// matching body; the other cases return at once. The child is not exited
// by hand, so failed assertions and race reports inside body still reach the
// parent through the regular exit code.
func crashProbe(t *testing.T, name string, body func()) {
	t.Helper()

	probe := t.Name() + "/" + name
	if selected, ok := os.LookupEnv(crashProbeEnv); ok {
		if selected == probe {
			body()
		}
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("%s: cannot locate the test binary: %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), crashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), crashProbeEnv+"="+probe)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s: probe child did not finish within %s\n%s", name, crashTimeout, out)
	}
	if err != nil {
		t.Fatalf("%s: probe child did not exit cleanly, the process crashed or an assertion failed: %v\n%s", name, err, out)
	}
}

// newTestServer returns a server with the built-in service handlers
// registered but not listening. Requests are fed to the handlers directly.
func newTestServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	s := New(opts...)
	s.initHandlers()
	return s
}

// newTestSession registers a session with the default timeout with the
// broker of s.
func newTestSession(s *Server) *session {
	return s.sb.NewSession(sessionTimeoutDefault)
}

// header returns a request header that carries the authentication token of
// the given session.
func header(sess *session) *ua.RequestHeader {
	return &ua.RequestHeader{AuthenticationToken: sess.AuthTokenID, RequestHandle: 1}
}

// call dispatches req to the handler registered for typeID and returns the
// response, failing the test if the handler reports an error.
func call(t *testing.T, s *Server, typeID uint16, req ua.Request) ua.Response {
	t.Helper()
	h, ok := s.handlers[typeID]
	if !ok {
		t.Fatalf("no handler registered for service %d", typeID)
	}
	resp, err := h(nil, req, 1)
	if err != nil {
		t.Fatalf("service %d returned an error instead of a response: %v", typeID, err)
	}
	return resp
}

// startTestServer starts s on a loopback port chosen by the operating system
// and returns the address it listens on. The server is stopped when the test
// ends.
func startTestServer(t *testing.T, s *Server) string {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		s.Close()
	})
	if err := s.Start(ctx); err != nil {
		t.Fatalf("cannot start server: %v", err)
	}
	return s.l.Addr().String()
}

// freePort returns a TCP port on the loopback interface that was free a
// moment ago. Tests use it so that the endpoint URL a server is configured
// with names the port it actually listens on.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot find a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// newLoopbackServer returns an unstarted server that listens on a free port of
// the loopback interface, with security "None" and anonymous authentication
// enabled.
func newLoopbackServer(t *testing.T, opts ...Option) *Server {
	t.Helper()

	return New(append([]Option{
		EndPoint("127.0.0.1", freePort(t)),
		EnableSecurity("None", ua.MessageSecurityModeNone),
		EnableAuthMode(ua.UserTokenTypeAnonymous),
	}, opts...)...)
}

// encodeOrDie encodes v and turns a panic inside the encoder, which does not
// tolerate nil pointers, into an error.
func encodeOrDie(t *testing.T, v any) (b []byte) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("encoding %T panicked: %v", v, r)
		}
	}()
	b, err := ua.Encode(v)
	if err != nil {
		t.Fatalf("cannot encode %T: %v", v, err)
	}
	return b
}

// requireRoundTrips encodes req the way the client does, decodes it and
// encodes the result again, failing the test unless both encodings are
// identical.
//
// The encoder writes nothing for a nil pointer field while the decoder always
// expects the whole struct, so a request with such a field is undecodable and
// makes the server drop the secure channel. The guard keeps a broken test
// request from looking like a server bug. Encoding again is the criterion
// because decoding fills in fields the original left empty, such as the
// Variant inside a DataValue.
func requireRoundTrips(t *testing.T, req ua.Request) {
	t.Helper()

	req.SetHeader(&ua.RequestHeader{AuthenticationToken: ua.NewTwoByteNodeID(0), AdditionalHeader: ua.NewExtensionObject(nil)})

	body := encodeOrDie(t, req)
	into := reflect.New(reflect.TypeOf(req).Elem()).Interface()
	if _, err := ua.Decode(body, into); err != nil {
		t.Fatalf("%T does not survive decoding: %v", req, err)
	}
	again := encodeOrDie(t, into)
	if !bytes.Equal(body, again) {
		t.Fatalf("%T does not survive a decode round trip:\n sent     %x\n received %x", req, body, again)
	}
}

// dialTestClient opens a secure channel to addr without creating a session.
// The client is closed when the test ends.
func dialTestClient(t *testing.T, addr string) *opcua.Client {
	t.Helper()

	c, err := opcua.NewClient("opc.tcp://" + addr)
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Dial(ctx); err != nil {
		t.Fatalf("cannot dial %s: %v", addr, err)
	}
	return c
}

// sendFault sends req with the given authentication token and returns the
// status carried by the ServiceFault the server answers with. The test fails
// if the server answers with anything else or not at all.
//
// The client hands a response whose service result is not OK to the callback
// first and returns that result as the error afterwards, so the response, not
// the error, is what gets checked.
func sendFault(t *testing.T, c *opcua.Client, req ua.Request, token *ua.NodeID) ua.StatusCode {
	t.Helper()

	var got ua.StatusCode
	seen := false
	err := send(t, c, req, token, func(resp ua.Response) error {
		fault, ok := resp.(*ua.ServiceFault)
		if !ok {
			return fmt.Errorf("expected *ua.ServiceFault, got %s", describeResponse(resp))
		}
		got = fault.ResponseHeader.ServiceResult
		seen = true
		return nil
	})
	if !seen {
		t.Fatalf("%T got no ServiceFault: %v", req, err)
	}
	return got
}

// send sends req with the given authentication token and hands the response
// to check.
func send(t *testing.T, c *opcua.Client, req ua.Request, token *ua.NodeID, check func(ua.Response) error) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.SecureChannel().SendRequest(ctx, req, token, func(resp ua.Response) error {
		return check(resp)
	})
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// testLogger records the formatted lines the server logs, so tests can
// assert on them. It implements Logger.
type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) add(level, msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+": "+fmt.Sprintf(msg, args...))
}

func (l *testLogger) Debug(msg string, args ...any) { l.add("DEBUG", msg, args...) }
func (l *testLogger) Info(msg string, args ...any)  { l.add("INFO", msg, args...) }
func (l *testLogger) Warn(msg string, args ...any)  { l.add("WARN", msg, args...) }
func (l *testLogger) Error(msg string, args ...any) { l.add("ERROR", msg, args...) }

// recorded returns a copy of the lines logged so far.
func (l *testLogger) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}
