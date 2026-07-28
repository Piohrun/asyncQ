package kdb

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	uuid "github.com/nu7hatch/gouuid"
)

// ReqType represents a q IPC request type.
type ReqType int8

const (
	ASYNC    ReqType = 0
	SYNC             = 1
	RESPONSE         = 2
)

// Attr is an attribute set on a non-scalar q object.
type Attr int8

const (
	NONE Attr = iota
	SORTED
	UNIQUE
	PARTED
	GROUPED
)

// q type constants.
const (
	K0 int8 = 0
	KB int8 = 1
	UU int8 = 2
	KG int8 = 4
	KH int8 = 5
	KI int8 = 6
	KJ int8 = 7
	KE int8 = 8
	KF int8 = 9
	KC int8 = 10
	KS int8 = 11
	KP int8 = 12
	KM int8 = 13
	KD int8 = 14
	KZ int8 = 15
	KN int8 = 16
	KU int8 = 17
	KV int8 = 18
	KT int8 = 19

	XT int8 = 98
	XD int8 = 99
	SD int8 = 127

	KFUNC      int8 = 100
	KFUNCUP    int8 = 101
	KFUNCBP    int8 = 102
	KFUNCTR    int8 = 103
	KPROJ      int8 = 104
	KCOMP      int8 = 105
	KEACH      int8 = 106
	KOVER      int8 = 107
	KSCAN      int8 = 108
	KPRIOR     int8 = 109
	KEACHRIGHT int8 = 110
	KEACHLEFT  int8 = 111
	KDYNLOAD   int8 = 112

	KERR int8 = -128
)

const (
	Nh int16 = math.MinInt16
	Wh int16 = math.MaxInt16
	Ni int32 = math.MinInt32
	Wi int32 = math.MaxInt32
	Nj int64 = math.MinInt64
	Wj int64 = math.MaxInt64
)

var (
	Ne = float32(math.NaN())
	We = float32(math.Inf(+1))
	Nf = math.NaN()
	Wf = math.Inf(+1)
)

// K is a decoded q value.
type K struct {
	Type int8
	Attr Attr
	Data interface{}
}

func Int(x int32) *K         { return &K{-KI, NONE, x} }
func IntV(x []int32) *K      { return &K{KI, NONE, x} }
func Long(x int64) *K        { return &K{-KJ, NONE, x} }
func LongV(x []int64) *K     { return &K{KJ, NONE, x} }
func Real(x float32) *K      { return &K{-KE, NONE, x} }
func RealV(x []float32) *K   { return &K{KE, NONE, x} }
func Float(x float64) *K     { return &K{-KF, NONE, x} }
func FloatV(x []float64) *K  { return &K{KF, NONE, x} }
func Error(x error) *K       { return &K{KERR, NONE, x} }
func Symbol(x string) *K     { return &K{-KS, NONE, x} }
func SymbolV(x []string) *K  { return &K{KS, NONE, x} }
func Date(x time.Time) *K    { return &K{-KD, NONE, x} }
func DateV(x []time.Time) *K { return &K{KD, NONE, x} }
func Atom(t int8, x any) *K  { return &K{t, NONE, x} }
func NewList(x ...*K) *K     { return &K{K0, NONE, x} }
func NewFunc(ctx, body string) *K {
	return &K{KFUNC, NONE, Function{Namespace: ctx, Body: body}}
}

// Len returns the logical length of a K value, or -1 for an invalid shape.
func (k *K) Len() int {
	length, ok := logicalLength(k)
	if !ok {
		return -1
	}
	return length
}

func logicalLength(value *K) (int, bool) {
	return logicalLengthWithMaxDepth(value, defaultMaxDepth)
}

