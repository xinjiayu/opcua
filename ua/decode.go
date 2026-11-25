// Copyright 2018-2020 opcua authors. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be
// found in the LICENSE file.

package ua

import (
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/gopcua/opcua/errors"
)

var (
	binaryDecoder = reflect.TypeOf((*BinaryDecoder)(nil)).Elem()
	timeType      = reflect.TypeOf(time.Time{})
)

func isBinaryDecoder(val reflect.Value) bool {
	return val.Type().Implements(binaryDecoder)
}

func isTime(val reflect.Value) bool {
	return val.CanConvert(timeType)
}

type BinaryDecoder interface {
	Decode([]byte) (int, error)
}

// maxNestingDepth is how many Variants, DiagnosticInfos and ExtensionObjects
// a value may be nested in. OPC UA Part 6 allows a Variant to be nested one
// level deep, so 100, the number upstream's own comment asked for, leaves
// room for any real message.
//
// These three are the only decoders written by hand that decode values inside
// themselves, and every generated type is a tree, so capping them caps every
// way the decoder can recurse. Without the limit every level of nesting was
// one byte on the wire for a Variant or a DiagnosticInfo and nine for an
// ExtensionObject, each one a step of recursion. A message of about a
// megabyte sent the decoding goroutine past the gigabyte Go allows a stack to
// grow to, which is a fatal error no recover catches, and it happened before
// any check for a session (P12). A two megabyte message of nested
// ExtensionObjects did not quite get there but took half a gigabyte of stack.
//
// The depth belongs to the value being decoded and is passed along with it.
// Nothing in the package counts it: messages are decoded concurrently.
const maxNestingDepth = 100

// errTooDeeplyNested is returned for a value nested deeper than
// maxNestingDepth.
var errTooDeeplyNested = errors.Errorf("nested more than %d Variants, DiagnosticInfos or ExtensionObjects deep", maxNestingDepth)

// depthAware is implemented by the decoders written by hand that decode
// values inside themselves: Variant, DiagnosticInfo and ExtensionObject,
// which are a level of nesting each, and DataValue, which only wraps one.
//
// depth is the number of levels around the value.
type depthAware interface {
	decodeNested(b []byte, depth int) (int, error)
}

// decodeBinary hands b to the Decode of a type written by hand. The types
// that decode values inside themselves are told how deep they are. The others
// cannot lead back to a Variant or a DiagnosticInfo, so for them every
// message starts at the top.
func decodeBinary(val reflect.Value, b []byte, depth int) (int, error) {
	switch v := val.Interface().(type) {
	case depthAware:
		return v.decodeNested(b, depth)
	case BinaryDecoder:
		return v.Decode(b)
	default:
		return 0, errors.Errorf("unsupported type %s", val.Type())
	}
}

func Decode(b []byte, v interface{}) (int, error) {
	val := reflect.ValueOf(v)
	return decode(b, val, val.Type().String(), 0)
}

func decode(b []byte, val reflect.Value, name string, depth int) (n int, err error) {
	if debugCodec {
		fmt.Printf("decode: %s has type %v and is a %s, %d bytes\n", name, val.Type(), val.Type().Kind(), len(b))
		defer func() {
			fmt.Printf("decode: decoded %d bytes into %s\n", n, name)
		}()
	}

	buf := NewBuffer(b)
	switch {
	case isBinaryDecoder(val):
		return decodeBinary(val, b, depth)
	case isTime(val):
		val.Set(reflect.ValueOf(buf.ReadTime()).Convert(val.Type()))
	default:
		// fmt.Printf("decode: %s is a %s\n", name, val.Kind())
		switch val.Kind() {
		case reflect.Bool:
			val.SetBool(buf.ReadBool())
		case reflect.Int8:
			val.SetInt(int64(buf.ReadInt8()))
		case reflect.Uint8:
			val.SetUint(uint64(buf.ReadByte()))
		case reflect.Int16:
			val.SetInt(int64(buf.ReadInt16()))
		case reflect.Uint16:
			val.SetUint(uint64(buf.ReadUint16()))
		case reflect.Int32:
			val.SetInt(int64(buf.ReadInt32()))
		case reflect.Uint32:
			val.SetUint(uint64(buf.ReadUint32()))
		case reflect.Int64:
			val.SetInt(buf.ReadInt64())
		case reflect.Uint64:
			val.SetUint(buf.ReadUint64())
		case reflect.Float32:
			val.SetFloat(float64(buf.ReadFloat32()))
		case reflect.Float64:
			val.SetFloat(buf.ReadFloat64())
		case reflect.String:
			val.SetString(buf.ReadString())
		case reflect.Slice:
			return decodeSlice(b, val, name, depth)
		case reflect.Array:
			return decodeArray(b, val, name, depth)
		case reflect.Ptr:
			return decode(b, val.Elem(), name, depth)
		case reflect.Struct:
			return decodeStruct(b, val, name, depth)
		default:
			return 0, errors.Errorf("unsupported type %s", val.Type())
		}
	}
	return buf.Pos(), buf.Error()
}

