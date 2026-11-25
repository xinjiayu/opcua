package ua

// P12: the depth of nesting in the decoder.
//
// A Variant can hold a Variant, a DiagnosticInfo a DiagnosticInfo, and an
// ExtensionObject any registered structure, among them ones that hold an
// ExtensionObject. Every level of that is one step of recursion in the
// decoder. Upstream v0.8.0 had no limit; a comment asked for one of 100. On
// the wire a level costs a single byte for a Variant or a DiagnosticInfo and
// nine for an ExtensionObject, so a message of about a megabyte sent the
// goroutine that decodes it past the gigabyte Go allows for a stack. That is
// a fatal error, "stack overflow", which no recover catches: the process
// dies before the message has even reached the check for a session. Two
// megabytes of nested ExtensionObjects did not quite get there but took half
// a gigabyte of stack.
//
// The depth now travels with the data being decoded and is capped at 100.
// It has to be carried on through DataValue: it wraps a Variant without
// being a level of nesting itself, and it starts a new buffer. Left at zero
// there, two bytes on the wire (a Variant in a DataValue in a Variant) were
// enough to start the count over, and a two megabyte message still killed
// the process.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantLimit is how many levels may be nested inside a value.
const wantLimit = 100

// tooDeep is what the error says when a value is nested deeper than that.
var tooDeep = fmt.Sprintf("nested more than %d", wantLimit)

// killDepth is the number of levels that exhausted the stack upstream.
const killDepth = 1_000_000

// childMark is set in the environment of a child process started by
// isolated.
const childMark = "GOPCUA_PATCH_CHILD"

// isolated runs body in a child process and fails the test if that child does
// not exit cleanly. An exhausted stack is a fatal error: in the test process
// it would take every other test of the package down with it, in a child it
// is an ordinary failure that shows the child's output.
func isolated(t *testing.T, body func()) {
	t.Helper()

	if _, child := os.LookupEnv(childMark); child {
		body()
		return
	}

	exe, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), childMark+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "the child process did not exit cleanly, it crashed or an assertion failed:\n%s", out)
}

// nestingPath is a way of nesting a value in values of its own kind.
type nestingPath string

const (
	variantInVariant              nestingPath = "Variant in Variant"
	variantInDataValue            nestingPath = "Variant in DataValue in Variant"
	variantInExtensionObject      nestingPath = "Variant in ExtensionObject in Variant"
	diagnosticInfo                nestingPath = "DiagnosticInfo in DiagnosticInfo"
	extensionObjectInExtensionObj nestingPath = "ExtensionObject in ExtensionObject"
)

var nestingPaths = []nestingPath{variantInVariant, variantInDataValue, variantInExtensionObject, diagnosticInfo, extensionObjectInExtensionObj}

// nested returns a value with n levels of nesting along the path, as it is
// encoded on the wire, and a zero value to decode it into. The innermost
// value is a Variant with Int32(5), or a DiagnosticInfo with SymbolicId 1.
func (p nestingPath) nested(t *testing.T, n int) ([]byte, interface{}) {
	t.Helper()

	variant := []byte{byte(TypeIDInt32), 5, 0, 0, 0}
	diagnostic := []byte{DiagnosticInfoSymbolicID, 1, 0, 0, 0}
	intoVariant := func() interface{} { return &struct{ V *Variant }{} }

	switch p {
	case variantInVariant:
		return append(bytes.Repeat([]byte{byte(TypeIDVariant)}, n), variant...), intoVariant()

	case variantInDataValue:
		// a Variant of type DataValue, then the mask of a DataValue with a value
		return append(bytes.Repeat([]byte{byte(TypeIDDataValue), DataValueValue}, n), variant...), intoVariant()

	case variantInExtensionObject:
		// a Variant of type ExtensionObject that carries a LiteralOperand,
		// which has a single field: a Variant
		typeID, err := Encode(NewFourByteNodeID(0, id.LiteralOperand_Encoding_DefaultBinary))
		require.NoError(t, err)
		require.Len(t, typeID, 4)

		const level = 10 // the Variant, the four bytes of node id, the encoding, the length
		b := make([]byte, level*n+len(variant))
		for i := 0; i < n; i++ {
			at := level * i
			b[at] = byte(TypeIDExtensionObject)
			copy(b[at+1:], typeID)
			b[at+5] = ExtensionObjectBinary
			binary.LittleEndian.PutUint32(b[at+6:], uint32(level*(n-1-i)+len(variant)))
		}
		copy(b[level*n:], variant)
		return b, intoVariant()

	case diagnosticInfo:
		return append(bytes.Repeat([]byte{DiagnosticInfoInnerDiagnosticInfo}, n), diagnostic...), new(DiagnosticInfo)

	case extensionObjectInExtensionObj:
		// an ExtensionObject that carries a DatagramConnectionTransportDataType,
		// which has a single field: an ExtensionObject. Nine bytes a level and
		// no Variant anywhere.
		typeID, err := Encode(NewFourByteNodeID(0, id.DatagramConnectionTransportDataType_Encoding_DefaultBinary))
		require.NoError(t, err)
		require.Len(t, typeID, 4)

		const level = 9 // the four bytes of node id, the encoding, the length
		// n nested ones inside the outermost, the innermost with an empty body
		b := make([]byte, level*(n+1))
		for i := 0; i <= n; i++ {
			at := level * i
			copy(b[at:], typeID)
			b[at+4] = ExtensionObjectBinary
			binary.LittleEndian.PutUint32(b[at+5:], uint32(level*(n-i)))
		}
		return b, new(ExtensionObject)
	}
	panic("unknown nesting path " + string(p))
}

