package kdb

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKStringPreservesRepresentativeLegacyRendering(t *testing.T) {
	tests := []struct {
		name  string
		value *K
		want  string
	}{
		{name: "atom", value: Int(42), want: "42"},
		{name: "vector", value: IntV([]int32{1, 2}), want: "[1 2]"},
		{name: "list", value: NewList(Int(1), Symbol("x")), want: "(1;x)"},
		{
			name:  "dictionary",
			value: NewDict(SymbolV([]string{"a"}), NewList(Int(1))),
			want:  "[a]![1]",
		},
		{
			name:  "table",
			value: NewTable([]string{"a"}, []*K{IntV([]int32{1, 2})}),
			want:  "+[a]!([1 2])",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.value.String(); got != test.want {
				t.Fatalf("legacy rendering mismatch:\nwant %q\ngot  %q", test.want, got)
			}
		})
	}
}

func TestKStringBoundsCallerConstructedCyclesAndOutput(t *testing.T) {
	cycle := &K{Type: K0, Attr: NONE}
	cycle.Data = []*K{cycle}
	if got := cycle.String(); got != "((unknown))" {
		t.Fatalf("cycle rendering = %q, want %q", got, "((unknown))")
	}

	deep := Long(1)
	for index := 0; index < maxKStringDepth+10; index++ {
		deep = NewList(deep)
	}
	if got := deep.String(); len(got) > maxKStringBytes || !strings.Contains(got, "unknown") {
		t.Fatalf("deep rendering was not bounded: length=%d suffix=%q", len(got), got[max(0, len(got)-16):])
	}

	huge := &K{Type: KC, Attr: NONE, Data: strings.Repeat("x", maxKStringBytes+100)}
	if got := huge.String(); len(got) > maxKStringBytes || !strings.HasSuffix(got, "...") {
		t.Fatalf("huge rendering was not truncated: length=%d", len(got))
	}

	unicodeHuge := &K{Type: KC, Attr: NONE, Data: strings.Repeat("€", maxKStringBytes)}
	if got := unicodeHuge.String(); len(got) > maxKStringBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "...") {
		t.Fatalf("UTF-8 rendering was not safely truncated: length=%d valid=%v", len(got), utf8.ValidString(got))
	}

	projection := &K{Type: KPROJ, Attr: NONE, Data: make([]*K, maxKStringElements+1)}
	if got := projection.String(); got != "unknown" {
		t.Fatalf("oversized projection rendering = %q, want unknown", got)
	}
}

type panicStringer struct{}

func (panicStringer) String() string {
	panic("unexpected String invocation")
}

func TestKStringFailsClosedForUnexpectedDataWithoutCallingStringer(t *testing.T) {
	malformed := &K{Type: KI, Attr: NONE, Data: panicStringer{}}
	if got := malformed.String(); got != "unknown" {
		t.Fatalf("malformed vector rendering = %q, want unknown", got)
	}
}

func TestKStringRecoversAndBoundsCallerErrorMethods(t *testing.T) {
	panicking := &callbackError{panics: true}
	if got := Error(panicking).String(); got != "unknown" {
		t.Fatalf("panicking Error rendering = %q, want unknown", got)
	}
	if panicking.calls != 1 {
		t.Fatalf("panicking Error invoked %d times, want once", panicking.calls)
	}

	huge := &callbackError{texts: []string{strings.Repeat("x", maxKStringBytes+100)}}
	if got := Error(huge).String(); len(got) > maxKStringBytes || !strings.HasSuffix(got, "...") {
		t.Fatalf("huge Error rendering was not bounded: length=%d", len(got))
	}
	if huge.calls != 1 {
		t.Fatalf("huge Error invoked %d times, want once", huge.calls)
	}
}

