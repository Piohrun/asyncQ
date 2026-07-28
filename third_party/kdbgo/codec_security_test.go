package kdb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func testFrame(order binary.ByteOrder, marker byte, request byte, compressed byte, body []byte) []byte {
	frame := make([]byte, 8+len(body))
	frame[0] = marker
	frame[1] = request
	frame[2] = compressed
	order.PutUint32(frame[4:8], uint32(len(frame)))
	copy(frame[8:], body)
	return frame
}

func decodeBytes(raw []byte) (*K, ReqType, error) {
	return Decode(bufio.NewReader(bytes.NewReader(raw)))
}

func wireType(qtype int8) byte {
	return byte(qtype)
}

func TestRepresentativeRoundTrips(t *testing.T) {
	table := NewTable(
		[]string{"a", "b"},
		[]*K{IntV([]int32{2}), IntV([]int32{3})},
	)
	expectedTable := []byte{
		0x01, 0x00, 0x00, 0x00, 0x2f, 0x00, 0x00, 0x00,
		0x62, 0x00, 0x63, 0x0b, 0x00, 0x02, 0x00, 0x00,
		0x00, 0x61, 0x00, 0x62, 0x00, 0x00, 0x00, 0x02,
		0x00, 0x00, 0x00, 0x06, 0x00, 0x01, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x00, 0x00, 0x06, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
	}
	var encoded bytes.Buffer
	if err := Encode(&encoded, ASYNC, table); err != nil {
		t.Fatalf("encode table: %v", err)
	}
	if !bytes.Equal(encoded.Bytes(), expectedTable) {
		t.Fatalf("table wire fixture mismatch:\nwant %v\ngot  %v", expectedTable, encoded.Bytes())
	}
	decoded, request, err := decodeBytes(encoded.Bytes())
	if err != nil {
		t.Fatalf("decode table: %v", err)
	}
	if request != ASYNC || !reflect.DeepEqual(decoded, table) {
		t.Fatalf("table round trip mismatch: request=%d value=%#v", request, decoded)
	}

	bools := make([]bool, 2000)
	for i := range bools {
		bools[i] = true
	}
	encoded.Reset()
	if err := Encode(&encoded, SYNC, &K{KB, NONE, bools}); err != nil {
		t.Fatalf("encode compressed vector: %v", err)
	}
	if encoded.Bytes()[2] != 1 {
		t.Fatal("representative compressible vector was not compressed")
	}
	expectedCompressedBools := []byte{
		0x01, 0x01, 0x01, 0x00, 0x26, 0x00, 0x00, 0x00,
		0xde, 0x07, 0x00, 0x00, 0x00, 0x01, 0x00, 0xd0,
		0x07, 0x00, 0x00, 0x01, 0x01, 0xff, 0x00, 0xff,
		0x00, 0xff, 0x00, 0xff, 0x00, 0xff, 0x00, 0xff,
		0x00, 0xff, 0x00, 0xff, 0x00, 0xc5,
	}
	if !bytes.Equal(encoded.Bytes(), expectedCompressedBools) {
		t.Fatalf("compressed q fixture mismatch:\nwant %v\ngot  %v", expectedCompressedBools, encoded.Bytes())
	}
	decoded, request, err = decodeBytes(encoded.Bytes())
	if err != nil {
		t.Fatalf("decode compressed vector %v: %v", encoded.Bytes(), err)
	}
	if request != SYNC || !reflect.DeepEqual(decoded, &K{KB, NONE, bools}) {
		t.Fatalf("compressed round trip mismatch: request=%d", request)
	}

	bigEndian := testFrame(binary.BigEndian, 0, byte(RESPONSE), 0, []byte{wireType(-KI), 0x01, 0x02, 0x03, 0x04})
	decoded, request, err = decodeBytes(bigEndian)
	if err != nil {
		t.Fatalf("decode big-endian atom: %v", err)
	}
	if request != RESPONSE || !reflect.DeepEqual(decoded, Int(0x01020304)) {
		t.Fatalf("big-endian decode mismatch: request=%d value=%#v", request, decoded)
	}
}

