package plugin

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
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
	accepted := nestedKdbList(maxKdbObjectDepth)
	if _, err := ParseKdbObjectAsFrame(accepted); err != nil {
		t.Fatalf("depth %d should be accepted: %v", maxKdbObjectDepth, err)
	}

	rejected := nestedKdbList(maxKdbObjectDepth + 1)
	if _, err := ParseKdbObjectAsFrame(rejected); err == nil {
		t.Fatalf("depth %d should be rejected", maxKdbObjectDepth+1)
	} else if !strings.Contains(err.Error(), "exceeds maximum nesting depth") {
		t.Fatalf("unexpected depth error: %v", err)
	}
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

func TestParseSimpleTablePreservesSupportedOpaqueGenericValues(t *testing.T) {
	res := kdb.NewTable(
		[]string{"mixed"},
		[]*kdb.K{
			kdb.NewList(
				kdb.Long(1),
				&kdb.K{Type: kdb.KFUNCUP, Attr: kdb.NONE, Data: byte(0)},
			),
		},
	)
	frames, err := parseKdbResponseToFrames(res, QueryModel{}, "A")
	if err != nil {
		t.Fatalf("parseKdbResponseToFrames returned error: %v", err)
	}
	field := onlyFrame(t, frames).Fields[0]
	for i, want := range []string{"1", "0"} {
		if got := field.At(i); got != want {
			t.Fatalf("unexpected opaque value at %d: got %#v want %#v", i, got, want)
		}
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
