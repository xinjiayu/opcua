package ua

// P3: the decoder trusted the element count a message declares for an array.
//
// Upstream v0.8.0 (ua/decode.go decodeSlice) built the destination slice with
// reflect.MakeSlice(n, n) before reading a single element, and only rejected
// counts above math.MaxInt32. A 45 byte ReadRequest declaring 50 million
// NodesToRead entries therefore allocated 400 MB of pointers before the first
// element turned out to be missing. Every element of a UA binary array occupies
// at least one byte on the wire, so a count above the number of bytes left can
// never be satisfied. The check ported from v0.9.3 rejects it up front.

import (
	"encoding/binary"
	"math"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodedReadRequest returns the bytes of a ReadRequest with the given entries
// in NodesToRead. The encoder cannot handle nil pointers, hence the fully
// populated header.
func encodedReadRequest(t *testing.T, nodes ...*ReadValueID) []byte {
	t.Helper()

	req := &ReadRequest{
		RequestHeader: &RequestHeader{
			AuthenticationToken: NewTwoByteNodeID(0),
			RequestHandle:       1,
			AdditionalHeader:    NewExtensionObject(nil),
		},
		TimestampsToReturn: TimestampsToReturnBoth,
		NodesToRead:        nodes,
	}
	b, err := Encode(req)
	require.NoError(t, err)
	return b
}

// readRequestWithDeclaredCount returns the bytes of a ReadRequest whose
// NodesToRead array claims count elements although the message carries none.
func readRequestWithDeclaredCount(t *testing.T, count uint32) []byte {
	t.Helper()

	b := encodedReadRequest(t)
	require.Len(t, b, 45)

	// the array length is the last field of the message
	binary.LittleEndian.PutUint32(b[len(b)-4:], count)
	return b
}

// bytesAllocated runs fn and reports how many bytes it allocated in total,
// including everything it released again before returning.
func bytesAllocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestDecodeRejectsArrayLongerThanInput(t *testing.T) {
	const declared = 50_000_000
	b := readRequestWithDeclaredCount(t, declared)
	require.Len(t, b, 45)

	var req ReadRequest
	var err error
	allocated := bytesAllocated(func() {
		_, err = Decode(b, &req)
	})
	t.Logf("decoding %d bytes that declare %d elements allocated %d bytes", len(b), declared, allocated)

	assert.ErrorContains(t, err, "array length 50000000 exceeds remaining 0 bytes")
	assert.Nil(t, req.NodesToRead)
	// upstream allocated 8 bytes per declared pointer, 400 MB, before it
	// noticed that the message carries no element at all.
	assert.Less(t, allocated, uint64(1<<20), "bytes allocated while decoding")
}

func TestDecodeArrayLengthUpToInputSize(t *testing.T) {
	// every element of a []int8 is exactly one byte on the wire, so an array
	// of n elements needs exactly n bytes after the length field.
	type oneByteElems struct {
		V []int8
	}

	cases := []struct {
		name    string
		count   uint32
		payload []byte
		want    []int8
		errText string
	}{
		{name: "exact fit", count: 3, payload: []byte{1, 2, 3}, want: []int8{1, 2, 3}},
		{name: "smaller than input", count: 2, payload: []byte{1, 2}, want: []int8{1, 2}},
		{name: "one too many", count: 3, payload: []byte{1, 2}, errText: "array length 3 exceeds remaining 2 bytes"},
		{name: "none left", count: 1, errText: "array length 1 exceeds remaining 0 bytes"},
		// kept well below math.MaxInt32: without the check this case would
		// allocate the declared count before failing.
		{name: "far above input size", count: 10_000_000, errText: "array length 10000000 exceeds remaining 0 bytes"},
		{name: "above max int32", count: math.MaxInt32 + 1, errText: "array too large: 2147483648 > 2147483647"},
		{name: "empty", count: 0, want: []int8{}},
		{name: "null", count: 0xffffffff, want: nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := binary.LittleEndian.AppendUint32(nil, c.count)
			b = append(b, c.payload...)

			var got oneByteElems
			_, err := Decode(b, &got)

			if c.errText != "" {
				require.ErrorContains(t, err, c.errText)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got.V)
		})
	}
}

func TestDecodeAcceptsOrdinaryRequest(t *testing.T) {
	// a well-formed ReadRequest must still decode exactly as before
	req := &ReadRequest{
		RequestHeader: &RequestHeader{
			AuthenticationToken: NewNumericNodeID(3, 42),
			RequestHandle:       7,
			AdditionalHeader:    NewExtensionObject(nil),
		},
		MaxAge:             1.5,
		TimestampsToReturn: TimestampsToReturnServer,
		NodesToRead: []*ReadValueID{
			{NodeID: NewNumericNodeID(1, 300), AttributeID: AttributeIDValue, DataEncoding: &QualifiedName{}},
			{NodeID: NewStringNodeID(2, "x"), AttributeID: AttributeIDBrowseName, DataEncoding: &QualifiedName{}},
			{NodeID: NewTwoByteNodeID(85), AttributeID: AttributeIDDisplayName, DataEncoding: &QualifiedName{}},
		},
	}
	b, err := Encode(req)
	require.NoError(t, err)

	var got ReadRequest
	n, err := Decode(b, &got)
	require.NoError(t, err)
	require.Equal(t, len(b), n)
	require.Equal(t, req, &got)
}

// TestDecodeArrayLengthIsBoundedByArrayType records why decodeArray did not
// need the same check: it rejects counts above the length of the fixed-size
// array it decodes into and sizes its allocation from the array type, never
// from the wire. No codec type carries a fixed-size array, so the function is
// not reachable from a message at all.
func TestDecodeArrayLengthIsBoundedByArrayType(t *testing.T) {
	b := binary.LittleEndian.AppendUint32(nil, math.MaxInt32)

	var got struct {
		V [2]int32
	}
	var err error
	allocated := bytesAllocated(func() {
		_, err = Decode(b, &got)
	})

	require.ErrorContains(t, err, "array too large: 2147483647 > 2")
	assert.Less(t, allocated, uint64(1<<20), "bytes allocated while decoding")
}