func TestCompressedBackReferenceHashProgression(t *testing.T) {
	value := NewList(
		NewList(
			NewList(Symbol("s864")),
			Symbol("s525"),
			Int(-395912471),
			&K{Type: KC, Attr: NONE, Data: strings.Repeat("x", 55)},
		),
		&K{Type: KC, Attr: NONE, Data: strings.Repeat("x", 54)},
		Int(579342269),
		Atom(-KB, true),
	)
	var frame bytes.Buffer
	if err := Encode(&frame, ASYNC, value); err != nil {
		t.Fatal(err)
	}
	if frame.Bytes()[2] != 1 {
		t.Fatal("compression regression fixture was not compressed")
	}
	decoded, _, err := decodeBytes(frame.Bytes())
	if err != nil {
		t.Fatalf("compressed regression fixture failed to decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatalf("compressed regression round trip mismatch:\nwant %s\ngot  %s", value, decoded)
	}
}

func TestSortedDictionaryWireFixtureAndLogicalLength(t *testing.T) {
	sorted := &K{
		Type: SD,
		Attr: NONE,
		Data: Dict{
			Key:   &K{Type: KS, Attr: SORTED, Data: []string{"a", "b"}},
			Value: IntV([]int32{2, 3}),
		},
	}
	expected := []byte{
		0x01, 0x00, 0x00, 0x00, 0x21, 0x00, 0x00, 0x00,
		0x7f, 0x0b, 0x01, 0x02, 0x00, 0x00, 0x00, 0x61,
		0x00, 0x62, 0x00, 0x06, 0x00, 0x02, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00,
		0x00,
	}
	var frame bytes.Buffer
	if err := Encode(&frame, ASYNC, sorted); err != nil {
		t.Fatalf("encode sorted dictionary: %v", err)
	}
	if !bytes.Equal(frame.Bytes(), expected) {
		t.Fatalf("sorted dictionary wire fixture mismatch:\nwant %v\ngot  %v", expected, frame.Bytes())
	}
	if got := sorted.Len(); got != 2 {
		t.Fatalf("raw sorted dictionary length = %d, want 2", got)
	}
	decoded, request, err := decodeBytes(frame.Bytes())
	if err != nil {
		t.Fatalf("decode sorted dictionary: %v", err)
	}
	if request != ASYNC || decoded.Type != XD || decoded.Attr != SORTED || decoded.Len() != 2 {
		t.Fatalf("unexpected normalized sorted dictionary: request=%d value=%#v length=%d", request, decoded, decoded.Len())
	}
	var reencoded bytes.Buffer
	if err := Encode(&reencoded, ASYNC, decoded); err != nil {
		t.Fatalf("re-encode normalized sorted dictionary: %v", err)
	}
	if !bytes.Equal(reencoded.Bytes(), expected) {
		t.Fatalf("normalized sorted dictionary lost its wire type:\nwant %v\ngot  %v", expected, reencoded.Bytes())
	}
}

func TestDecodeCharacterVectorStringLimitExactBoundary(t *testing.T) {
	encode := func(value string) []byte {
		t.Helper()
		var frame bytes.Buffer
		if err := Encode(&frame, RESPONSE, &K{Type: KC, Attr: NONE, Data: value}); err != nil {
			t.Fatal(err)
		}
		return frame.Bytes()
	}
	limits := DefaultDecodeLimits()
	limits.MaxStringBytes = 3
	if value, _, err := DecodeWithLimits(bufio.NewReader(bytes.NewReader(encode("abc"))), limits); err != nil {
		t.Fatalf("exact-boundary char vector rejected: %v", err)
	} else if value.Data != "abc" {
		t.Fatalf("unexpected exact-boundary value: %#v", value)
	}
	if _, _, err := DecodeWithLimits(bufio.NewReader(bytes.NewReader(encode("abcd"))), limits); !errors.Is(err, ErrBadMsg) {
		t.Fatalf("over-limit char vector error = %v, want ErrBadMsg", err)
	}
}

func TestTemporalAtomSentinelsPreserveRawWireRepresentations(t *testing.T) {
	tests := []struct {
		name    string
		qtype   int8
		payload []byte
		want    interface{}
	}{
		{name: "date null", qtype: -KD, payload: int32Bytes(Ni), want: Ni},
		{name: "date infinity", qtype: -KD, payload: int32Bytes(Wi), want: Wi},
		{name: "minute null", qtype: -KU, payload: int32Bytes(Ni), want: Ni},
		{name: "second infinity", qtype: -KV, payload: int32Bytes(Wi), want: Wi},
		{name: "time null", qtype: -KT, payload: int32Bytes(Ni), want: Ni},
		{
			name:    "datetime null",
			qtype:   -KZ,
			payload: uint64Bytes(0x7ff8000000000001),
			want:    math.Float64frombits(0x7ff8000000000001),
		},
		{name: "datetime infinity", qtype: -KZ, payload: uint64Bytes(math.Float64bits(math.Inf(1))), want: math.Inf(1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, append([]byte{byte(test.qtype)}, test.payload...))
			decoded, request, err := decodeBytes(raw)
			if err != nil {
				t.Fatalf("decode temporal atom: %v", err)
			}
			if request != ASYNC || decoded.Type != test.qtype {
				t.Fatalf("decoded request/type = %d/%d, want %d/%d", request, decoded.Type, ASYNC, test.qtype)
			}
			switch want := test.want.(type) {
			case int32:
				got, ok := decoded.Data.(int32)
				if !ok || got != want {
					t.Fatalf("decoded raw temporal value = %#v, want int32(%d)", decoded.Data, want)
				}
			case float64:
				got, ok := decoded.Data.(float64)
				if !ok || math.Float64bits(got) != math.Float64bits(want) {
					t.Fatalf("decoded raw datetime bits = %#v, want %#x", decoded.Data, math.Float64bits(want))
				}
			}
			var reencoded bytes.Buffer
			if err := Encode(&reencoded, ASYNC, decoded); err != nil {
				t.Fatalf("re-encode temporal atom: %v", err)
			}
			if !bytes.Equal(reencoded.Bytes(), raw) {
				t.Fatalf("temporal atom did not round trip exactly:\nwant %v\ngot  %v", raw, reencoded.Bytes())
			}
		})
	}
}