func decodeStruct(b []byte, val reflect.Value, name string, depth int) (int, error) {
	pos := 0
	valt := val.Type()
	for i := 0; i < val.NumField(); i++ {
		ft := valt.Field(i)
		fname := name + "." + ft.Name

		// if the field is a pointer we need to create
		// the value before we can marshal data into it.
		f := val.Field(i)
		if f.Type().Kind() == reflect.Ptr {
			f.Set(reflect.New(f.Type().Elem()))
			// fmt.Printf("decode: %s has type %v and has new value %#v\n", fname, f.Type(), f.Interface())
		}

		n, err := decode(b[pos:], f, fname, depth)
		if err != nil {
			return pos, err
		}
		pos += n
	}
	return pos, nil
}

func decodeSlice(b []byte, val reflect.Value, name string, depth int) (int, error) {
	buf := NewBuffer(b)
	n := buf.ReadUint32()
	if buf.Error() != nil {
		return buf.Pos(), buf.Error()
	}

	if n == null {
		return buf.Pos(), nil
	}

	if n > math.MaxInt32 {
		return buf.Pos(), errors.Errorf("array too large: %d > %d", n, math.MaxInt32)
	}

	// elemType is the type of the slice elements
	// e.g. *Foo for []*Foo
	elemType := val.Type().Elem()
	// fmt.Println("elemType: ", elemType.String())

	// fast path for []byte
	if elemType.Kind() == reflect.Uint8 {
		// fmt.Println("decode: []byte fast path")
		val.SetBytes(buf.ReadN(int(n)))
		return buf.Pos(), buf.Error()
	}

	pos := buf.Pos()
	// The element count comes straight off the wire and the only prior check is
	// n > math.MaxInt32, so a tiny message can still declare hundreds of millions
	// of elements and force a huge MakeSlice before any element byte is read. Each
	// element occupies at least one byte on the wire, so a count larger than the
	// bytes remaining can never be satisfied. Reject it here to keep the allocation
	// bounded by the actual input size.
	if int(n) > buf.Len() {
		return pos, errors.Errorf("array length %d exceeds remaining %d bytes", n, buf.Len())
	}

	// a is a slice of []*Foo
	a := reflect.MakeSlice(val.Type(), int(n), int(n))
	for i := 0; i < int(n); i++ {

		// if the slice elements are pointers we need to create
		// them before we can marshal data into them.
		if elemType.Kind() == reflect.Ptr {
			a.Index(i).Set(reflect.New(elemType.Elem()))
		}

		ename := fmt.Sprintf("%s[%d]", name, i)
		m, err := decode(b[pos:], a.Index(i), ename, depth)
		if err != nil {
			return pos, err
		}
		pos += m
	}
	val.Set(a)

	return pos, nil
}

func decodeArray(b []byte, val reflect.Value, name string, depth int) (int, error) {
	buf := NewBuffer(b)
	n := buf.ReadUint32()
	if buf.Error() != nil {
		return buf.Pos(), buf.Error()
	}

	if n == null {
		return buf.Pos(), nil
	}

	if n > math.MaxInt32 {
		return buf.Pos(), errors.Errorf("array too large: %d > %d", n, math.MaxInt32)
	}

	if n > uint32(val.Len()) {
		return buf.Pos(), errors.Errorf("array too large: %d > %d", n, val.Len())
	}

	// elemType is the type of the slice elements
	// e.g. *Foo for []*Foo
	elemType := val.Type().Elem()
	// fmt.Println("elemType: ", elemType.String())

	// fast path for []byte
	if elemType.Kind() == reflect.Uint8 {
		// fmt.Println("decode: []byte fast path")
		reflect.Copy(val, reflect.ValueOf(buf.ReadN(int(n))))
		return buf.Pos(), buf.Error()
	}

	pos := buf.Pos()
	// a is a pointer to an array [n]*Foo, where n is know at compile time
	a := reflect.New(val.Type()).Elem()
	for i := 0; i < int(n); i++ {

		// if the slice elements are pointers we need to create
		// them before we can marshal data into them.
		if elemType.Kind() == reflect.Ptr {
			a.Index(i).Set(reflect.New(elemType.Elem()))
		}

		ename := fmt.Sprintf("%s[%d]", name, i)
		m, err := decode(b[pos:], a.Index(i), ename, depth)
		if err != nil {
			return pos, err
		}
		pos += m
	}
	val.Set(a)

	return pos, nil
}