// counted is how many levels one wrap along the path adds: a Variant or a
// DiagnosticInfo is one level, a Variant inside an ExtensionObject is two.
func (p nestingPath) counted() int {
	if p == variantInExtensionObject {
		return 2
	}
	return 1
}

// decodeNested decodes n levels of nesting along the path and returns the
// error, if any.
func decodeNested(t *testing.T, p nestingPath, n int) error {
	t.Helper()

	b, into := p.nested(t, n)
	_, err := Decode(b, into)
	return err
}

// writeRequestNested returns the bytes of a WriteRequest, as a server
// receives them, whose value is a Variant nested n levels deep.
func writeRequestNested(t *testing.T, n int) []byte {
	t.Helper()

	req := &WriteRequest{
		RequestHeader: &RequestHeader{AuthenticationToken: NewTwoByteNodeID(0), RequestHandle: 1, AdditionalHeader: NewExtensionObject(nil)},
		NodesToWrite: []*WriteValue{{
			NodeID:      NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime),
			AttributeID: AttributeIDValue,
			Value:       &DataValue{EncodingMask: DataValueValue, Value: MustVariant(int32(1))},
		}},
	}
	body, err := Encode(req)
	require.NoError(t, err)

	// the message ends with the mask of the DataValue and then its Variant:
	// one byte of type and four bytes of Int32
	body = body[:len(body)-5]
	body = append(body, bytes.Repeat([]byte{byte(TypeIDVariant)}, n)...)

	typeID, err := Encode(NewFourByteNodeID(0, id.WriteRequest_Encoding_DefaultBinary))
	require.NoError(t, err)
	return append(typeID, body...)
}

// TestDecodeRefusesDeeplyNestedValues feeds every nesting path a value
// nested as deep as it took to exhaust the stack upstream. Each path gets a
// process of its own, because that is what is at stake.
func TestDecodeRefusesDeeplyNestedValues(t *testing.T) {
	for _, p := range nestingPaths {
		t.Run(string(p), func(t *testing.T) {
			isolated(t, func() {
				require.ErrorContains(t, decodeNested(t, p, killDepth), tooDeep)
			})
		})
	}
}

// TestDecodeServiceRefusesDeeplyNestedValue sends the nesting inside a real
// service message, a WriteRequest of about a megabyte, through the entry
// point the server uses.
func TestDecodeServiceRefusesDeeplyNestedValue(t *testing.T) {
	isolated(t, func() {
		_, _, err := DecodeService(writeRequestNested(t, killDepth))
		require.ErrorContains(t, err, tooDeep)
	})
}

// TestDecodeNestingLimit pins the limit for every nesting path: the deepest
// wrapping that stays within the limit decodes completely, one wrap more is
// refused.
func TestDecodeNestingLimit(t *testing.T) {
	for _, p := range nestingPaths {
		t.Run(string(p), func(t *testing.T) {
			wraps := wantLimit / p.counted()
			b, into := p.nested(t, wraps)
			n, err := Decode(b, into)
			require.NoError(t, err)
			require.Equal(t, len(b), n, "the whole nested value was decoded")

			require.ErrorContains(t, decodeNested(t, p, wraps+1), tooDeep)
		})
	}
}