func uint32Bytes(value uint32) []byte {
	result := make([]byte, 4)
	binary.LittleEndian.PutUint32(result, value)
	return result
}

func int32Bytes(value int32) []byte {
	return uint32Bytes(uint32(value))
}

func uint64Bytes(value uint64) []byte {
	result := make([]byte, 8)
	binary.LittleEndian.PutUint64(result, value)
	return result
}

func TestDecodeRejectsNegativeSignedCountsEvenWithPermissiveLimits(t *testing.T) {
	limits := DefaultDecodeLimits()
	limits.MaxVectorElements = math.MaxUint32
	limits.MaxObjects = math.MaxUint32
	tests := []struct {
		name string
		body []byte
	}{
		{name: "generic list", body: []byte{byte(K0), 0, 0, 0, 0, 0x80}},
		{name: "projection", body: []byte{byte(KPROJ), 0, 0, 0, 0x80}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, test.body)
			_, _, err := DecodeWithLimits(bufio.NewReader(bytes.NewReader(raw)), limits)
			if !errors.Is(err, ErrBadMsg) || !strings.Contains(err.Error(), "int32 count") {
				t.Fatalf("negative signed count error = %v", err)
			}
		})
	}
}

func malformedCorpus() map[string][]byte {
	little := func(body []byte) []byte {
		return testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, body)
	}
	compressedSize := func(size int) []byte {
		frame := make([]byte, size)
		if size >= 1 {
			frame[0] = 1
		}
		if size >= 3 {
			frame[2] = 1
		}
		if size >= 8 {
			binary.LittleEndian.PutUint32(frame[4:8], uint32(size))
		}
		return frame
	}
	hugeExpandedBody := make([]byte, 6)
	binary.LittleEndian.PutUint32(hugeExpandedBody[:4], ^uint32(0))
	hugeHeader := make([]byte, 8)
	hugeHeader[0] = 1
	binary.LittleEndian.PutUint32(hugeHeader[4:], uint32(defaultMaxFrame+1))

	badCount := func(qtype int8) []byte {
		return little([]byte{byte(qtype), 0, 0xff, 0xff, 0xff, 0xff})
	}
	return map[string][]byte{
		"compressed-8":             compressedSize(8),
		"compressed-9":             compressedSize(9),
		"compressed-10":            compressedSize(10),
		"huge-expanded-size":       testFrame(binary.LittleEndian, 1, byte(ASYNC), 1, hugeExpandedBody),
		"huge-wire-header":         hugeHeader,
		"huge-fixed-vector":        badCount(KI),
		"huge-generic-list":        badCount(K0),
		"huge-symbol-vector":       badCount(KS),
		"huge-projection":          little([]byte{byte(KPROJ), 0xff, 0xff, 0xff, 0xff}),
		"truncated-bool-atom":      little([]byte{wireType(-KB)}),
		"truncated-guid-atom":      little([]byte{wireType(-UU), 1, 2}),
		"truncated-int-vector":     little([]byte{byte(KI), 0, 2, 0, 0, 0, 1}),
		"unterminated-symbol":      little([]byte{wireType(-KS), 'x'}),
		"unterminated-symbol-list": little([]byte{byte(KS), 0, 1, 0, 0, 0, 'x'}),
		"invalid-boolean":          little([]byte{wireType(-KB), 2}),
		"invalid-attribute":        little([]byte{byte(KI), 5, 0, 0, 0, 0}),
		"invalid-primitive":        little([]byte{byte(KFUNCBP), 255}),
		"unsupported-type":         little([]byte{3}),
		"trailing-data":            little([]byte{wireType(-KB), 1, 0}),
	}
}

