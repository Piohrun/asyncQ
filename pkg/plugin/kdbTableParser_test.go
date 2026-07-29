package plugin

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
	uuid "github.com/nu7hatch/gouuid"
)

func TestParseKdbResponseRejectsMalformedObjectsWithoutPanicking(t *testing.T) {
	cyclicList := &kdb.K{Type: kdb.K0, Attr: kdb.NONE}
	cyclicList.Data = []*kdb.K{cyclicList}

	tests := []struct {
		name     string
		response *kdb.K
		model    QueryModel
		want     string
	}{
		{
			name:     "nil response",
			response: nil,
			want:     "nil response",
		},
		{
			name:     "vector with nil data",
			response: &kdb.K{Type: kdb.KJ, Attr: kdb.NONE},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "invalid data for kdb+ vector type 7",
		},
		{
			name:     "vector with wrong concrete data",
			response: &kdb.K{Type: kdb.KJ, Attr: kdb.NONE, Data: []int32{1}},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "expected []int64",
		},
		{
			name:     "atom with wrong concrete data",
			response: &kdb.K{Type: -kdb.KJ, Attr: kdb.NONE, Data: int32(1)},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "invalid data for kdb+ atom type -7",
		},
		{
			name:     "atom with attribute",
			response: &kdb.K{Type: -kdb.KJ, Attr: kdb.SORTED, Data: int64(1)},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "atom has invalid attribute",
		},
		{
			name:     "unsupported vector type",
			response: &kdb.K{Type: 3, Attr: kdb.NONE, Data: []byte{1}},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "unsupported kdb+ vector type 3",
		},
		{
			name:     "generic list with nil item",
			response: &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: []*kdb.K{nil}},
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "item 0 is nil",
		},
		{
			name: "generic list with invalid primitive index",
			response: kdb.NewList(
				&kdb.K{Type: kdb.KFUNCBP, Attr: kdb.NONE, Data: byte(255)},
			),
			model: QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:  "invalid binary-function index",
		},
		{
			name:     "cyclic generic list",
			response: cyclicList,
			model:    QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:     "contains a cycle",
		},
		{
			name:     "table with wrong concrete data",
			response: &kdb.K{Type: kdb.XT, Attr: kdb.NONE, Data: "not a table"},
			want:     "invalid table data",
		},
		{
			name: "table with different name and data counts",
			response: &kdb.K{
				Type: kdb.XT,
				Attr: kdb.NONE,
				Data: kdb.Table{
					Columns: []string{"a", "b"},
					Data:    []*kdb.K{kdb.LongV([]int64{1})},
				},
			},
			want: "column name/data counts differ",
		},
		{
			name: "table with nil column",
			response: &kdb.K{
				Type: kdb.XT,
				Attr: kdb.NONE,
				Data: kdb.Table{
					Columns: []string{"a"},
					Data:    []*kdb.K{nil},
				},
			},
			want: "table column 0 is nil",
		},
		{
			name: "table with atom column",
			response: &kdb.K{
				Type: kdb.XT,
				Attr: kdb.NONE,
				Data: kdb.Table{
					Columns: []string{"a"},
					Data:    []*kdb.K{kdb.Long(1)},
				},
			},
			want: "table column 0 is not a vector",
		},
		{
			name: "table with unequal row lengths",
			response: kdb.NewTable(
				[]string{"a", "b"},
				[]*kdb.K{kdb.LongV([]int64{1, 2}), kdb.LongV([]int64{1})},
			),
			want: "table columns have unequal row lengths",
		},
		{
			name:     "dictionary with wrong concrete data",
			response: &kdb.K{Type: kdb.XD, Attr: kdb.NONE, Data: "not a dictionary"},
			want:     "invalid dictionary data",
		},
		{
			name: "dictionary with nil key",
			response: &kdb.K{
				Type: kdb.XD,
				Attr: kdb.NONE,
				Data: kdb.Dict{Value: kdb.Long(1)},
			},
			model: QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:  "dictionary key is nil",
		},
		{
			name: "dictionary with nil value",
			response: &kdb.K{
				Type: kdb.XD,
				Attr: kdb.NONE,
				Data: kdb.Dict{Key: kdb.Symbol("a")},
			},
			model: QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:  "dictionary value is nil",
		},
		{
			name: "dictionary with incompatible column lengths",
			response: kdb.NewDict(
				kdb.SymbolV([]string{"a", "b"}),
				kdb.NewList(kdb.LongV([]int64{1, 2}), kdb.LongV([]int64{1, 2, 3})),
			),
			model: QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			want:  "dictionary values have incompatible lengths",
		},
		{
			name: "keyed table with unequal key and value rows",
			response: kdb.NewDict(
				kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"A", "B"})}),
				kdb.NewTable([]string{"value"}, []*kdb.K{kdb.LongV([]int64{1})}),
			),
			want: "keyed table key/value row counts differ",
		},
		{
			name: "grouped table with unequal aggregate lengths",
			response: kdb.NewDict(
				kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"A"})}),
				kdb.NewTable(
					[]string{"bid", "ask"},
					[]*kdb.K{
						kdb.NewList(kdb.FloatV([]float64{1, 2})),
						kdb.NewList(kdb.FloatV([]float64{1})),
					},
				),
			),
			want: "columns have unequal lengths",
		},
		{
			name: "grouped table with no key columns",
			response: kdb.NewDict(
				kdb.NewTable(nil, nil),
				kdb.NewTable([]string{"value"}, []*kdb.K{kdb.NewList()}),
			),
			want: "key table has no columns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseErrorWithoutPanic(t, tt.response, tt.model, tt.want)
		})
	}
}

func TestParseKdbResponseValidationDoesNotLeakColumnContents(t *testing.T) {
	const sensitiveColumnName = "secret-column-value-that-must-not-appear"
	response := &kdb.K{
		Type: kdb.XT,
		Attr: kdb.NONE,
		Data: kdb.Table{
			Columns: []string{sensitiveColumnName},
			Data:    nil,
		},
	}

	_, err := parseKdbResponseToFrames(response, QueryModel{}, "A")
	if err == nil {
		t.Fatal("expected malformed response error")
	}
	if strings.Contains(err.Error(), sensitiveColumnName) {
		t.Fatalf("error leaked response contents: %v", err)
	}
}

func assertParseErrorWithoutPanic(t *testing.T, response *kdb.K, model QueryModel, want string) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("parseKdbResponseToFrames panicked: %v", recovered)
		}
	}()

	frames, err := parseKdbResponseToFrames(response, model, "A")
	if err == nil {
		t.Fatalf("expected error, got frames: %#v", frames)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected error: got %q, want substring %q", err, want)
	}
	if strings.Contains(err.Error(), "unexpected parser failure") {
		t.Fatalf("malformed response reached panic recovery instead of explicit validation: %v", err)
	}
}

func TestParserHelpersRejectNilAndOutOfRangeInputs(t *testing.T) {
	if item := correctedIndex(kdb.LongV([]int64{1}), 1); item != nil {
		t.Fatalf("out-of-range corrected index returned %#v", item)
	}
	if _, err := parseFrameName(nil); err == nil {
		t.Fatal("expected nil frame-name key error")
	}
	if _, err := getDepth([]*kdb.K{nil}); err == nil {
		t.Fatal("expected nil grouped column error")
	}
}

func TestKdbObjectValidationDepthBoundary(t *testing.T) {
	accepted := nestedKdbList(maxKdbObjectDepth - 1)
	if err := validateKdbObject(accepted); err != nil {
		t.Fatalf("depth %d should be accepted: %v", maxKdbObjectDepth, err)
	}

	rejected := nestedKdbList(maxKdbObjectDepth)
	if err := validateKdbObject(rejected); err == nil {
		t.Fatalf("depth %d should be rejected", maxKdbObjectDepth+1)
	} else if !strings.Contains(err.Error(), "exceeds maximum nesting depth") {
		t.Fatalf("unexpected depth error: %v", err)
	}
}

func TestKdbObjectValidationRejectsAliasedDAGDepthBypass(t *testing.T) {
	for _, shallowFirst := range []bool{true, false} {
		name := "deep path first"
		if shallowFirst {
			name = "shallow path first"
		}
		t.Run(name, func(t *testing.T) {
			response := aliasedDepthDAG(shallowFirst)
			limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxDepth = 5
			})
			err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits))
			if err == nil || !strings.Contains(err.Error(), "maximum nesting depth") {
				t.Fatalf("expected aliased deep-path rejection, got %v", err)
			}

			limits.MaxDepth = 6
			if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
				t.Fatalf("legal shared DAG should be accepted: %v", err)
			}
		})
	}
}