func TestDecodeShallowNestingUnchanged(t *testing.T) {
	// value is encoded and decoded again; check looks at what came out
	cases := []struct {
		name  string
		value interface{}
		into  func() interface{}
		check func(t *testing.T, into interface{})
	}{
		{
			name:  "variant in variant",
			value: MustVariant(MustVariant(int32(7))),
			into:  func() interface{} { return new(Variant) },
			check: func(t *testing.T, into interface{}) {
				require.Equal(t, int32(7), into.(*Variant).Value().(*Variant).Value())
			},
		},
		{
			name:  "variant in variant in variant",
			value: MustVariant(MustVariant(MustVariant("three"))),
			into:  func() interface{} { return new(Variant) },
			check: func(t *testing.T, into interface{}) {
				require.Equal(t, "three", into.(*Variant).Value().(*Variant).Value().(*Variant).Value())
			},
		},
		{
			name:  "array of variants",
			value: MustVariant([]*Variant{MustVariant(int32(1)), MustVariant("two")}),
			into:  func() interface{} { return new(Variant) },
			check: func(t *testing.T, into interface{}) {
				elems := into.(*Variant).Value().([]*Variant)
				require.Len(t, elems, 2)
				require.Equal(t, int32(1), elems[0].Value())
				require.Equal(t, "two", elems[1].Value())
			},
		},
		{
			name:  "variant in data value",
			value: &DataValue{EncodingMask: DataValueValue, Value: MustVariant(MustVariant("in"))},
			into:  func() interface{} { return new(DataValue) },
			check: func(t *testing.T, into interface{}) {
				require.Equal(t, "in", into.(*DataValue).Value.Value().(*Variant).Value())
			},
		},
		{
			name:  "data value in variant",
			value: MustVariant(&DataValue{EncodingMask: DataValueValue, Value: MustVariant(int32(9))}),
			into:  func() interface{} { return new(Variant) },
			check: func(t *testing.T, into interface{}) {
				require.Equal(t, int32(9), into.(*Variant).Value().(*DataValue).Value.Value())
			},
		},
		{
			name:  "extension object in variant",
			value: MustVariant(NewExtensionObject(&LiteralOperand{Value: MustVariant(int32(11))})),
			into:  func() interface{} { return new(Variant) },
			check: func(t *testing.T, into interface{}) {
				require.Equal(t, int32(11), into.(*Variant).Value().(*ExtensionObject).Value.(*LiteralOperand).Value.Value())
			},
		},
		{
			name: "diagnostic info in diagnostic info",
			value: &DiagnosticInfo{
				EncodingMask:        DiagnosticInfoAdditionalInfo | DiagnosticInfoInnerDiagnosticInfo,
				AdditionalInfo:      "outer",
				InnerDiagnosticInfo: &DiagnosticInfo{EncodingMask: DiagnosticInfoSymbolicID, SymbolicID: 3},
			},
			into: func() interface{} { return new(DiagnosticInfo) },
			check: func(t *testing.T, into interface{}) {
				got := into.(*DiagnosticInfo)
				require.Equal(t, "outer", got.AdditionalInfo)
				require.Equal(t, int32(3), got.InnerDiagnosticInfo.SymbolicID)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := Encode(c.value)
			require.NoError(t, err)

			into := c.into()
			n, err := Decode(b, into)
			require.NoError(t, err)
			require.Equal(t, len(b), n)

			c.check(t, into)

			// and nothing changed that putting it on the wire again would show
			again, err := Encode(into)
			require.NoError(t, err)
			require.Equal(t, b, again)
		})
	}
}

// TestDecodeServiceShallowNestingUnchanged decodes a real service message
// with a nested value in it, the way a server does.
func TestDecodeServiceShallowNestingUnchanged(t *testing.T) {
	nested := MustVariant(MustVariant(MustVariant(int32(21))))
	req := &WriteRequest{
		RequestHeader: &RequestHeader{AuthenticationToken: NewTwoByteNodeID(0), AdditionalHeader: NewExtensionObject(nil)},
		NodesToWrite:  []*WriteValue{{NodeID: NewNumericNodeID(3, 42), AttributeID: AttributeIDValue, Value: &DataValue{EncodingMask: DataValueValue, Value: nested}}},
	}
	body, err := Encode(req)
	require.NoError(t, err)
	typeID, err := Encode(NewFourByteNodeID(0, id.WriteRequest_Encoding_DefaultBinary))
	require.NoError(t, err)

	_, got, err := DecodeService(append(typeID, body...))
	require.NoError(t, err)

	value := got.(*WriteRequest).NodesToWrite[0].Value.Value
	require.Equal(t, int32(21), value.Value().(*Variant).Value().(*Variant).Value())
}

// TestDecodeNestingDepthIsNotShared decodes a value that is too deep and one
// that is fine at the same time from several goroutines. The depth belongs to
// the value being decoded; nothing of the package counts it, or one message
// could push another over the limit.
func TestDecodeNestingDepthIsNotShared(t *testing.T) {
	tooDeepBytes, _ := variantInVariant.nested(t, wantLimit+1)
	fineBytes, _ := variantInVariant.nested(t, wantLimit-1)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, err := Decode(tooDeepBytes, &struct{ V *Variant }{})
				assert.ErrorContains(t, err, tooDeep)

				into := &struct{ V *Variant }{}
				_, err = Decode(fineBytes, into)
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()
}