func TestMalformedDecodeCorpusNeverPanics(t *testing.T) {
	for name, raw := range malformedCorpus() {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeBytes(raw); err == nil {
				t.Fatal("malformed frame decoded successfully")
			}
		})
	}
}

func TestInvalidHeaderFields(t *testing.T) {
	validBody := []byte{wireType(-KB), 1}
	tests := []struct {
		name       string
		marker     byte
		request    byte
		compressed byte
		reserved   byte
	}{
		{name: "endian", marker: 2},
		{name: "request", marker: 1, request: 3},
		{name: "compression", marker: 1, compressed: 2},
		{name: "reserved", marker: 1, reserved: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := testFrame(binary.LittleEndian, test.marker, test.request, test.compressed, validBody)
			frame[3] = test.reserved
			if _, _, err := decodeBytes(frame); err == nil {
				t.Fatal("invalid header decoded successfully")
			}
		})
	}
}

func TestFrameIsolationPreventsCrossFrameSmuggling(t *testing.T) {
	first := testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, []byte{wireType(-KI), 1})
	var second bytes.Buffer
	if err := Encode(&second, RESPONSE, Long(42)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(bytes.NewReader(append(first, second.Bytes()...)))
	if _, _, err := Decode(reader); err == nil {
		t.Fatal("truncated first frame decoded using bytes from the second")
	}
	value, request, err := Decode(reader)
	if err != nil {
		t.Fatalf("second frame was not preserved: %v", err)
	}
	if request != RESPONSE || !reflect.DeepEqual(value, Long(42)) {
		t.Fatalf("unexpected second frame: request=%d value=%#v", request, value)
	}
}

func TestDepthAndObjectLimits(t *testing.T) {
	nested := Long(1)
	for i := 0; i < 5; i++ {
		nested = NewList(nested)
	}
	var frame bytes.Buffer
	if err := Encode(&frame, ASYNC, nested); err != nil {
		t.Fatal(err)
	}
	limits := DefaultDecodeLimits()
	limits.MaxDepth = 3
	if _, _, err := DecodeWithLimits(bufio.NewReader(bytes.NewReader(frame.Bytes())), limits); err == nil {
		t.Fatal("excessive nesting decoded successfully")
	}

	frame.Reset()
	if err := Encode(&frame, ASYNC, NewList(Long(1), Long(2), Long(3))); err != nil {
		t.Fatal(err)
	}
	limits = DefaultDecodeLimits()
	limits.MaxObjects = 2
	if _, _, err := DecodeWithLimits(bufio.NewReader(bytes.NewReader(frame.Bytes())), limits); err == nil {
		t.Fatal("excessive object count decoded successfully")
	}
}

func objectBody(t *testing.T, value *K) []byte {
	t.Helper()
	var frame bytes.Buffer
	if err := Encode(&frame, ASYNC, value); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), frame.Bytes()[8:]...)
}

func TestMalformedDictionaryAndTableShapes(t *testing.T) {
	dictBody := []byte{byte(XD)}
	dictBody = append(dictBody, objectBody(t, SymbolV([]string{"a"}))...)
	dictBody = append(dictBody, objectBody(t, IntV([]int32{1, 2}))...)
	if _, _, err := decodeBytes(testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, dictBody)); err == nil {
		t.Fatal("mismatched dictionary decoded successfully")
	}

	badTableDict := []byte{byte(XD)}
	badTableDict = append(badTableDict, objectBody(t, IntV([]int32{1}))...)
	badTableDict = append(badTableDict, objectBody(t, NewList(IntV([]int32{1})))...)
	tableBody := append([]byte{byte(XT), 0}, badTableDict...)
	if _, _, err := decodeBytes(testFrame(binary.LittleEndian, 1, byte(ASYNC), 0, tableBody)); err == nil {
		t.Fatal("malformed table decoded successfully")
	}
}

func TestLegacyUncompressMalformedInputsReturnNil(t *testing.T) {
	for size := 0; size <= 12; size++ {
		if got := Uncompress(make([]byte, size)); got != nil {
			t.Fatalf("malformed compressed body of size %d returned %d bytes", size, len(got))
		}
	}
	body := make([]byte, 6)
	binary.LittleEndian.PutUint32(body, ^uint32(0))
	if got := Uncompress(body); got != nil {
		t.Fatal("huge expanded size returned data")
	}
}