func TestKdbObjectValidationEdgeBudget(t *testing.T) {
	t.Run("shared backing is charged per parent edge", func(t *testing.T) {
		child := kdb.Long(1)
		sharedBacking := []*kdb.K{child}
		first := &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking}
		second := &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking}
		response := kdb.NewList(first, second)

		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxEdges = 4 // root->parents plus each parent->shared child.
		})
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
			t.Fatalf("edge boundary should be accepted: %v", err)
		}
		limits.MaxEdges = 3
		err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits))
		if err == nil || !strings.Contains(err.Error(), "traversed edge limit") {
			t.Fatalf("expected one-over edge rejection, got %v", err)
		}
	})

	t.Run("all structural edge kinds are charged", func(t *testing.T) {
		leaf := kdb.Long(1)
		vector := kdb.LongV([]int64{1})
		table := kdb.NewTable([]string{"value"}, []*kdb.K{vector})
		dict := kdb.NewDict(kdb.Symbol("value"), leaf)
		projection := &kdb.K{Type: kdb.KPROJ, Attr: kdb.NONE, Data: []*kdb.K{leaf}}
		adverb := &kdb.K{Type: kdb.KEACH, Attr: kdb.NONE, Data: leaf}
		response := kdb.NewList(table, dict, projection, adverb)

		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxEdges = 9
		})
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
			t.Fatalf("edge boundary should be accepted: %v", err)
		}
		limits.MaxEdges = 8
		err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits))
		if err == nil || !strings.Contains(err.Error(), "traversed edge limit") {
			t.Fatalf("expected aggregate edge rejection, got %v", err)
		}
	})
}

func TestKdbFunctionValidationMirrorsWireShape(t *testing.T) {
	function := kdb.NewFunc(".test", "{x+y}")

	t.Run("root list and nested body edges", func(t *testing.T) {
		response := kdb.NewList(function)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxEdges = 2
		})
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
			t.Fatalf("function edge boundary should be accepted: %v", err)
		}
		limits.MaxEdges = 1
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err == nil ||
			!strings.Contains(err.Error(), "traversed edge limit") {
			t.Fatalf("expected function body edge rejection, got %v", err)
		}
	})

	t.Run("root and list depth", func(t *testing.T) {
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxDepth = 2
		})
		if err := validateKdbObjectWithBudget(function, newKdbFrameParseBudget(limits)); err != nil {
			t.Fatalf("root function depth boundary should be accepted: %v", err)
		}
		limits.MaxDepth = 1
		if err := validateKdbObjectWithBudget(function, newKdbFrameParseBudget(limits)); err == nil ||
			!strings.Contains(err.Error(), "maximum nesting depth") {
			t.Fatalf("expected root function depth rejection, got %v", err)
		}

		response := kdb.NewList(function)
		limits.MaxDepth = 3
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
			t.Fatalf("listed function depth boundary should be accepted: %v", err)
		}
		limits.MaxDepth = 2
		if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err == nil ||
			!strings.Contains(err.Error(), "maximum nesting depth") {
			t.Fatalf("expected listed function depth rejection, got %v", err)
		}
	})

	t.Run("aliased function depth", func(t *testing.T) {
		for _, shallowFirst := range []bool{true, false} {
			deep := kdb.NewList(function)
			var response *kdb.K
			if shallowFirst {
				response = kdb.NewList(function, deep)
			} else {
				response = kdb.NewList(deep, function)
			}
			limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxDepth = 3
			})
			if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err == nil ||
				!strings.Contains(err.Error(), "maximum nesting depth") {
				t.Fatalf("expected aliased function depth rejection, got %v", err)
			}
			limits.MaxDepth = 4
			if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(limits)); err != nil {
				t.Fatalf("legal aliased function DAG should be accepted: %v", err)
			}
		}
	})
}

func TestKdbFunctionValidationMatchesTransportTextContract(t *testing.T) {
	decodeLimit := kdb.DefaultDecodeLimits().MaxStringBytes
	encodeLimit := kdb.DefaultEncodeLimits().MaxStringBytes
	if decodeLimit != encodeLimit || maxKdbTransportStringBytes != decodeLimit {
		t.Fatalf(
			"kdbgo function text limits drifted: parser=%d decode=%d encode=%d",
			maxKdbTransportStringBytes,
			decodeLimit,
			encodeLimit,
		)
	}

	exact := strings.Repeat("x", int(maxKdbTransportStringBytes))
	over := exact + "x"
	tests := []struct {
		name     string
		function kdb.Function
		want     string
	}{
		{name: "namespace exact", function: kdb.Function{Namespace: exact, Body: "x"}},
		{name: "namespace one over", function: kdb.Function{Namespace: over, Body: "x"}, want: "namespace exceeds"},
		{name: "body exact", function: kdb.Function{Namespace: ".test", Body: exact}},
		{name: "body one over", function: kdb.Function{Namespace: ".test", Body: over}, want: "body exceeds"},
		{name: "namespace NUL", function: kdb.Function{Namespace: "a\x00b", Body: "x"}, want: "namespace contains NUL"},
		{
			name: "arbitrary transport bytes",
			function: kdb.Function{
				Namespace: string([]byte{0xff}),
				Body:      string([]byte{0x00, 0x80, 0xff}),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKdbObject(&kdb.K{Type: kdb.KFUNC, Attr: kdb.NONE, Data: tt.function})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("transport-valid function should be accepted: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}

	err := validateKdbObject(&kdb.K{Type: kdb.KFUNC, Attr: kdb.NONE, Data: "not a function"})
	if err == nil || !strings.Contains(err.Error(), "expected kdb.Function") {
		t.Fatalf("expected malformed function rejection, got %v", err)
	}
}

func TestKdbPrimitiveValidationMatchesTransport(t *testing.T) {
	tests := []struct {
		name    string
		qtype   int8
		index   byte
		accepts bool
	}{
		{name: "unary last table index", qtype: kdb.KFUNCUP, index: 41, accepts: true},
		{name: "unary identity sentinel", qtype: kdb.KFUNCUP, index: 255, accepts: true},
		{name: "unary first invalid", qtype: kdb.KFUNCUP, index: 42},
		{name: "unary last invalid", qtype: kdb.KFUNCUP, index: 254},
		{name: "binary last table index", qtype: kdb.KFUNCBP, index: 33, accepts: true},
		{name: "binary first invalid", qtype: kdb.KFUNCBP, index: 34},
		{name: "ternary last table index", qtype: kdb.KFUNCTR, index: 2, accepts: true},
		{name: "ternary first invalid", qtype: kdb.KFUNCTR, index: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := &kdb.K{Type: tt.qtype, Attr: kdb.NONE, Data: tt.index}
			validationErr := validateKdbObject(value)
			var encoded bytes.Buffer
			encodeErr := kdb.Encode(&encoded, kdb.ASYNC, value)
			if (validationErr == nil) != tt.accepts {
				t.Fatalf("parser acceptance=%t, want %t: %v", validationErr == nil, tt.accepts, validationErr)
			}
			if (encodeErr == nil) != tt.accepts {
				t.Fatalf("kdbgo encoder acceptance=%t, want %t: %v", encodeErr == nil, tt.accepts, encodeErr)
			}
		})
	}
}

func aliasedDepthDAG(shallowFirst bool) *kdb.K {
	shared := kdb.NewList(kdb.NewList(kdb.Long(1)))
	deepParent := kdb.NewList(kdb.NewList(shared))
	if shallowFirst {
		return kdb.NewList(shared, deepParent)
	}
	return kdb.NewList(deepParent, shared)
}

func TestParseKdbObjectAsFrameAcceptsDecodedTemporalShapes(t *testing.T) {
	qEpoch := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	nullTimestamp := qEpoch.Add(time.Duration(kdb.Nj))
	minuteValue := kdb.Minute(time.Time{})
	secondValue := kdb.Second(time.Time{})
	timeValue := kdb.Time(time.Time{})

	tests := []struct {
		name  string
		value *kdb.K
	}{
		{name: "timestamp atom", value: &kdb.K{Type: -kdb.KP, Attr: kdb.NONE, Data: nullTimestamp}},
		{name: "timestamp vector", value: &kdb.K{Type: kdb.KP, Attr: kdb.NONE, Data: []time.Time{qEpoch}}},
		{name: "month atom null", value: &kdb.K{Type: -kdb.KM, Attr: kdb.NONE, Data: kdb.Month(kdb.Ni)}},
		{name: "month vector", value: &kdb.K{Type: kdb.KM, Attr: kdb.NONE, Data: []kdb.Month{0}}},
		{name: "date atom null", value: &kdb.K{Type: -kdb.KD, Attr: kdb.NONE, Data: int32(kdb.Ni)}},
		{name: "date vector", value: &kdb.K{Type: kdb.KD, Attr: kdb.NONE, Data: []time.Time{qEpoch}}},
		{name: "datetime atom null", value: &kdb.K{Type: -kdb.KZ, Attr: kdb.NONE, Data: math.NaN()}},
		{name: "datetime vector", value: &kdb.K{Type: kdb.KZ, Attr: kdb.NONE, Data: []time.Time{qEpoch}}},
		{name: "timespan atom null", value: &kdb.K{Type: -kdb.KN, Attr: kdb.NONE, Data: time.Duration(kdb.Nj)}},
		{name: "timespan vector", value: &kdb.K{Type: kdb.KN, Attr: kdb.NONE, Data: []time.Duration{0}}},
		{name: "minute atom null", value: &kdb.K{Type: -kdb.KU, Attr: kdb.NONE, Data: int32(kdb.Ni)}},
		{name: "minute vector", value: &kdb.K{Type: kdb.KU, Attr: kdb.NONE, Data: []kdb.Minute{minuteValue}}},
		{name: "second atom null", value: &kdb.K{Type: -kdb.KV, Attr: kdb.NONE, Data: int32(kdb.Ni)}},
		{name: "second vector", value: &kdb.K{Type: kdb.KV, Attr: kdb.NONE, Data: []kdb.Second{secondValue}}},
		{name: "time atom from indexed vector", value: &kdb.K{Type: -kdb.KT, Attr: kdb.NONE, Data: timeValue}},
		{name: "time vector", value: &kdb.K{Type: kdb.KT, Attr: kdb.NONE, Data: []kdb.Time{timeValue}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := ParseKdbObjectAsFrame(tt.value)
			if err != nil {
				t.Fatalf("ParseKdbObjectAsFrame returned error: %v", err)
			}
			if got := frame.Fields[0].Len(); got != 1 {
				t.Fatalf("unexpected field length: got %d want 1", got)
			}
		})
	}
}

func nestedKdbList(depth int) *kdb.K {
	value := kdb.Long(1)
	for i := 0; i < depth; i++ {
		value = kdb.NewList(value)
	}
	return value
}

func TestParsePanopticonScalarObjectAsFrame(t *testing.T) {
	frames, err := parseKdbResponseToFrames(kdb.Long(42), QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"value"})
	if got := frame.Fields[0].At(0); got != int64(42) {
		t.Fatalf("unexpected scalar value: %#v", got)
	}
}

func TestParsePanopticonVectorObjectAsFrame(t *testing.T) {
	frames, err := parseKdbResponseToFrames(kdb.LongV([]int64{1, 2, 3}), QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"value"})
	if frame.Fields[0].Len() != 3 {
		t.Fatalf("unexpected vector length: %d", frame.Fields[0].Len())
	}
	if got := frame.Fields[0].At(2); got != int64(3) {
		t.Fatalf("unexpected vector value: %#v", got)
	}
}

func TestParsePanopticonCharVectorAsString(t *testing.T) {
	frames, err := parseKdbResponseToFrames(kdb.Atom(kdb.KC, "ready"), QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"value"})
	if frame.Fields[0].Len() != 1 {
		t.Fatalf("char vector should be one string row, got %d rows", frame.Fields[0].Len())
	}
	if got := frame.Fields[0].At(0); got != "ready" {
		t.Fatalf("unexpected string value: %#v", got)
	}
}