// logicalLengthWithMaxDepth follows the key side of dictionaries and the
// first column of tables without recursion. Caller-created K graphs are not
// necessarily trees, so both repeated pointers and excessive indirection are
// invalid shapes.
func logicalLengthWithMaxDepth(value *K, maxDepth int) (int, bool) {
	if maxDepth < 1 {
		return 0, false
	}
	visited := make(map[*K]struct{}, min(maxDepth, defaultMaxDepth))
	for depth := 1; depth <= maxDepth; depth++ {
		if value == nil {
			return 0, false
		}
		if _, exists := visited[value]; exists {
			return 0, false
		}
		visited[value] = struct{}{}

		switch value.Type {
		case SD, XD:
			dict, ok := value.Data.(Dict)
			if !ok || dict.Key == nil {
				return 0, false
			}
			value = dict.Key
			continue
		case XT:
			table, ok := value.Data.(Table)
			if !ok {
				return 0, false
			}
			if len(table.Data) == 0 {
				return 0, true
			}
			value = table.Data[0]
			continue
		}

		if value.Type < K0 || value.Type >= KFUNC {
			return 1, true
		}
		if value.Type >= K0 && value.Type <= KT {
			reflected := reflect.ValueOf(value.Data)
			if !reflected.IsValid() {
				return 0, false
			}
			switch reflected.Kind() {
			case reflect.Array, reflect.Slice, reflect.String:
				return reflected.Len(), true
			default:
				return 0, false
			}
		}
		return 0, false
	}
	return 0, false
}

// Index returns the i'th element, or nil when the value or index is invalid.
func (k *K) Index(i int) interface{} {
	if k == nil || i < 0 || k.Type < K0 || k.Type > XT {
		return nil
	}
	n := k.Len()
	if n < 0 || i >= n {
		return nil
	}
	if n == 0 {
		if k.Type == K0 {
			return &K{K0, NONE, []*K{}}
		}
		return nil
	}
	if k.Type >= K0 && k.Type <= KT {
		return reflect.ValueOf(k.Data).Index(i).Interface()
	}
	if k.Type != XT {
		return nil
	}
	t, ok := k.Data.(Table)
	if !ok {
		return nil
	}
	d := t.Index(i)
	return &K{XD, NONE, d}
}

var attrPrint = []string{"", "`s#", "`u#", "`p#", "`g#"}
var unaryops = []string{"::", "+:", "-:", "*:", "%:", "&:", "|:", "^:", "=:", "<:", ">:", "$:", ",:", "#:", "_:", "~:", "!:", "?:", "@:", ".:", "0::", "1::", "2::", "avg", "last", "sum", "prd", "min", "max", "exit", "getenv", "abs", "sqrt", "log", "exp", "sin", "asin", "cos", "acos", "tan", "atan", "enlist"}
var binaryops = []string{":", "+", "-", "*", "%", "&", "|", "^", "=", "<", ">", "$", ",", "#", "_", "~", "!", "?", "@", ".", "0:", "1:", "2:", "in", "within", "like", "bin", "ss", "insert", "wsum", "wavg", "div", "xexp", "setenv"}
var ternaryops = []string{"'", "/", "\\"}
var adverbs = map[int8]string{
	KEACH:      "'",
	KOVER:      "/",
	KSCAN:      "\\",
	KPRIOR:     "':",
	KEACHRIGHT: "/:",
	KEACHLEFT:  "\\:",
}

func attrString(attr Attr) string {
	if int(attr) < 0 || int(attr) >= len(attrPrint) {
		return ""
	}
	return attrPrint[attr]
}

const (
	maxKStringBytes    = 1 << 20
	maxKStringDepth    = 64
	maxKStringObjects  = 1 << 20
	maxKStringElements = 1 << 20
)

type cappedStringWriter struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedStringWriter) remaining() int {
	return maxKStringBytes - w.buf.Len()
}

func (w *cappedStringWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := w.remaining()
	if remaining <= 0 {
		w.truncated = w.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		prefix := value[:remaining]
		if utf8.Valid(value) {
			for len(prefix) > 0 && !utf8.Valid(prefix) {
				prefix = prefix[:len(prefix)-1]
			}
		}
		value = prefix
		w.truncated = true
	}
	_, _ = w.buf.Write(value)
	return originalLength, nil
}