func TestEncodeLimitsRejectUntrustedGraphsBeforeWrite(t *testing.T) {
	cycle := &K{Type: K0, Attr: NONE}
	cycle.Data = []*K{cycle}
	tests := []struct {
		name   string
		value  *K
		limits func() EncodeLimits
	}{
		{name: "cycle", value: cycle, limits: DefaultEncodeLimits},
		{name: "nil-item", value: NewList(nil), limits: DefaultEncodeLimits},
		{name: "invalid-dict", value: NewDict(SymbolV([]string{"a"}), IntV([]int32{1, 2})), limits: DefaultEncodeLimits},
		{name: "invalid-table", value: NewTable([]string{"a"}, []*K{IntV([]int32{1}), IntV([]int32{2})}), limits: DefaultEncodeLimits},
		{name: "invalid-primitive", value: &K{KFUNCBP, NONE, byte(255)}, limits: DefaultEncodeLimits},
		{
			name:  "string",
			value: &K{KC, NONE, "too long"},
			limits: func() EncodeLimits {
				limits := DefaultEncodeLimits()
				limits.MaxStringBytes = 2
				return limits
			},
		},
		{
			name:  "vector",
			value: IntV([]int32{1, 2}),
			limits: func() EncodeLimits {
				limits := DefaultEncodeLimits()
				limits.MaxVectorElements = 1
				return limits
			},
		},
		{
			name:  "allocation",
			value: &K{KC, NONE, strings.Repeat("x", 32)},
			limits: func() EncodeLimits {
				limits := DefaultEncodeLimits()
				limits.MaxAllocationBytes = 32
				return limits
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dst bytes.Buffer
			if err := EncodeWithLimits(&dst, ASYNC, test.value, test.limits()); err == nil {
				t.Fatal("invalid object encoded successfully")
			}
			if dst.Len() != 0 {
				t.Fatalf("encoder wrote %d bytes before rejecting input", dst.Len())
			}
		})
	}
}

type observedWriter struct {
	writes int
	bytes.Buffer
}

func (w *observedWriter) Write(value []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(value)
}

type callbackError struct {
	calls  int
	texts  []string
	mutate func()
	panics bool
}

func (e *callbackError) Error() string {
	e.calls++
	if e.mutate != nil {
		e.mutate()
	}
	if e.panics {
		panic("caller error panic")
	}
	if len(e.texts) == 0 {
		return ""
	}
	return e.texts[(e.calls-1)%len(e.texts)]
}