func TestParseSimpleTableConvertsMixedGenericListColumnToStrings(t *testing.T) {
	res := kdb.NewTable(
		[]string{"sym", "mixed"},
		[]*kdb.K{
			kdb.SymbolV([]string{"AAPL", "MSFT", "GOOG"}),
			kdb.NewList(kdb.Long(42), kdb.Atom(kdb.KC, "ready"), kdb.Symbol("done")),
		},
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"sym", "mixed"})
	if frame.Fields[0].Len() != 3 || frame.Fields[1].Len() != 3 {
		t.Fatalf("mixed generic column should preserve row count, got %d/%d", frame.Fields[0].Len(), frame.Fields[1].Len())
	}
	mixed := fieldByName(t, frame, "mixed")
	for i, want := range []string{"42", "ready", "done"} {
		if got := mixed.At(i); got != want {
			t.Fatalf("unexpected mixed value at %d: got %#v want %#v", i, got, want)
		}
	}
}

func TestParseSimpleTablePreservesGenericStringListColumn(t *testing.T) {
	res := kdb.NewTable(
		[]string{"sym", "state"},
		[]*kdb.K{
			kdb.SymbolV([]string{"AAPL", "MSFT"}),
			kdb.NewList(kdb.Atom(kdb.KC, "ready"), kdb.Atom(kdb.KC, "done")),
		},
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	state := fieldByName(t, frame, "state")
	if state.Len() != 2 {
		t.Fatalf("generic string column should have two rows, got %d", state.Len())
	}
	for i, want := range []string{"ready", "done"} {
		if got := state.At(i); got != want {
			t.Fatalf("unexpected string value at %d: got %#v want %#v", i, got, want)
		}
	}
}

func TestParseSimpleTableRejectsOpaqueGenericValues(t *testing.T) {
	res := kdb.NewTable(
		[]string{"mixed"},
		[]*kdb.K{
			kdb.NewList(
				kdb.Long(1),
				&kdb.K{Type: kdb.KFUNCUP, Attr: kdb.NONE, Data: byte(0)},
			),
		},
	)
	_, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err == nil || !strings.Contains(err.Error(), "complex value") {
		t.Fatalf("expected explicit complex-value rejection, got %v", err)
	}
}

func TestParsePanopticonGenericDictAsFrame(t *testing.T) {
	res := kdb.NewDict(
		kdb.SymbolV([]string{"sym", "count"}),
		kdb.NewList(kdb.Symbol("AAPL"), kdb.Long(2)),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"sym", "count"})
	if got := fieldByName(t, frame, "sym").At(0); got != "AAPL" {
		t.Fatalf("unexpected sym value: %#v", got)
	}
	if got := fieldByName(t, frame, "count").At(0); got != int64(2) {
		t.Fatalf("unexpected count value: %#v", got)
	}
}

func TestParsePanopticonSingleColumnDictWithVectorValue(t *testing.T) {
	res := kdb.NewDict(kdb.Symbol("value"), kdb.LongV([]int64{1, 2, 3}))
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"value"})
	if frame.Fields[0].Len() != 3 {
		t.Fatalf("unexpected column length: %d", frame.Fields[0].Len())
	}
	if got := frame.Fields[0].At(1); got != int64(2) {
		t.Fatalf("unexpected column value: %#v", got)
	}
}

func TestParsePanopticonHomogeneousDictAsOneRowFrame(t *testing.T) {
	res := kdb.NewDict(kdb.SymbolV([]string{"bid", "ask"}), kdb.FloatV([]float64{101.25, 101.5}))
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"bid", "ask"})
	if frame.Fields[0].Len() != 1 || frame.Fields[1].Len() != 1 {
		t.Fatalf("homogeneous keyed values should produce one row, got %d/%d", frame.Fields[0].Len(), frame.Fields[1].Len())
	}
	if got := fieldByName(t, frame, "ask").At(0); got != float64(101.5) {
		t.Fatalf("unexpected ask value: %#v", got)
	}
}

func TestParsePanopticonKeyedTableFlattensKeyAndValueColumns(t *testing.T) {
	res := kdb.NewDict(
		kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"AAPL", "MSFT"})}),
		kdb.NewTable([]string{"price"}, []*kdb.K{kdb.FloatV([]float64{189.5, 421.25})}),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"sym", "price"})
	if frame.Fields[0].Len() != 2 || frame.Fields[1].Len() != 2 {
		t.Fatalf("keyed table should have two rows, got %d/%d", frame.Fields[0].Len(), frame.Fields[1].Len())
	}
	if got := fieldByName(t, frame, "sym").At(1); got != "MSFT" {
		t.Fatalf("unexpected sym value: %#v", got)
	}
	if got := fieldByName(t, frame, "price").At(0); got != float64(189.5) {
		t.Fatalf("unexpected price value: %#v", got)
	}
}

func TestParsePanopticonKeyedTableRejectsZeroColumnKeyWithValueRows(t *testing.T) {
	res := kdb.NewDict(
		kdb.NewTable(nil, nil),
		kdb.NewTable([]string{"value"}, []*kdb.K{kdb.LongV([]int64{1})}),
	)

	assertParseErrorWithoutPanic(
		t,
		res,
		QueryModel{CompatibilityMode: CompatibilityModePanopticon},
		"keyed table key/value row counts differ: 0 and 1",
	)
}

func TestParsePanopticonEmptyKeyedTableRemainsValid(t *testing.T) {
	res := kdb.NewDict(kdb.NewTable(nil, nil), kdb.NewTable(nil, nil))

	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)
	if len(frame.Fields) != 0 {
		t.Fatalf("expected empty keyed frame, got %d fields", len(frame.Fields))
	}
}

func TestParseGroupedTableConvertsMixedGenericListColumnToStrings(t *testing.T) {
	res := kdb.NewDict(
		kdb.NewTable([]string{"sym"}, []*kdb.K{
			kdb.SymbolV([]string{"AAPL"}),
		}),
		kdb.NewTable([]string{"mixed"}, []*kdb.K{
			kdb.NewList(kdb.NewList(kdb.Long(42), kdb.Atom(kdb.KC, "ready"), kdb.Symbol("done"))),
		}),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"mixed"})
	mixed := fieldByName(t, frame, "mixed")
	if mixed.Len() != 3 {
		t.Fatalf("mixed grouped column should preserve row count, got %d", mixed.Len())
	}
	for i, want := range []string{"42", "ready", "done"} {
		if got := mixed.At(i); got != want {
			t.Fatalf("unexpected grouped mixed value at %d: got %#v want %#v", i, got, want)
		}
	}
}