func (w *cappedStringWriter) writeString(value string) {
	remaining := w.remaining()
	if remaining <= 0 {
		w.truncated = w.truncated || len(value) > 0
		return
	}
	if len(value) <= remaining {
		_, _ = w.buf.WriteString(value)
		return
	}
	prefix := value[:remaining]
	if utf8.ValidString(value) {
		for len(prefix) > 0 && !utf8.ValidString(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	_, _ = w.buf.WriteString(prefix)
	w.truncated = true
}

func (w *cappedStringWriter) string() string {
	result := w.buf.String()
	if !w.truncated {
		return result
	}
	if len(result)+3 <= maxKStringBytes {
		return result + "..."
	}
	prefix := result[:maxKStringBytes-3]
	if utf8.ValidString(result) {
		for len(prefix) > 0 && !utf8.ValidString(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix + "..."
}

type kStringState struct {
	writer   cappedStringWriter
	active   map[*K]struct{}
	objects  int
	elements int
}

func safeErrorText(value error) (text string, err error) {
	if value == nil {
		return "", errors.New("nil error")
	}
	defer func() {
		if recover() != nil {
			text = ""
			err = errors.New("error method panicked")
		}
	}()
	return value.Error(), nil
}

func (s *kStringState) consumeElements(count int) bool {
	if count < 0 || count > maxKStringElements-s.elements {
		return false
	}
	s.elements += count
	return true
}

func (s *kStringState) writeDisplayValue(value interface{}, depth int) {
	if s.writer.truncated {
		return
	}
	if depth > maxKStringDepth {
		s.writer.writeString("unknown")
		return
	}
	if value == nil {
		s.writer.writeString("<nil>")
		return
	}
	if item, ok := value.(*K); ok {
		s.writeK(item, depth+1)
		return
	}
	switch typed := value.(type) {
	case string:
		s.writer.writeString(typed)
		return
	case bool:
		s.writer.writeString(strconv.FormatBool(typed))
		return
	case byte:
		s.writer.writeString(strconv.FormatUint(uint64(typed), 10))
		return
	case int16:
		s.writer.writeString(strconv.FormatInt(int64(typed), 10))
		return
	case int32:
		s.writer.writeString(strconv.FormatInt(int64(typed), 10))
		return
	case int64:
		s.writer.writeString(strconv.FormatInt(typed, 10))
		return
	case float32:
		s.writer.writeString(strconv.FormatFloat(float64(typed), 'g', -1, 32))
		return
	case float64:
		s.writer.writeString(strconv.FormatFloat(typed, 'g', -1, 64))
		return
	case time.Time:
		s.writer.writeString(typed.String())
		return
	case time.Duration:
		s.writer.writeString(typed.String())
		return
	case Month:
		s.writer.writeString(typed.String())
		return
	case Minute:
		s.writer.writeString(typed.String())
		return
	case Second:
		s.writer.writeString(typed.String())
		return
	case Time:
		s.writer.writeString(typed.String())
		return
	case error:
		text, err := safeErrorText(typed)
		if err != nil {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(text)
		return
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Array, reflect.Slice:
		length := reflected.Len()
		if !s.consumeElements(length) {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString("[")
		for index := 0; index < length; index++ {
			if index > 0 {
				s.writer.writeString(" ")
			}
			s.writeDisplayValue(reflected.Index(index).Interface(), depth+1)
		}
		s.writer.writeString("]")
	default:
		s.writer.writeString("unknown")
	}
}

func (s *kStringState) writeTypedData(qtype int8, value interface{}, depth int, prefix string) bool {
	switch qtype {
	case -KB:
		if _, ok := value.(bool); !ok {
			return false
		}
	case -UU:
		if _, ok := value.(uuid.UUID); !ok {
			return false
		}
	case -KG, -KC:
		if _, ok := value.(byte); !ok {
			return false
		}
	case -KH:
		if _, ok := value.(int16); !ok {
			return false
		}
	case -KI:
		if _, ok := value.(int32); !ok {
			return false
		}
	case -KJ:
		if _, ok := value.(int64); !ok {
			return false
		}
	case -KE:
		if _, ok := value.(float32); !ok {
			return false
		}
	case -KF:
		if _, ok := value.(float64); !ok {
			return false
		}
	case -KS:
		if _, ok := value.(string); !ok {
			return false
		}
	case -KP:
		if _, ok := value.(time.Time); !ok {
			return false
		}
	case -KM:
		switch value.(type) {
		case Month, int32:
		default:
			return false
		}
	case -KD:
		switch value.(type) {
		case time.Time, int32:
		default:
			return false
		}
	case -KZ:
		switch value.(type) {
		case time.Time, float64:
		default:
			return false
		}
	case -KN:
		if _, ok := value.(time.Duration); !ok {
			return false
		}
	case -KU:
		switch value.(type) {
		case Minute, int32:
		default:
			return false
		}
	case -KV:
		switch value.(type) {
		case Second, int32:
		default:
			return false
		}
	case -KT:
		switch value.(type) {
		case Time, int32:
		default:
			return false
		}
	case KB:
		_, ok := value.([]bool)
		if !ok {
			return false
		}
	case UU:
		_, ok := value.([]uuid.UUID)
		if !ok {
			return false
		}
	case KG:
		_, ok := value.([]byte)
		if !ok {
			return false
		}
	case KC:
		_, ok := value.(string)
		if !ok {
			return false
		}
	case KS:
		_, ok := value.([]string)
		if !ok {
			return false
		}
	case KH:
		_, ok := value.([]int16)
		if !ok {
			return false
		}
	case KI:
		_, ok := value.([]int32)
		if !ok {
			return false
		}
	case KJ:
		_, ok := value.([]int64)
		if !ok {
			return false
		}
	case KE:
		_, ok := value.([]float32)
		if !ok {
			return false
		}
	case KF:
		_, ok := value.([]float64)
		if !ok {
			return false
		}
	case KP:
		_, ok := value.([]time.Time)
		if !ok {
			return false
		}
	case KM:
		switch value.(type) {
		case []Month, []int32:
		default:
			return false
		}
	case KD:
		switch value.(type) {
		case []time.Time, []int32:
		default:
			return false
		}
	case KZ:
		switch value.(type) {
		case []time.Time, []float64:
		default:
			return false
		}
	case KN:
		_, ok := value.([]time.Duration)
		if !ok {
			return false
		}
	case KU:
		switch value.(type) {
		case []Minute, []int32:
		default:
			return false
		}
	case KV:
		switch value.(type) {
		case []Second, []int32:
		default:
			return false
		}
	case KT:
		switch value.(type) {
		case []Time, []int32:
		default:
			return false
		}
	default:
		return false
	}
	s.writer.writeString(prefix)
	s.writeDisplayValue(value, depth)
	return true
}

func (s *kStringState) writeK(k *K, depth int) {
	if s.writer.truncated {
		return
	}
	if k == nil || depth > maxKStringDepth || s.objects >= maxKStringObjects {
		s.writer.writeString("unknown")
		return
	}
	if _, exists := s.active[k]; exists {
		s.writer.writeString("unknown")
		return
	}
	s.objects++
	s.active[k] = struct{}{}
	defer delete(s.active, k)

	if k.Type == KERR {
		value, ok := k.Data.(error)
		if !ok || value == nil {
			s.writer.writeString("unknown")
			return
		}
		s.writeDisplayValue(value, depth)
		return
	}
	if k.Type < K0 {
		if !s.writeTypedData(k.Type, k.Data, depth, "") {
			s.writer.writeString("unknown")
		}
		return
	}
	if k.Type > K0 && k.Type <= KT {
		if !s.writeTypedData(k.Type, k.Data, depth, attrString(k.Attr)) {
			s.writer.writeString("unknown")
		}
		return
	}
	switch k.Type {
	case K0:
		list, ok := k.Data.([]*K)
		if !ok {
			s.writer.writeString("unknown")
			return
		}
		if !s.consumeElements(len(list)) {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(attrString(k.Attr))
		s.writer.writeString("(")
		for index, item := range list {
			s.writeK(item, depth+1)
			if index != len(list)-1 {
				s.writer.writeString(";")
			}
		}
		s.writer.writeString(")")
	case XD, SD:
		dict, ok := k.Data.(Dict)
		if !ok || dict.Key == nil || dict.Value == nil {
			s.writer.writeString("unknown")
			return
		}
		if k.Type == SD {
			s.writer.writeString("`s#")
		} else {
			s.writer.writeString(attrString(k.Attr))
		}
		s.writeDisplayValue(dict.Key.Data, depth+1)
		s.writer.writeString("!")
		s.writeDisplayValue(dict.Value.Data, depth+1)
	case XT:
		table, ok := k.Data.(Table)
		if !ok {
			s.writer.writeString("unknown")
			return
		}
		if !s.consumeElements(len(table.Data)) {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(attrString(k.Attr))
		s.writer.writeString("+")
		s.writeDisplayValue(table.Columns, depth+1)
		s.writer.writeString("!(")
		for index, column := range table.Data {
			s.writeK(column, depth+1)
			if index != len(table.Data)-1 {
				s.writer.writeString(";")
			}
		}
		s.writer.writeString(")")
	case KFUNC:
		function, ok := k.Data.(Function)
		if !ok {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(function.Body)
	case KFUNCUP:
		primitive, ok := k.Data.(byte)
		if !ok || (primitive != 255 && int(primitive) >= len(unaryops)) {
			s.writer.writeString("unknown")
			return
		}
		if primitive != 255 {
			s.writer.writeString(unaryops[primitive])
		}
	case KFUNCBP:
		primitive, ok := k.Data.(byte)
		if !ok || int(primitive) >= len(binaryops) {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(binaryops[primitive])
	case KFUNCTR:
		primitive, ok := k.Data.(byte)
		if !ok || int(primitive) >= len(ternaryops) {
			s.writer.writeString("unknown")
			return
		}
		s.writer.writeString(ternaryops[primitive])
	case KPROJ, KCOMP:
		list, ok := k.Data.([]*K)
		if !ok {
			s.writer.writeString("unknown")
			return
		}
		if !s.consumeElements(len(list)) {
			s.writer.writeString("unknown")
			return
		}
		for _, item := range list {
			if item != nil {
				s.writeK(item, depth+1)
			}
		}
	case KEACH, KOVER, KSCAN, KPRIOR, KEACHRIGHT, KEACHLEFT:
		item, ok := k.Data.(*K)
		if !ok || item == nil {
			s.writer.writeString("unknown")
			return
		}
		s.writeK(item, depth+1)
		s.writer.writeString(adverbs[k.Type])
	default:
		s.writer.writeString("unknown")
	}
}

// String converts a K value to its legacy display form. Caller-constructed
// cycles and excessive graphs are rendered as bounded diagnostic text.
func (k K) String() string {
	state := kStringState{active: make(map[*K]struct{})}
	state.writeK(&k, 1)
	return state.writer.string()
}

var (
	ErrBadMsg      = errors.New("Bad Message")
	ErrBadHeader   = errors.New("Bad header")
	ErrSyncRequest = errors.New("nosyncrequest")
)

var qEpoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

type Month int32

func (m Month) String() string {
	return fmt.Sprintf("%v.%02vm", 2000+int(m/12), int(m)%12)
}

type Minute time.Time

func (m Minute) String() string {
	value := time.Time(m)
	return fmt.Sprintf("%02v:%02v", value.Hour(), value.Minute())
}

type Second time.Time

func (s Second) String() string {
	value := time.Time(s)
	return fmt.Sprintf("%02v:%02v:%02v", value.Hour(), value.Minute(), value.Second())
}

type Time time.Time

func (t Time) String() string {
	value := time.Time(t)
	return fmt.Sprintf("%02v:%02v:%02v.%03v", value.Hour(), value.Minute(), value.Second(), value.Nanosecond()/1_000_000)
}

type Table struct {
	Columns []string
	Data    []*K
}

func NewTable(cols []string, data []*K) *K {
	return &K{XT, NONE, Table{Columns: cols, Data: data}}
}

func (tbl *Table) Index(i int) Dict {
	if tbl == nil {
		return Dict{}
	}
	result := Dict{Key: &K{KS, NONE, tbl.Columns}}
	values := make([]*K, len(tbl.Columns))
	result.Value = &K{K0, NONE, values}
	if i < 0 || len(tbl.Data) != len(tbl.Columns) {
		return result
	}
	for column := range tbl.Columns {
		if tbl.Data[column] == nil {
			return result
		}
		value := tbl.Data[column].Index(i)
		if value == nil {
			return result
		}
		dataType := tbl.Data[column].Type
		if dataType == K0 {
			item, ok := value.(*K)
			if !ok || item == nil {
				return result
			}
			dataType = item.Type
		} else if dataType > K0 && dataType <= KT {
			dataType = -dataType
		}
		values[column] = &K{dataType, NONE, value}
	}
	return result
}

func (tbl Table) String() string {
	return (K{Type: XT, Attr: NONE, Data: tbl}).String()
}

type Dict struct {
	Key   *K
	Value *K
}

func NewDict(k, v *K) *K { return &K{XD, NONE, Dict{Key: k, Value: v}} }

func (d Dict) String() string {
	return (K{Type: XD, Attr: NONE, Data: d}).String()
}

func titleInitial(str string) string {
	if str == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(str)
	if first == utf8.RuneError && size == 0 {
		return str
	}
	return string(unicode.ToTitle(first)) + str[size:]
}

func dictStrings(t Dict) ([]string, []*K, error) {
	if t.Key == nil || t.Value == nil {
		return nil, nil, errors.New("dictionary key and value are required")
	}
	keys, ok := t.Key.Data.([]string)
	if !ok {
		return nil, nil, errors.New("dictionary keys are not symbols")
	}
	values, ok := t.Value.Data.([]*K)
	if !ok || len(keys) != len(values) {
		return nil, nil, errors.New("dictionary values do not match keys")
	}
	return keys, values, nil
}

func UnmarshalDict(t Dict, v interface{}) error {
	keys, values, err := dictStrings(t)
	if err != nil {
		return err
	}
	target := reflect.ValueOf(v)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return errors.New("Invalid target type. Should be non null pointer")
	}
	target = reflect.Indirect(target)
	if target.Kind() != reflect.Struct {
		return errors.New("Invalid target type. Should point to a struct")
	}
	for i, key := range keys {
		if values[i] == nil {
			continue
		}
		field := target.FieldByName(titleInitial(key))
		value := reflect.ValueOf(values[i].Data)
		if field.IsValid() && field.CanSet() && value.IsValid() && value.Type().AssignableTo(field.Type()) {
			field.Set(value)
		}
	}
	return nil
}

func UnmarshalDictToMap(t Dict, v interface{}) error {
	keys, values, err := dictStrings(t)
	if err != nil {
		return err
	}
	target := reflect.ValueOf(v)
	if target.Kind() != reflect.Map || target.Type().Key().Kind() != reflect.String {
		return errors.New("target type should be map[string]T")
	}
	if target.IsNil() {
		return errors.New("target map must be initialized")
	}
	for i, key := range keys {
		if values[i] == nil {
			continue
		}
		value := reflect.ValueOf(values[i].Data)
		if value.IsValid() && value.Type().AssignableTo(target.Type().Elem()) {
			mapKey := reflect.New(target.Type().Key()).Elem()
			mapKey.SetString(titleInitial(key))
			target.SetMapIndex(mapKey, value)
		}
	}
	return nil
}

func UnmarshalTable(t Table, v interface{}) (interface{}, error) {
	target := reflect.ValueOf(v)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return nil, errors.New("Invalid target type. Should be non null pointer")
	}
	target = reflect.Indirect(target)
	if target.Kind() != reflect.Slice {
		return nil, errors.New("Invalid target type. Should point to a slice")
	}
	if target.Type().Elem().Kind() != reflect.Struct {
		return nil, errors.New("Invalid target type. Slice elements should be structs")
	}
	if len(t.Columns) != len(t.Data) {
		return nil, fmt.Errorf("table has %d column names but %d column values", len(t.Columns), len(t.Data))
	}
	rows := 0
	if len(t.Data) > 0 {
		if t.Data[0] == nil {
			return nil, errors.New("invalid nil table column 0")
		}
		rows = t.Data[0].Len()
		if rows < 0 {
			return nil, errors.New("invalid table column 0")
		}
	}
	for index, column := range t.Data {
		if column == nil {
			return nil, fmt.Errorf("invalid nil table column %d", index)
		}
		if column.Type < K0 || column.Type > KT {
			return nil, fmt.Errorf("invalid table column %d type %d", index, column.Type)
		}
		if length := column.Len(); length < 0 || length != rows {
			return nil, fmt.Errorf("table column %d length %d does not match row count %d", index, length, rows)
		}
	}
	for i := 0; i < rows; i++ {
		element := reflect.New(target.Type().Elem())
		if err := UnmarshalDict(t.Index(i), element.Interface()); err != nil {
			return nil, err
		}
		target.Set(reflect.Append(target, reflect.Indirect(element)))
	}
	return target.Interface(), nil
}

type Function struct {
	Namespace string
	Body      string
}