func TestLogicalLengthAndEncodeRejectDictionaryCyclesAndExcessDepth(t *testing.T) {
	for _, qtype := range []int8{XD, SD} {
		t.Run(fmt.Sprintf("type-%d", qtype), func(t *testing.T) {
			cycle := &K{Type: qtype, Attr: NONE}
			cycle.Data = Dict{Key: cycle, Value: IntV([]int32{1})}
			if got := cycle.Len(); got != -1 {
				t.Fatalf("cyclic dictionary length = %d, want -1", got)
			}
			dst := &observedWriter{}
			if err := Encode(dst, ASYNC, cycle); err == nil {
				t.Fatal("cyclic dictionary encoded successfully")
			}
			if dst.writes != 0 || dst.Len() != 0 {
				t.Fatalf("cyclic dictionary reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
			}
		})
	}

	deep := IntV([]int32{1})
	for index := 0; index < defaultMaxDepth; index++ {
		deep = &K{Type: XD, Attr: NONE, Data: Dict{Key: deep, Value: deep}}
	}
	if got := deep.Len(); got != -1 {
		t.Fatalf("over-depth dictionary length = %d, want -1", got)
	}
	dst := &observedWriter{}
	if err := Encode(dst, ASYNC, deep); err == nil {
		t.Fatal("over-depth dictionary encoded successfully")
	}
	if dst.writes != 0 || dst.Len() != 0 {
		t.Fatalf("over-depth dictionary reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
	}

	tableCycle := &K{Type: XT, Attr: NONE}
	tableCycle.Data = Table{Columns: []string{"x"}, Data: []*K{tableCycle}}
	if got := tableCycle.Len(); got != -1 {
		t.Fatalf("cyclic table length = %d, want -1", got)
	}
}

func TestEncodeCachesAndBoundsCallerErrorText(t *testing.T) {
	nondeterministic := &callbackError{texts: []string{"short", strings.Repeat("x", 4096)}}
	value := Error(nondeterministic)
	var frame bytes.Buffer
	if err := Encode(&frame, ASYNC, value); err != nil {
		t.Fatalf("encode nondeterministic error: %v", err)
	}
	if nondeterministic.calls != 1 {
		t.Fatalf("Error invoked %d times, want once", nondeterministic.calls)
	}
	if got := frame.Bytes()[9:]; !bytes.Equal(got, []byte("short\x00")) {
		t.Fatalf("encoded cached error text = %q, want short", got)
	}

	repeated := &callbackError{texts: []string{"same"}}
	repeatedK := Error(repeated)
	if err := Encode(io.Discard, ASYNC, NewList(repeatedK, repeatedK)); err != nil {
		t.Fatalf("encode repeated error object: %v", err)
	}
	if repeated.calls != 1 {
		t.Fatalf("repeated Error object invoked %d times, want once", repeated.calls)
	}

	limits := DefaultEncodeLimits()
	limits.MaxStringBytes = 4
	tooLong := &callbackError{texts: []string{"12345"}}
	dst := &observedWriter{}
	if err := EncodeWithLimits(dst, ASYNC, Error(tooLong), limits); err == nil {
		t.Fatal("over-limit error text encoded successfully")
	}
	if tooLong.calls != 1 {
		t.Fatalf("over-limit Error invoked %d times, want once", tooLong.calls)
	}
	if dst.writes != 0 || dst.Len() != 0 {
		t.Fatalf("over-limit error reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
	}

	panicking := &callbackError{panics: true}
	dst = &observedWriter{}
	err := Encode(dst, ASYNC, Error(panicking))
	if err == nil || !strings.Contains(err.Error(), "error method panicked") {
		t.Fatalf("panicking Error returned %v", err)
	}
	if panicking.calls != 1 {
		t.Fatalf("panicking Error invoked %d times, want once", panicking.calls)
	}
	if dst.writes != 0 || dst.Len() != 0 {
		t.Fatalf("panicking error reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
	}
}

func TestEncodeExactPreflightBufferRejectsGraphMutationBeforeExternalWrite(t *testing.T) {
	large := strings.Repeat("x", 64<<10)
	tests := []struct {
		name    string
		initial string
		mutated string
	}{
		{name: "growth", initial: "x", mutated: large},
		{name: "shrink", initial: large, mutated: "x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			charVector := &K{Type: KC, Attr: NONE, Data: test.initial}
			trigger := &callbackError{
				texts: []string{"trigger"},
				mutate: func() {
					charVector.Data = test.mutated
				},
			}
			dst := &observedWriter{}
			err := Encode(dst, ASYNC, NewList(charVector, Error(trigger)))
			if err == nil {
				t.Fatal("mutated graph encoded successfully")
			}
			if trigger.calls != 1 {
				t.Fatalf("mutation Error invoked %d times, want once", trigger.calls)
			}
			if dst.writes != 0 || dst.Len() != 0 {
				t.Fatalf("mutated graph reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
			}
		})
	}

	t.Run("same-size malformed dictionary", func(t *testing.T) {
		dictionary := NewDict(SymbolV([]string{"a"}), IntV([]int32{1}))
		key := dictionary.Data.(Dict).Key
		trigger := &callbackError{
			texts: []string{"trigger"},
			mutate: func() {
				// `a` and two empty symbols both occupy two payload bytes,
				// but the latter changes the dictionary's logical key length.
				key.Data = []string{"", ""}
			},
		}
		dst := &observedWriter{}
		err := Encode(dst, ASYNC, NewList(dictionary, Error(trigger)))
		if err == nil || !strings.Contains(err.Error(), "key/value lengths differ") {
			t.Fatalf("same-size malformed dictionary error = %v", err)
		}
		if trigger.calls != 1 {
			t.Fatalf("mutation Error invoked %d times, want once", trigger.calls)
		}
		if dst.writes != 0 || dst.Len() != 0 {
			t.Fatalf("same-size malformed dictionary reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
		}
	})

	t.Run("new error object", func(t *testing.T) {
		mutated := &K{Type: KC, Attr: NONE, Data: "x"}
		introduced := &callbackError{texts: []string{"12345"}}
		trigger := &callbackError{
			texts: []string{"trigger"},
			mutate: func() {
				// Both representations encode to seven bytes. The callback-free
				// validation pass must reject the new KERR without invoking it.
				mutated.Type = KERR
				mutated.Data = introduced
			},
		}
		dst := &observedWriter{}
		err := Encode(dst, ASYNC, NewList(mutated, Error(trigger)))
		if err == nil || !strings.Contains(err.Error(), "introduced after callback preflight") {
			t.Fatalf("new error object error = %v", err)
		}
		if trigger.calls != 1 || introduced.calls != 0 {
			t.Fatalf("Error calls = trigger:%d introduced:%d, want 1/0", trigger.calls, introduced.calls)
		}
		if dst.writes != 0 || dst.Len() != 0 {
			t.Fatalf("new error object reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
		}
	})

	t.Run("cycle", func(t *testing.T) {
		mutated := &K{Type: KC, Attr: NONE, Data: strings.Repeat("x", int(defaultMaxString))}
		trigger := &callbackError{
			texts: []string{"trigger"},
			mutate: func() {
				mutated.Type = K0
				mutated.Data = []*K{mutated}
			},
		}
		dst := &observedWriter{}
		err := Encode(dst, ASYNC, NewList(mutated, Error(trigger)))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("post-preflight cycle error = %v", err)
		}
		if trigger.calls != 1 {
			t.Fatalf("mutation Error invoked %d times, want once", trigger.calls)
		}
		if dst.writes != 0 || dst.Len() != 0 {
			t.Fatalf("post-preflight cycle reached external writer: writes=%d bytes=%d", dst.writes, dst.Len())
		}
	})
}

type shortWriter struct {
	buf bytes.Buffer
}

func (w *shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	n := 3
	if len(value) < n {
		n = len(value)
	}
	return w.buf.Write(value[:n])
}

func TestEncodePerformsFullWrites(t *testing.T) {
	writer := &shortWriter{}
	if err := Encode(writer, ASYNC, NewList(Long(1), Symbol("x"))); err != nil {
		t.Fatalf("encode through short writer: %v", err)
	}
	if _, _, err := decodeBytes(writer.buf.Bytes()); err != nil {
		t.Fatalf("full frame was not written: %v", err)
	}
}

type invalidCountWriter struct {
	count func(int) int
}

func (w invalidCountWriter) Write(value []byte) (int, error) {
	return w.count(len(value)), nil
}

func TestEncodeRejectsContractViolatingWriterCountsWithoutPanicking(t *testing.T) {
	tests := []struct {
		name  string
		count func(int) int
	}{
		{name: "negative", count: func(int) int { return -1 }},
		{name: "too large", count: func(length int) int { return length + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Encode(invalidCountWriter{count: test.count}, ASYNC, Long(1))
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("invalid writer count error = %v, want io.ErrShortWrite", err)
			}
		})
	}
}

func TestCompressRequiresACompleteUncompressedFrameAndIsIdempotent(t *testing.T) {
	var frame bytes.Buffer
	if err := EncodeWithLimits(
		&frame,
		SYNC,
		&K{Type: KC, Attr: NONE, Data: strings.Repeat("x", 512)},
		DefaultEncodeLimits(),
	); err != nil {
		t.Fatal(err)
	}
	compressed := append([]byte(nil), frame.Bytes()...)
	if compressed[2] != 1 {
		t.Fatal("fixture was not compressed")
	}
	idempotent := Compress(compressed)
	if !bytes.Equal(idempotent, compressed) || &idempotent[0] != &compressed[0] {
		t.Fatal("already-compressed frame was transformed or copied")
	}

	uncompressed := make([]byte, 8+2+512)
	uncompressed[0] = 1
	uncompressed[1] = byte(SYNC)
	binary.LittleEndian.PutUint32(uncompressed[4:8], uint32(len(uncompressed)))
	uncompressed[8] = byte(KC)
	binary.LittleEndian.PutUint32(uncompressed[10:14], 508)
	// The exact payload shape is irrelevant to Compress; only the complete
	// frame header contract is checked before compression.
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "endian", mutate: func(value []byte) { value[0] = 2 }},
		{name: "request", mutate: func(value []byte) { value[1] = 3 }},
		{name: "compressed", mutate: func(value []byte) { value[2] = 1 }},
		{name: "reserved", mutate: func(value []byte) { value[3] = 1 }},
		{name: "declared size", mutate: func(value []byte) { binary.LittleEndian.PutUint32(value[4:8], uint32(len(value)-1)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := append([]byte(nil), uncompressed...)
			test.mutate(input)
			got := Compress(input)
			if !bytes.Equal(got, input) || &got[0] != &input[0] {
				t.Fatal("malformed frame was transformed or copied")
			}
		})
	}
}

func TestRandomAcyclicEncodeDecodeRoundTrips(t *testing.T) {
	random := rand.New(rand.NewSource(0x6173796e6351))
	for iteration := 0; iteration < 2000; iteration++ {
		value := randomRoundTripValue(random, 0)
		request := ReqType(random.Intn(3))
		var frame bytes.Buffer
		if err := Encode(&frame, request, value); err != nil {
			t.Fatalf("iteration %d encode %#v: %v", iteration, value, err)
		}
		decoded, decodedRequest, err := decodeBytes(frame.Bytes())
		if err != nil {
			t.Fatalf("iteration %d decode compressed=%d size=%d value=%s: %v", iteration, frame.Bytes()[2], frame.Len(), value.String(), err)
		}
		if decodedRequest != request || !reflect.DeepEqual(decoded, value) {
			t.Fatalf("iteration %d round trip mismatch:\nrequest %d/%d\nwant %#v (%s)\ngot  %#v (%s)", iteration, request, decodedRequest, value, value.String(), decoded, decoded.String())
		}
	}
}

func randomRoundTripValue(random *rand.Rand, depth int) *K {
	switch random.Intn(9) {
	case 0:
		return Int(int32(random.Uint32()))
	case 1:
		return Long(random.Int63())
	case 2:
		return Atom(-KB, random.Intn(2) == 1)
	case 3:
		return Symbol(fmt.Sprintf("s%d", random.Intn(1000)))
	case 4:
		return &K{Type: KC, Attr: NONE, Data: strings.Repeat("x", random.Intn(64))}
	case 5:
		values := make([]int32, random.Intn(32))
		for index := range values {
			values[index] = int32(random.Uint32())
		}
		return IntV(values)
	case 6:
		var values []byte
		if length := random.Intn(64); length > 0 {
			values = make([]byte, length)
		}
		_, _ = random.Read(values)
		return &K{Type: KG, Attr: NONE, Data: values}
	case 7:
		length := random.Intn(12)
		keys := make([]string, length)
		values := make([]int32, length)
		for index := range keys {
			keys[index] = fmt.Sprintf("k%d", index)
			values[index] = int32(random.Uint32())
		}
		return NewDict(SymbolV(keys), IntV(values))
	default:
		if depth >= 3 {
			return &K{Type: K0, Attr: NONE, Data: []*K{}}
		}
		values := make([]*K, random.Intn(8))
		for index := range values {
			values[index] = randomRoundTripValue(random, depth+1)
		}
		return NewList(values...)
	}
}

func FuzzDecode(f *testing.F) {
	var valid bytes.Buffer
	if err := Encode(&valid, ASYNC, NewList(Long(1), Symbol("seed"))); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	for _, raw := range malformedCorpus() {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _, _ = decodeBytes(raw)
	})
}

func FuzzUncompress(f *testing.F) {
	f.Add([]byte{})
	for _, raw := range malformedCorpus() {
		if len(raw) > 8 {
			f.Add(raw[8:])
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = UncompressBounded(raw, defaultMaxFrame)
	})
}

func FuzzCompress(f *testing.F) {
	f.Add([]byte{})
	var valid bytes.Buffer
	if err := Encode(&valid, ASYNC, &K{Type: KC, Attr: NONE, Data: strings.Repeat("x", 256)}); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Fuzz(func(t *testing.T, raw []byte) {
		before := append([]byte(nil), raw...)
		result := Compress(raw)
		if !completeUncompressedFrameHeader(before) && !bytes.Equal(result, before) {
			t.Fatal("Compress transformed input without a valid complete uncompressed frame header")
		}
	})
}

func completeUncompressedFrameHeader(frame []byte) bool {
	if len(frame) < 8 || uint64(len(frame)) > uint64(^uint32(0)) {
		return false
	}
	order, ok := byteOrder(frame[0])
	return ok &&
		frame[1] <= byte(RESPONSE) &&
		frame[2] == 0 &&
		frame[3] == 0 &&
		uint64(order.Uint32(frame[4:8])) == uint64(len(frame))
}

func TestDecodeTruncatedHeaderReturnsReadError(t *testing.T) {
	_, _, err := Decode(bufio.NewReader(io.LimitReader(bytes.NewReader(make([]byte, 7)), 7)))
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}

func TestDecodePreservesPrimaryObjectErrorWhenBytesRemain(t *testing.T) {
	frame := testFrame(binary.LittleEndian, 1, byte(RESPONSE), 0, []byte{3, 0})
	_, _, err := decodeBytes(frame)
	if err == nil || !strings.Contains(err.Error(), "unsupported q type") {
		t.Fatalf("primary object error was masked: %v", err)
	}
	if strings.Contains(err.Error(), "trailing bytes") {
		t.Fatalf("trailing-byte error masked primary object error: %v", err)
	}
}