func TestParseGroupedTableClassifiesCharacterVectorsAgainstIncludedKeys(t *testing.T) {
	tests := []struct {
		name               string
		includeKeys        bool
		expectedFieldNames []string
	}{
		{
			name:               "value columns only",
			includeKeys:        false,
			expectedFieldNames: []string{"letters", "label", "values"},
		},
		{
			name:               "key and value columns",
			includeKeys:        true,
			expectedFieldNames: []string{"group", "letters", "label", "values"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames, err := ParseGroupedKdbTable(groupedCharacterVectorResponse(), tt.includeKeys)
			if err != nil {
				t.Fatalf("ParseGroupedKdbTable returned error: %v", err)
			}
			frame := onlyFrame(t, frames)
			if frame.Name != "desk" {
				t.Fatalf("unexpected frame name: got %q want %q", frame.Name, "desk")
			}
			assertFieldNames(t, frame, tt.expectedFieldNames)
			for _, field := range frame.Fields {
				if field.Len() != 3 {
					t.Fatalf("field %q has length %d, want 3", field.Name, field.Len())
				}
			}

			assertFieldValues(t, fieldByName(t, frame, "letters"), []interface{}{"a", "b", "c"})
			assertFieldValues(t, fieldByName(t, frame, "label"), []interface{}{"ok", "ok", "ok"})
			assertFieldValues(t, fieldByName(t, frame, "values"), []interface{}{int64(10), int64(20), int64(30)})
			if tt.includeKeys {
				assertFieldValues(t, fieldByName(t, frame, "group"), []interface{}{"desk", "desk", "desk"})
			}
		})
	}
}

func groupedCharacterVectorResponse() *kdb.K {
	return kdb.NewDict(
		kdb.NewTable(
			[]string{"group"},
			[]*kdb.K{kdb.NewList(kdb.Atom(kdb.KC, "desk"))},
		),
		kdb.NewTable(
			[]string{"letters", "label", "values"},
			[]*kdb.K{
				kdb.NewList(kdb.Atom(kdb.KC, "abc")),
				kdb.NewList(kdb.Atom(kdb.KC, "ok")),
				kdb.NewList(kdb.LongV([]int64{10, 20, 30})),
			},
		),
	)
}

func assertFieldValues(t *testing.T, field *data.Field, want []interface{}) {
	t.Helper()
	got := make([]interface{}, field.Len())
	for i := range got {
		got[i] = field.At(i)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field %q values: got %#v want %#v", field.Name, got, want)
	}
}

func TestParseEmptyGroupedTableWithoutValueColumns(t *testing.T) {
	res := kdb.NewDict(
		kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV(nil)}),
		kdb.NewTable(nil, nil),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("expected no frames, got %d", len(frames))
	}
}

func TestParsePanopticonDictionaryListAsRows(t *testing.T) {
	res := kdb.NewList(
		kdb.NewDict(kdb.SymbolV([]string{"sym", "price"}), kdb.NewList(kdb.Symbol("AAPL"), kdb.Float(189.5))),
		kdb.NewDict(kdb.SymbolV([]string{"sym", "price"}), kdb.NewList(kdb.Symbol("MSFT"), kdb.Float(421.25))),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"sym", "price"})
	if frame.Fields[0].Len() != 2 || frame.Fields[1].Len() != 2 {
		t.Fatalf("dictionary list should have two rows, got %d/%d", frame.Fields[0].Len(), frame.Fields[1].Len())
	}
	if got := fieldByName(t, frame, "sym").At(1); got != "MSFT" {
		t.Fatalf("unexpected sym value: %#v", got)
	}
	if got := fieldByName(t, frame, "price").At(0); got != float64(189.5) {
		t.Fatalf("unexpected price value: %#v", got)
	}
}

func TestParsePanopticonDictionaryListAllowsMissingAndReorderedKeys(t *testing.T) {
	res := kdb.NewList(
		kdb.NewDict(kdb.SymbolV([]string{"sym", "price"}), kdb.NewList(kdb.Symbol("AAPL"), kdb.Float(189.5))),
		kdb.NewDict(kdb.SymbolV([]string{"venue", "sym"}), kdb.NewList(kdb.Atom(kdb.KC, "XNYS"), kdb.Symbol("MSFT"))),
		kdb.NewDict(kdb.SymbolV([]string{"price", "sym"}), kdb.NewList(kdb.Int(101), kdb.Symbol("GOOG"))),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	assertFieldNames(t, frame, []string{"sym", "price", "venue"})
	if frame.Fields[0].Len() != 3 || frame.Fields[1].Len() != 3 || frame.Fields[2].Len() != 3 {
		t.Fatalf("dictionary list should have three rows, got %d/%d/%d", frame.Fields[0].Len(), frame.Fields[1].Len(), frame.Fields[2].Len())
	}
	if got := fieldByName(t, frame, "sym").At(2); got != "GOOG" {
		t.Fatalf("unexpected sym value: %#v", got)
	}
	if price := fieldByName(t, frame, "price"); !price.NilAt(1) {
		t.Fatalf("missing price should be nil, got %#v", price.At(1))
	}
	if got := fieldByName(t, frame, "venue").At(1).(*string); got == nil || *got != "XNYS" {
		t.Fatalf("unexpected venue value: %#v", got)
	}
}

func TestParsePanopticonDictionaryListCoercesMixedNumericColumnsToFloat(t *testing.T) {
	res := kdb.NewList(
		kdb.NewDict(kdb.SymbolV([]string{"sym", "value"}), kdb.NewList(kdb.Symbol("AAPL"), kdb.Long(2))),
		kdb.NewDict(kdb.SymbolV([]string{"sym", "value"}), kdb.NewList(kdb.Symbol("MSFT"), kdb.Float(2.5))),
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{CompatibilityMode: CompatibilityModePanopticon}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	frame := onlyFrame(t, frames)

	if got := fieldByName(t, frame, "value").At(0); got != float64(2) {
		t.Fatalf("unexpected coerced first value: %#v", got)
	}
	if got := fieldByName(t, frame, "value").At(1); got != float64(2.5) {
		t.Fatalf("unexpected coerced second value: %#v", got)
	}
}

func onlyFrame(t *testing.T, frames []*data.Frame) *data.Frame {
	t.Helper()
	if len(frames) != 1 {
		t.Fatalf("expected one frame, got %d", len(frames))
	}
	return frames[0]
}

func assertFieldNames(t *testing.T, frame *data.Frame, want []string) {
	t.Helper()
	got := make([]string, len(frame.Fields))
	for i, field := range frame.Fields {
		got[i] = field.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected field names: got %#v want %#v", got, want)
	}
}

func fieldByName(t *testing.T, frame *data.Frame, name string) *data.Field {
	t.Helper()
	for _, field := range frame.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("field %q not found", name)
	return nil
}

func TestKdbFrameParserBudgetBoundaries(t *testing.T) {
	t.Run("frames", func(t *testing.T) {
		response := groupedBudgetResponse(2, 1, 1)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxFrames = 2
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("frame boundary should be accepted: %v", err)
		}
		limits.MaxFrames = 1
		assertLimitedParseError(t, response, QueryModel{}, limits, "frame limit")
	})

	t.Run("fields per frame", func(t *testing.T) {
		response := simpleBudgetTable(1, "a", "b")
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxFieldsPerFrame = 2
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("field boundary should be accepted: %v", err)
		}
		limits.MaxFieldsPerFrame = 1
		assertLimitedParseError(t, response, QueryModel{}, limits, "column limit")
	})

	t.Run("total fields across frames", func(t *testing.T) {
		response := groupedBudgetResponse(2, 1, 2)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxFieldsTotal = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("aggregate field boundary should be accepted: %v", err)
		}
		limits.MaxFieldsTotal = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "total field limit")
	})

	t.Run("total rows across frames", func(t *testing.T) {
		response := groupedBudgetResponse(2, 2, 1)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxRowsTotal = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("aggregate row boundary should be accepted: %v", err)
		}
		limits.MaxRowsTotal = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "total row limit")
	})

	t.Run("materialized cells", func(t *testing.T) {
		response := simpleBudgetTable(2, "a", "b")
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxCellsTotal = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("cell boundary should be accepted: %v", err)
		}
		limits.MaxCellsTotal = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "materialized cell limit")
	})

	t.Run("field name bytes", func(t *testing.T) {
		response := simpleBudgetTable(1, "abcd")
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxFieldNameBytes = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("field-name boundary should be accepted: %v", err)
		}
		limits.MaxFieldNameBytes = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "field name exceeds byte limit")
	})

	t.Run("aggregate name bytes", func(t *testing.T) {
		response := simpleBudgetTable(1, "a", "b")
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxNameBytesTotal = 4 // frame name, retained RefID, and two fields.
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("name-byte boundary should be accepted: %v", err)
		}
		limits.MaxNameBytesTotal = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "field-name byte limit")
	})

	t.Run("materialized strings", func(t *testing.T) {
		response := kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"aa", "bb"})})
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxStringBytes = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("string-byte boundary should be accepted: %v", err)
		}
		limits.MaxStringBytes = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "materialized string byte limit")
	})

	t.Run("materialized strings across frames", func(t *testing.T) {
		response := kdb.NewDict(
			kdb.NewTable([]string{"group"}, []*kdb.K{kdb.SymbolV([]string{"A", "B"})}),
			kdb.NewTable(
				[]string{"value"},
				[]*kdb.K{kdb.NewList(kdb.SymbolV([]string{"aa"}), kdb.SymbolV([]string{"bb"}))},
			),
		)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxStringBytes = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("cross-frame string boundary should be accepted: %v", err)
		}
		limits.MaxStringBytes = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "materialized string byte limit")
	})

	t.Run("per value string", func(t *testing.T) {
		response := kdb.NewTable([]string{"sym"}, []*kdb.K{kdb.SymbolV([]string{"abcd"})})
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxCellStringBytes = 4
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("per-string boundary should be accepted: %v", err)
		}
		limits.MaxCellStringBytes = 3
		assertLimitedParseError(t, response, QueryModel{}, limits, "per-value byte limit")
	})

	t.Run("estimated materialized bytes", func(t *testing.T) {
		response := simpleBudgetTable(1, "a")
		required := uint64(estimatedKdbCellBytes + estimatedKdbFieldBytes + estimatedKdbFrameBytes +
			len("A") + len("A") + len("a"))
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxMaterialized = required
		})
		if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits); err != nil {
			t.Fatalf("materialized-byte boundary should be accepted: %v", err)
		}
		limits.MaxMaterialized = required - 1
		assertLimitedParseError(t, response, QueryModel{}, limits, "materialized byte limit")
	})

	t.Run("visited objects", func(t *testing.T) {
		response := kdb.NewList(kdb.Long(1), kdb.Long(2))
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxObjects = 3
		})
		if _, err := parseKdbResponseToFramesWithLimits(
			response,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			"A",
			limits,
		); err != nil {
			t.Fatalf("object boundary should be accepted: %v", err)
		}
		limits.MaxObjects = 2
		assertLimitedParseError(
			t,
			response,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			limits,
			"visited object limit",
		)
	})

	t.Run("nesting depth", func(t *testing.T) {
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxDepth = 4
		})
		budget := newKdbFrameParseBudget(limits)
		if err := validateKdbObjectWithBudget(nestedKdbList(3), budget); err != nil {
			t.Fatalf("depth boundary should be accepted: %v", err)
		}
		budget = newKdbFrameParseBudget(limits)
		if err := validateKdbObjectWithBudget(nestedKdbList(4), budget); err == nil ||
			!strings.Contains(err.Error(), "maximum nesting depth") {
			t.Fatalf("expected one-over depth rejection, got %v", err)
		}
	})
}

