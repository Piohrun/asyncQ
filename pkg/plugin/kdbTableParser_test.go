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