func TestExportedDictAndTableStringUseBoundedCycleSafeRenderer(t *testing.T) {
	cycle := &K{Type: K0, Attr: NONE}
	cycle.Data = []*K{cycle}

	dict := Dict{Key: SymbolV([]string{"x"}), Value: cycle}
	if got := dict.String(); len(got) > maxKStringBytes || !strings.Contains(got, "unknown") {
		t.Fatalf("dictionary cycle was not bounded: %q", got)
	}

	table := Table{Columns: []string{"x"}, Data: []*K{cycle}}
	if got := table.String(); len(got) > maxKStringBytes || !strings.Contains(got, "unknown") {
		t.Fatalf("table cycle was not bounded: %q", got)
	}
}

func TestNilTableIndexDoesNotPanic(t *testing.T) {
	var table *Table
	row := table.Index(0)
	if row.Key != nil || row.Value != nil {
		t.Fatalf("nil table returned a populated row: %#v", row)
	}
}

type unicodeStructTarget struct {
	Éclair int32
}

type namedStringKey string

func TestUnmarshalDictRejectsNonStructAndHandlesUnicode(t *testing.T) {
	dict := Dict{
		Key:   SymbolV([]string{"éclair"}),
		Value: NewList(Int(7)),
	}
	var nonStruct int
	if err := UnmarshalDict(dict, &nonStruct); err == nil {
		t.Fatal("non-struct pointer was accepted")
	}
	var target unicodeStructTarget
	if err := UnmarshalDict(dict, &target); err != nil {
		t.Fatalf("unicode struct unmarshal failed: %v", err)
	}
	if target.Éclair != 7 {
		t.Fatalf("unicode field value = %d, want 7", target.Éclair)
	}
}

func TestUnmarshalDictToMapSupportsNamedStringKeys(t *testing.T) {
	dict := Dict{
		Key:   SymbolV([]string{"éclair"}),
		Value: NewList(Int(7)),
	}
	target := map[namedStringKey]int32{}
	if err := UnmarshalDictToMap(dict, target); err != nil {
		t.Fatalf("named string-key map unmarshal failed: %v", err)
	}
	if target[namedStringKey("Éclair")] != 7 {
		t.Fatalf("named map key was not populated: %#v", target)
	}
}

func TestUnmarshalTableRejectsMalformedShapesWithoutPanicking(t *testing.T) {
	type row struct {
		A int32
		B int32
	}
	tests := []struct {
		name  string
		table Table
		out   interface{}
	}{
		{
			name:  "column count",
			table: Table{Columns: []string{"a"}, Data: []*K{IntV([]int32{1}), IntV([]int32{2})}},
			out:   &[]row{},
		},
		{
			name:  "unequal lengths",
			table: Table{Columns: []string{"a", "b"}, Data: []*K{IntV([]int32{1}), IntV([]int32{2, 3})}},
			out:   &[]row{},
		},
		{
			name:  "nil column",
			table: Table{Columns: []string{"a"}, Data: []*K{nil}},
			out:   &[]row{},
		},
		{
			name:  "scalar column",
			table: Table{Columns: []string{"a"}, Data: []*K{Int(1)}},
			out:   &[]row{},
		},
		{
			name:  "non-struct slice",
			table: Table{Columns: []string{"a"}, Data: []*K{IntV([]int32{1})}},
			out:   &[]int32{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := UnmarshalTable(test.table, test.out); err == nil {
				t.Fatal("malformed table/target was accepted")
			}
		})
	}

	valid := Table{
		Columns: []string{"a", "b"},
		Data:    []*K{IntV([]int32{1, 2}), IntV([]int32{3, 4})},
	}
	var rows []row
	if _, err := UnmarshalTable(valid, &rows); err != nil {
		t.Fatalf("valid table unmarshal failed: %v", err)
	}
	if len(rows) != 2 || rows[0] != (row{A: 1, B: 3}) || rows[1] != (row{A: 2, B: 4}) {
		t.Fatalf("unexpected table rows: %#v", rows)
	}
}
