package plugin

import (
	"reflect"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/sv/kdbgo"
)

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