func TestKdbFrameParserMaterializedBudgetCoversDictionaryListWorstCases(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		columnName        string
		response          *kdb.K
		additionalBytes   uint64
		wantStringAtFirst string
	}{
		{
			name:       "nullable time",
			columnName: "t",
			response: sparseDictionaryColumn(
				"t",
				kdb.Atom(-kdb.KP, now),
				kdb.Long(1),
				kdb.Long(2),
			),
			additionalBytes: estimatedKdbNullableValueBytes,
		},
		{
			name:       "nullable string",
			columnName: "s",
			response: sparseDictionaryColumn(
				"s",
				kdb.Atom(kdb.KC, "xy"),
				kdb.Long(1),
				kdb.Long(2),
			),
			additionalBytes:   estimatedKdbNullableValueBytes + uint64(len("xy")),
			wantStringAtFirst: "xy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Two rows x two union fields, plus field/frame/name metadata.
			required := expectedFrameMaterializedBytes(2, []string{tt.columnName, "id"}, "A", "A") +
				tt.additionalBytes
			limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxMaterialized = required
			})
			frames, err := parseKdbResponseToFramesWithLimits(
				tt.response,
				QueryModel{CompatibilityMode: CompatibilityModePanopticon},
				"A",
				limits,
			)
			if err != nil {
				t.Fatalf("worst-case materialized boundary should be accepted: %v", err)
			}
			if tt.wantStringAtFirst != "" {
				field := fieldByName(t, onlyFrame(t, frames), tt.columnName)
				got, ok := field.At(0).(*string)
				if !ok || got == nil || *got != tt.wantStringAtFirst {
					t.Fatalf("unexpected nullable string value: %#v", field.At(0))
				}
			}

			limits.MaxMaterialized = required - 1
			assertLimitedParseError(
				t,
				tt.response,
				QueryModel{CompatibilityMode: CompatibilityModePanopticon},
				limits,
				"materialized byte limit",
			)
		})
	}
}

func TestKdbDictionaryListPrechargesLargeSourceStrings(t *testing.T) {
	const (
		rowCount  = 64
		valueSize = 256 << 10
	)
	source := bytes.Repeat([]byte{'x'}, valueSize)
	cell := &kdb.K{Type: kdb.KC, Attr: kdb.NONE, Data: source}
	key := kdb.Symbol("payload")
	rows := make([]*kdb.K, rowCount)
	for i := range rows {
		rows[i] = kdb.NewDict(key, cell)
	}
	response := kdb.NewList(rows...)
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		// Below one cell: rejection must happen before any []byte-to-string copy.
		limits.MaxStringBytes = valueSize - 1
	})

	parse := func() error {
		_, err := parseKdbResponseToFramesWithLimits(
			response,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			"A",
			limits,
		)
		return err
	}
	if err := parse(); err == nil || !strings.Contains(err.Error(), "materialized string byte limit") {
		t.Fatalf("expected precharge rejection, got %v", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	err := parse()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "materialized string byte limit") {
		t.Fatalf("expected measured precharge rejection, got %v", err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 4<<20 {
		t.Fatalf(
			"rejected dictionary list allocated %d bytes; source strings appear converted before precharge",
			allocated,
		)
	}
}

func TestKdbDictionaryListDirectStringBudgetBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		source interface{}
	}{
		{name: "decoded string storage", source: "xy"},
		{name: "byte slice storage", source: []byte("xy")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cell := &kdb.K{Type: kdb.KC, Attr: kdb.NONE, Data: tt.source}
			response := dictionaryListColumn(cell, cell, cell)
			frames := assertDictionaryListStringBudget(t, response, 6)
			assertFieldValues(
				t,
				fieldByName(t, onlyFrame(t, frames), "value"),
				[]interface{}{"xy", "xy", "xy"},
			)

			source, ok := tt.source.([]byte)
			if !ok {
				return
			}
			field := fieldByName(t, onlyFrame(t, frames), "value")
			source[0] = 'z'
			if got := field.At(0); got != "xy" {
				t.Fatalf("source mutation changed emitted value: %#v", got)
			}
			field.Set(0, "frame")
			if string(source) != "zy" {
				t.Fatalf("frame mutation changed source bytes: %q", source)
			}
		})
	}
}

func TestKdbDictionaryListDirectStringKindsAndFallback(t *testing.T) {
	identifier := uuid.UUID{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	instant := time.Date(2026, time.July, 29, 12, 34, 56, 0, time.UTC)
	instantText := instant.Format(time.RFC3339Nano)
	tests := []struct {
		name          string
		response      *kdb.K
		requiredBytes uint64
		want          []interface{}
	}{
		{
			name:          "symbol atom",
			response:      dictionaryListColumn(kdb.Symbol("sym")),
			requiredBytes: 3,
			want:          []interface{}{"sym"},
		},
		{
			name:          "singleton symbol vector",
			response:      dictionaryListColumn(kdb.SymbolV([]string{"vector"})),
			requiredBytes: 6,
			want:          []interface{}{"vector"},
		},
		{
			name:          "UUID atom",
			response:      dictionaryListColumn(kdb.Atom(-kdb.UU, identifier)),
			requiredBytes: kdbUUIDTextBytes,
			want:          []interface{}{identifier.String()},
		},
		{
			name: "singleton UUID vector",
			response: dictionaryListColumn(&kdb.K{
				Type: kdb.UU,
				Attr: kdb.NONE,
				Data: []uuid.UUID{identifier},
			}),
			requiredBytes: kdbUUIDTextBytes,
			want:          []interface{}{identifier.String()},
		},
		{
			name: "character byte UTF-8 expansion",
			response: dictionaryListColumn(
				kdb.Atom(-kdb.KC, byte(0x7f)),
				kdb.Atom(-kdb.KC, byte(0x80)),
			),
			requiredBytes: 3,
			want:          []interface{}{string(rune(0x7f)), string(rune(0x80))},
		},
		{
			name: "nullable direct string",
			response: kdb.NewList(
				kdb.NewDict(kdb.Symbol("value"), kdb.Atom(kdb.KC, "xy")),
				kdb.NewDict(kdb.NewList(), kdb.NewList()),
			),
			requiredBytes: 2,
			want:          []interface{}{stringPointer("xy"), (*string)(nil)},
		},
		{
			name: "heterogeneous text fallback",
			response: dictionaryListColumn(
				kdb.Atom(kdb.KC, "xy"),
				kdb.Long(7),
			),
			requiredBytes: 3,
			want:          []interface{}{"xy", "7"},
		},
		{
			name: "nullable heterogeneous text fallback",
			response: kdb.NewList(
				kdb.NewDict(kdb.Symbol("value"), kdb.Atom(kdb.KC, "xy")),
				kdb.NewDict(kdb.Symbol("value"), kdb.Long(7)),
				kdb.NewDict(kdb.NewList(), kdb.NewList()),
			),
			requiredBytes: 3,
			want: []interface{}{
				stringPointer("xy"),
				stringPointer("7"),
				(*string)(nil),
			},
		},
		{
			name: "time text fallback remains incremental",
			response: dictionaryListColumn(
				kdb.Atom(kdb.KC, "x"),
				kdb.Atom(-kdb.KP, instant),
			),
			requiredBytes: uint64(len("x") + len(instantText)),
			want:          []interface{}{"x", instantText},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := assertDictionaryListStringBudget(t, tt.response, tt.requiredBytes)
			assertFieldValues(t, fieldByName(t, onlyFrame(t, frames), "value"), tt.want)
		})
	}
}

func TestKdbDictionaryListDirectStringPerCellBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		value    *kdb.K
		maximum  uint64
		wantText string
	}{
		{
			name:     "KC byte slice",
			value:    &kdb.K{Type: kdb.KC, Attr: kdb.NONE, Data: []byte("abcd")},
			maximum:  3,
			wantText: "per-value byte limit",
		},
		{
			name:     "UUID",
			value:    kdb.Atom(-kdb.UU, uuid.UUID{}),
			maximum:  kdbUUIDTextBytes - 1,
			wantText: "per-value byte limit",
		},
		{
			name:     "expanded character byte",
			value:    kdb.Atom(-kdb.KC, byte(0x80)),
			maximum:  1,
			wantText: "per-value byte limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := dictionaryListColumn(tt.value)
			limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxCellStringBytes = tt.maximum
			})
			assertLimitedParseError(
				t,
				response,
				QueryModel{CompatibilityMode: CompatibilityModePanopticon},
				limits,
				tt.wantText,
			)
		})
	}
}

func TestKdbCharacterRowModeChargesEmittedUTF8Bytes(t *testing.T) {
	raw := []byte{0x7f, 0x80, 0xff}
	requiredBase := expectedFrameMaterializedBytes(3, []string{"c"}, "A", "A")
	const emittedBytes = 5 // DEL is one byte; U+0080 and U+00FF are two each.

	for _, tt := range []struct {
		name string
		data interface{}
	}{
		{name: "decoded string storage", data: string(raw)},
		{name: "byte slice storage", data: raw},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := kdb.NewTable(
				[]string{"c"},
				[]*kdb.K{{Type: kdb.KC, Attr: kdb.NONE, Data: tt.data}},
			)
			limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxStringBytes = emittedBytes
				limits.MaxMaterialized = requiredBase + emittedBytes
			})
			frames, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", limits)
			if err != nil {
				t.Fatalf("KC byte boundary should be accepted: %v", err)
			}
			assertFieldValues(
				t,
				onlyFrame(t, frames).Fields[0],
				[]interface{}{string(rune(0x7f)), string(rune(0x80)), string(rune(0xff))},
			)

			limits.MaxStringBytes = emittedBytes - 1
			assertLimitedParseError(t, response, QueryModel{}, limits, "materialized string byte limit")

			limits.MaxStringBytes = emittedBytes
			limits.MaxMaterialized = requiredBase + emittedBytes - 1
			assertLimitedParseError(t, response, QueryModel{}, limits, "materialized byte limit")

			limits.MaxMaterialized = requiredBase + emittedBytes
			limits.MaxCellStringBytes = 1
			assertLimitedParseError(t, response, QueryModel{}, limits, "per-value byte limit")
		})
	}
}

func TestKdbCharacterTextModeRequiresBoundedValidUTF8(t *testing.T) {
	for _, data := range []interface{}{string([]byte{0x80}), []byte{0x80}} {
		invalid := &kdb.K{Type: kdb.KC, Attr: kdb.NONE, Data: data}
		assertParseErrorWithoutPanic(
			t,
			invalid,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			"not valid UTF-8",
		)
	}

	response := &kdb.K{Type: kdb.KC, Attr: kdb.NONE, Data: []byte("abcd")}
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		limits.MaxCellStringBytes = 3
	})
	assertLimitedParseError(
		t,
		response,
		QueryModel{CompatibilityMode: CompatibilityModePanopticon},
		limits,
		"per-value byte limit",
	)
}

func TestKdbDictionaryListMixedNumericPrecision(t *testing.T) {
	const exact = int64(1 << 53)
	tests := []struct {
		name     string
		integer  int64
		nullable bool
		wantText bool
	}{
		{name: "positive exact", integer: exact},
		{name: "negative exact", integer: -exact},
		{name: "positive one over", integer: exact + 1, wantText: true},
		{name: "negative one over", integer: -exact - 1, wantText: true},
		{name: "nullable positive exact", integer: exact, nullable: true},
		{name: "nullable negative exact", integer: -exact, nullable: true},
		{name: "nullable positive one over", integer: exact + 1, nullable: true, wantText: true},
		{name: "nullable negative one over", integer: -exact - 1, nullable: true, wantText: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []*kdb.K{
				kdb.NewDict(kdb.Symbol("value"), kdb.Long(tt.integer)),
				kdb.NewDict(kdb.Symbol("value"), kdb.Float(1.5)),
			}
			if tt.nullable {
				rows = append(rows, kdb.NewDict(kdb.NewList(), kdb.NewList()))
			}
			response := kdb.NewList(rows...)
			frames, err := parseKdbResponseToFrames(
				response,
				QueryModel{CompatibilityMode: CompatibilityModePanopticon},
				"A",
			)
			if err != nil {
				t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
			}
			field := fieldByName(t, onlyFrame(t, frames), "value")
			if tt.wantText {
				want := strconv.FormatInt(tt.integer, 10)
				if tt.nullable {
					got, ok := field.At(0).(*string)
					if !ok || got == nil || *got != want {
						t.Fatalf("large integer was not preserved textually: %#v", field.At(0))
					}
				} else if got := field.At(0); got != want {
					t.Fatalf("large integer was not preserved textually: %#v", got)
				}
				return
			}

			if tt.nullable {
				got, ok := field.At(0).(*float64)
				if !ok || got == nil || *got != float64(tt.integer) {
					t.Fatalf("exact nullable integer was not coerced safely: %#v", field.At(0))
				}
			} else if got := field.At(0); got != float64(tt.integer) {
				t.Fatalf("exact integer was not coerced safely: %#v", got)
			}
		})
	}
}

func TestKdbDictionaryListValidatesKeysBeforeUnionHashing(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		limits kdbFrameParseLimits
		want   string
	}{
		{
			name: "empty",
			key:  "",
			limits: parserTestLimits(func(_ *kdbFrameParseLimits) {
			}),
			want: "field name is empty",
		},
		{
			name: "control",
			key:  "a\nb",
			limits: parserTestLimits(func(_ *kdbFrameParseLimits) {
			}),
			want: "control character",
		},
		{
			name: "invalid utf8",
			key:  string([]byte{0xff}),
			limits: parserTestLimits(func(_ *kdbFrameParseLimits) {
			}),
			want: "not valid UTF-8",
		},
		{
			name: "configured byte limit",
			key:  "abcd",
			limits: parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxFieldNameBytes = 3
			}),
			want: "field name exceeds byte limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := kdb.NewList(kdb.NewDict(kdb.Symbol(tt.key), kdb.Long(1)))
			assertLimitedParseError(
				t,
				response,
				QueryModel{CompatibilityMode: CompatibilityModePanopticon},
				tt.limits,
				tt.want,
			)
		})
	}

	response := kdb.NewList(
		kdb.NewDict(kdb.Symbol("aa"), kdb.Long(1)),
		kdb.NewDict(kdb.Symbol("bb"), kdb.Long(2)),
	)
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		// Frame name + retained RefID + first key fits; the second does not.
		limits.MaxNameBytesTotal = uint64(len("A") + len("A") + len("aa") + len("b"))
	})
	assertLimitedParseError(
		t,
		response,
		QueryModel{CompatibilityMode: CompatibilityModePanopticon},
		limits,
		"field-name byte limit",
	)
}

func TestKdbFrameParserValidatesAndAccountsRefID(t *testing.T) {
	response := groupedBudgetResponse(2, 1, 1)
	for _, refID := range []string{
		"",
		" A",
		"A\nB",
		string([]byte{0xff}),
		strings.Repeat("A", maxRefIDBytes+1),
	} {
		_, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, refID, defaultKdbFrameParseLimits)
		if err == nil || !strings.Contains(err.Error(), "invalid RefID") {
			t.Fatalf("expected invalid RefID rejection for %q, got %v", refID, err)
		}
	}
	malformed := &kdb.K{Type: kdb.KJ, Attr: kdb.NONE}
	if _, err := parseKdbResponseToFramesWithLimits(malformed, QueryModel{}, "", defaultKdbFrameParseLimits); err == nil ||
		!strings.Contains(err.Error(), "invalid RefID") {
		t.Fatalf("RefID should be rejected before response dispatch, got %v", err)
	}

	const refID = "RR"
	// Two one-byte frame names, two six-byte field names, and RefID retained
	// by both frames.
	requiredNameBytes := uint64(2*len("A") + 2*len("value0") + 2*len(refID))
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		limits.MaxNameBytesTotal = requiredNameBytes
	})
	if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, refID, limits); err != nil {
		t.Fatalf("grouped RefID metadata boundary should be accepted: %v", err)
	}
	limits.MaxNameBytesTotal = requiredNameBytes - 1
	if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, refID, limits); err == nil ||
		!strings.Contains(err.Error(), "field-name byte limit") {
		t.Fatalf("expected grouped RefID metadata rejection, got %v", err)
	}

	requiredMaterialized := expectedFrameMaterializedBytes(1, []string{"value0"}, "A", refID) +
		expectedFrameMaterializedBytes(1, []string{"value0"}, "B", refID)
	limits = parserTestLimits(func(limits *kdbFrameParseLimits) {
		limits.MaxMaterialized = requiredMaterialized
	})
	if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, refID, limits); err != nil {
		t.Fatalf("grouped RefID materialized boundary should be accepted: %v", err)
	}
	limits.MaxMaterialized = requiredMaterialized - 1
	if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, refID, limits); err == nil ||
		!strings.Contains(err.Error(), "materialized byte limit") {
		t.Fatalf("expected grouped RefID materialized rejection, got %v", err)
	}
}

func TestGroupedPlanningDoesNotRetainUnusedKeyAtoms(t *testing.T) {
	const dimension = 1024
	keyValues := make([]string, dimension)
	for i := range keyValues {
		keyValues[i] = "x"
	}
	keyNames := make([]string, dimension)
	keyColumns := make([]*kdb.K, dimension)
	for i := range keyColumns {
		keyNames[i] = "key" + strconv.Itoa(i)
		keyColumns[i] = kdb.SymbolV(keyValues)
	}
	groupValue := kdb.LongV([]int64{1})
	groups := make([]*kdb.K, dimension)
	for i := range groups {
		groups[i] = groupValue
	}
	response := kdb.NewDict(
		kdb.NewTable(keyNames, keyColumns),
		kdb.NewTable([]string{"value"}, []*kdb.K{kdb.NewList(groups...)}),
	)
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		// Force planning to consume every one of the 1,048,576 key cells.
		limits.MaxFieldNameBytes = 8 << 10
		limits.MaxNameBytesTotal = 8 << 20
	})

	allocations := testing.AllocsPerRun(1, func() {
		frames, err := parseKdbResponseToFramesWithLimits(
			response,
			QueryModel{IncludeKeyColumns: false},
			"A",
			limits,
		)
		if err != nil {
			panic(fmt.Sprintf("unexpected grouped planning error: %v", err))
		}
		if len(frames) != dimension || len(frames[0].Fields) != 1 {
			panic(fmt.Sprintf("unexpected grouped frame shape: %d frames", len(frames)))
		}
	})
	if allocations > 20_000 {
		t.Fatalf("unused 1024x1024 key planning allocated %.0f objects; key atoms appear retained", allocations)
	}
}

func TestGroupedFrameNameStreamingPreservesLegacyScalarText(t *testing.T) {
	instant := time.Date(2026, time.July, 29, 12, 34, 56, 789_000_000, time.FixedZone("test", 3600))
	identifier := uuid.UUID{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	vectors := []*kdb.K{
		kdb.NewList(kdb.Atom(kdb.KC, "text")),
		{Type: kdb.KB, Attr: kdb.NONE, Data: []bool{true}},
		{Type: kdb.UU, Attr: kdb.NONE, Data: []uuid.UUID{identifier}},
		{Type: kdb.KG, Attr: kdb.NONE, Data: []byte{255}},
		{Type: kdb.KH, Attr: kdb.NONE, Data: []int16{-12}},
		kdb.IntV([]int32{-34}),
		kdb.LongV([]int64{-56}),
		kdb.RealV([]float32{1.25}),
		kdb.FloatV([]float64{2.5}),
		{Type: kdb.KC, Attr: kdb.NONE, Data: []byte{0xff}},
		{Type: kdb.KC, Attr: kdb.NONE, Data: string([]byte{0xff})},
		kdb.SymbolV([]string{"symbol"}),
		{Type: kdb.KP, Attr: kdb.NONE, Data: []time.Time{instant}},
		{Type: kdb.KM, Attr: kdb.NONE, Data: []kdb.Month{13}},
		kdb.DateV([]time.Time{instant}),
		{Type: kdb.KZ, Attr: kdb.NONE, Data: []time.Time{instant}},
		{Type: kdb.KN, Attr: kdb.NONE, Data: []time.Duration{time.Second + 2}},
		{Type: kdb.KU, Attr: kdb.NONE, Data: []kdb.Minute{kdb.Minute(instant)}},
		{Type: kdb.KV, Attr: kdb.NONE, Data: []kdb.Second{kdb.Second(instant)}},
		{Type: kdb.KT, Attr: kdb.NONE, Data: []kdb.Time{kdb.Time(instant)}},
	}

	for _, vector := range vectors {
		item, ok := correctedIndexValidated(vector, 0)
		if !ok {
			t.Fatalf("legacy indexing failed for type %d", vector.Type)
		}
		want, err := scalarKdbText(item)
		if err != nil {
			t.Fatalf("legacy scalar conversion failed for type %d: %v", vector.Type, err)
		}
		got, err := scalarKdbVectorTextAt(vector, 0, maxKdbCellStringBytes)
		if err != nil {
			t.Fatalf("streaming scalar conversion failed for type %d: %v", vector.Type, err)
		}
		if got != want {
			t.Fatalf("streaming scalar conversion for type %d: got %q want %q", vector.Type, got, want)
		}
	}
}

func TestKdbFrameParserRejectsAmplifyingShapesBeforeMaterialization(t *testing.T) {
	t.Run("sparse dictionary union", func(t *testing.T) {
		response := kdb.NewList(
			kdb.NewDict(kdb.Symbol("a"), kdb.Long(1)),
			kdb.NewDict(kdb.Symbol("b"), kdb.Long(2)),
			kdb.NewDict(kdb.Symbol("c"), kdb.Long(3)),
		)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxCellsTotal = 8
		})
		assertLimitedParseError(
			t,
			response,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			limits,
			"materialized cell limit",
		)
	})

	t.Run("grouped key projection", func(t *testing.T) {
		response := groupedBudgetResponse(2, 3, 1)
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxCellsTotal = 11
		})
		assertLimitedParseError(
			t,
			response,
			QueryModel{IncludeKeyColumns: true},
			limits,
			"materialized cell limit",
		)
	})

	t.Run("overflowed dimensions", func(t *testing.T) {
		maximum := ^uint64(0)
		limits := kdbFrameParseLimits{
			MaxFrames:          maximum,
			MaxFieldsPerFrame:  maximum,
			MaxFieldsTotal:     maximum,
			MaxRowsTotal:       maximum,
			MaxCellsTotal:      maximum,
			MaxFieldNameBytes:  maximum,
			MaxNameBytesTotal:  maximum,
			MaxStringBytes:     maximum,
			MaxMaterialized:    maximum,
			MaxDepth:           maxKdbObjectDepth,
			MaxObjects:         maximum,
			MaxEdges:           maximum,
			MaxCellStringBytes: maximum,
		}
		budget := newKdbFrameParseBudget(limits)
		maximumInt := int(^uint(0) >> 1)
		err := budget.reserveFrame("A", []string{"a", "b", "c"}, maximumInt, "")
		if err == nil || !strings.Contains(err.Error(), "dimensions overflow") {
			t.Fatalf("expected checked dimension overflow, got %v", err)
		}
	})
}

func TestKdbFrameParserRejectsUnsafeFieldNames(t *testing.T) {
	tests := []struct {
		name       string
		fieldNames []string
		want       string
	}{
		{name: "empty", fieldNames: []string{""}, want: "field name is empty"},
		{name: "control", fieldNames: []string{"a\nb"}, want: "control character"},
		{name: "nul", fieldNames: []string{"a\x00b"}, want: "control character"},
		{name: "invalid utf8", fieldNames: []string{string([]byte{0xff})}, want: "not valid UTF-8"},
		{name: "duplicate", fieldNames: []string{"a", "a"}, want: "duplicate field name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := simpleBudgetTable(1, tt.fieldNames...)
			assertParseErrorWithoutPanic(t, response, QueryModel{}, tt.want)
		})
	}
}

func TestKdbFrameParserDoesNotMutateSource(t *testing.T) {
	names := []string{"sym", "value"}
	symbols := []string{"A", "B"}
	values := []int64{1, 2}
	response := kdb.NewTable(names, []*kdb.K{kdb.SymbolV(symbols), kdb.LongV(values)})

	frames, err := parseKdbResponseToFrames(response, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"sym", "value"}) ||
		!reflect.DeepEqual(symbols, []string{"A", "B"}) ||
		!reflect.DeepEqual(values, []int64{1, 2}) {
		t.Fatal("parser mutated source kdb+ data")
	}

	frame := onlyFrame(t, frames)
	frame.Fields[0].Name = "frame_sym"
	frame.Fields[0].Set(0, "FRAME")
	frame.Fields[1].Set(0, int64(99))
	if !reflect.DeepEqual(names, []string{"sym", "value"}) ||
		!reflect.DeepEqual(symbols, []string{"A", "B"}) ||
		!reflect.DeepEqual(values, []int64{1, 2}) {
		t.Fatal("mutating the emitted frame changed source kdb+ data")
	}

	names[1] = "source_value"
	symbols[1] = "SOURCE"
	values[1] = 88
	if frame.Fields[1].Name != "value" ||
		frame.Fields[0].At(1) != "B" ||
		frame.Fields[1].At(1) != int64(2) {
		t.Fatal("mutating source kdb+ data changed the emitted frame")
	}
}

func FuzzParseKdbResponseToFramesBoundedAndPanicFree(f *testing.F) {
	f.Add([]byte("abc"), byte(0))
	f.Add([]byte{0xff, 0x00}, byte(1))
	f.Add([]byte("value"), byte(2))
	for shape := byte(3); shape < 16; shape++ {
		f.Add([]byte{shape, 0x00, 0x80, 0xff}, shape)
	}
	for _, primitive := range []byte{41, 255, 42, 254} {
		f.Add([]byte{primitive}, byte(15))
	}
	f.Fuzz(func(t *testing.T, raw []byte, shape byte) {
		if len(raw) > 256 {
			raw = raw[:256]
		}
		var response *kdb.K
		switch shape % 16 {
		case 0:
			response = kdb.NewTable([]string{string(raw)}, []*kdb.K{kdb.LongV([]int64{1})})
		case 1:
			response = kdb.NewList(kdb.Atom(kdb.KC, string(raw)), kdb.Long(int64(len(raw))))
		case 2:
			response = kdb.NewDict(kdb.Symbol(string(raw)), kdb.Long(int64(len(raw))))
		case 3:
			response = &kdb.K{Type: int8(shape), Attr: kdb.NONE, Data: raw}
		case 4:
			response = &kdb.K{Type: kdb.K0, Attr: kdb.NONE}
			response.Data = []*kdb.K{response}
		case 5:
			response = aliasedDepthDAG(len(raw)%2 == 0)
		case 6:
			child := kdb.Long(int64(len(raw)))
			sharedBacking := []*kdb.K{child}
			response = kdb.NewList(
				&kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking},
				&kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking},
			)
		case 7:
			response = kdb.NewTable(
				[]string{"a", "b"},
				[]*kdb.K{kdb.LongV([]int64{int64(len(raw))})},
			)
		case 8:
			response = &kdb.K{
				Type: kdb.XD,
				Attr: kdb.NONE,
				Data: kdb.Dict{Value: kdb.Long(int64(len(raw)))},
			}
		case 9:
			response = kdb.NewTable(
				[]string{"c"},
				[]*kdb.K{{Type: kdb.KC, Attr: kdb.NONE, Data: append([]byte(nil), raw...)}},
			)
		case 10:
			response = &kdb.K{Type: -kdb.KJ, Attr: kdb.SORTED, Data: int64(len(raw))}
		case 11:
			operand := kdb.NewList(kdb.Long(int64(len(raw))))
			response = &kdb.K{Type: kdb.KPROJ, Attr: kdb.NONE, Data: []*kdb.K{operand, operand}}
		case 12:
			rows := len(raw) % 16
			response = simpleBudgetTable(rows, "value")
			required := expectedFrameMaterializedBytes(rows, []string{"value"}, "A", "A")
			exact := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxMaterialized = required
			})
			if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", exact); err != nil {
				t.Fatalf("exact materialized boundary failed: %v", err)
			}
			exact.MaxMaterialized--
			if _, err := parseKdbResponseToFramesWithLimits(response, QueryModel{}, "A", exact); err == nil {
				t.Fatal("one-under materialized boundary was accepted")
			}
			return
		case 13:
			child := kdb.Long(int64(len(raw)))
			sharedBacking := []*kdb.K{child}
			response = kdb.NewList(
				&kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking},
				&kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: sharedBacking},
			)
			exact := parserTestLimits(func(limits *kdbFrameParseLimits) {
				limits.MaxEdges = 4
			})
			if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(exact)); err != nil {
				t.Fatalf("exact edge boundary failed: %v", err)
			}
			exact.MaxEdges--
			if err := validateKdbObjectWithBudget(response, newKdbFrameParseBudget(exact)); err == nil {
				t.Fatal("one-under edge boundary was accepted")
			}
			return
		case 14:
			source := append([]byte(nil), raw...)
			response = kdb.NewTable(
				[]string{"value"},
				[]*kdb.K{{Type: kdb.KG, Attr: kdb.NONE, Data: source}},
			)
			frames, err := parseKdbResponseToFrames(response, QueryModel{}, "A")
			if err != nil {
				t.Fatalf("byte-vector parse failed: %v", err)
			}
			if len(source) == 0 {
				return
			}
			field := onlyFrame(t, frames).Fields[0]
			original := source[0]
			source[0] ^= 0xff
			if got := field.At(0); got != original {
				t.Fatalf("source mutation leaked into frame: got %#v want %#v", got, original)
			}
			mutatedSource := source[0]
			field.Set(0, original^0x55)
			if source[0] != mutatedSource {
				t.Fatalf("frame mutation leaked into source: got %#v want %#v", source[0], mutatedSource)
			}
			return
		case 15:
			index := shape
			if len(raw) > 0 {
				index = raw[0]
			}
			response = &kdb.K{Type: kdb.KFUNCUP, Attr: kdb.NONE, Data: index}
		}
		limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
			limits.MaxRowsTotal = 1024
			limits.MaxCellsTotal = 4096
			limits.MaxMaterialized = 1 << 20
			limits.MaxStringBytes = 1 << 16
			limits.MaxDepth = 16
			limits.MaxObjects = 4096
			limits.MaxEdges = 4096
		})
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("parser panicked for fuzz input: %v", recovered)
			}
		}()
		_, _ = parseKdbResponseToFramesWithLimits(
			response,
			QueryModel{CompatibilityMode: CompatibilityModePanopticon},
			"A",
			limits,
		)
	})
}

func parserTestLimits(update func(*kdbFrameParseLimits)) kdbFrameParseLimits {
	limits := defaultKdbFrameParseLimits
	update(&limits)
	return limits
}

func assertLimitedParseError(t *testing.T, response *kdb.K, model QueryModel, limits kdbFrameParseLimits, want string) {
	t.Helper()
	_, err := parseKdbResponseToFramesWithLimits(response, model, "A", limits)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

func simpleBudgetTable(rows int, names ...string) *kdb.K {
	columns := make([]*kdb.K, len(names))
	for i := range columns {
		values := make([]int64, rows)
		for row := range values {
			values[row] = int64(i + row)
		}
		columns[i] = kdb.LongV(values)
	}
	return kdb.NewTable(names, columns)
}

func expectedFrameMaterializedBytes(rows int, fieldNames []string, frameName string, refID string) uint64 {
	nameBytes := uint64(len(frameName) + len(refID))
	for _, name := range fieldNames {
		nameBytes += uint64(len(name))
	}
	return uint64(rows*len(fieldNames))*estimatedKdbCellBytes +
		uint64(len(fieldNames))*estimatedKdbFieldBytes +
		estimatedKdbFrameBytes +
		nameBytes
}

func sparseDictionaryColumn(columnName string, value *kdb.K, firstID *kdb.K, secondID *kdb.K) *kdb.K {
	return kdb.NewList(
		kdb.NewDict(
			kdb.SymbolV([]string{columnName, "id"}),
			kdb.NewList(value, firstID),
		),
		kdb.NewDict(kdb.Symbol("id"), secondID),
	)
}

func dictionaryListColumn(values ...*kdb.K) *kdb.K {
	key := kdb.Symbol("value")
	rows := make([]*kdb.K, len(values))
	for i, value := range values {
		rows[i] = kdb.NewDict(key, value)
	}
	return kdb.NewList(rows...)
}

func assertDictionaryListStringBudget(t *testing.T, response *kdb.K, required uint64) []*data.Frame {
	t.Helper()
	limits := parserTestLimits(func(limits *kdbFrameParseLimits) {
		limits.MaxStringBytes = required
	})
	frames, err := parseKdbResponseToFramesWithLimits(
		response,
		QueryModel{CompatibilityMode: CompatibilityModePanopticon},
		"A",
		limits,
	)
	if err != nil {
		t.Fatalf("exact direct-string boundary should be accepted: %v", err)
	}
	limits.MaxStringBytes = required - 1
	assertLimitedParseError(
		t,
		response,
		QueryModel{CompatibilityMode: CompatibilityModePanopticon},
		limits,
		"materialized string byte limit",
	)
	return frames
}

func stringPointer(value string) *string {
	return &value
}

func groupedBudgetResponse(frameCount int, rowsPerFrame int, valueColumns int) *kdb.K {
	keys := make([]string, frameCount)
	for i := range keys {
		keys[i] = string(rune('A' + i))
	}
	columns := make([]string, valueColumns)
	data := make([]*kdb.K, valueColumns)
	for column := 0; column < valueColumns; column++ {
		columns[column] = "value" + strconv.Itoa(column)
		groups := make([]*kdb.K, frameCount)
		for frame := range groups {
			values := make([]int64, rowsPerFrame)
			for row := range values {
				values[row] = int64(column + frame + row)
			}
			groups[frame] = kdb.LongV(values)
		}
		data[column] = kdb.NewList(groups...)
	}
	return kdb.NewDict(
		kdb.NewTable([]string{"group"}, []*kdb.K{kdb.SymbolV(keys)}),
		kdb.NewTable(columns, data),
	)
}

func BenchmarkCorrectedTableIndexValidated(b *testing.B) {
	const rowCount = 8192
	longs := make([]int64, rowCount)
	symbols := make([]string, rowCount)
	for i := 0; i < rowCount; i++ {
		longs[i] = int64(i)
		symbols[i] = "sym"
	}
	table := kdb.Table{
		Columns: []string{"id", "sym", "value"},
		Data: []*kdb.K{
			kdb.LongV(longs),
			kdb.SymbolV(symbols),
			kdb.LongV(longs),
		},
	}
	if err := validateKdbObject(&kdb.K{Type: kdb.XT, Attr: kdb.NONE, Data: table}); err != nil {
		b.Fatalf("invalid benchmark table: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := correctedTableIndexValidated(table, i%rowCount); err != nil {
			b.Fatal(err)
		}
	}
}
