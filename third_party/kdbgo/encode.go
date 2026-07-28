package kdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"time"

	uuid "github.com/nu7hatch/gouuid"
)

const defaultMaxEncodeAllocation = uint64(128 << 20)

// EncodeLimits bounds request-derived object graphs before any frame buffer is
// allocated. MaxAllocationBytes includes the raw frame and the worst-case
// temporary compression buffer.
type EncodeLimits struct {
	MaxFrameBytes      uint64
	MaxVectorElements  uint64
	MaxStringBytes     uint64
	MaxDepth           int
	MaxObjects         uint64
	MaxAllocationBytes uint64
}

// DefaultEncodeLimits returns the default outbound IPC limits.
func DefaultEncodeLimits() EncodeLimits {
	return EncodeLimits{
		MaxFrameBytes:      defaultMaxFrame,
		MaxVectorElements:  defaultMaxVector,
		MaxStringBytes:     defaultMaxString,
		MaxDepth:           defaultMaxDepth,
		MaxObjects:         defaultMaxObjects,
		MaxAllocationBytes: defaultMaxEncodeAllocation,
	}
}

func (limits EncodeLimits) normalized() (EncodeLimits, error) {
	defaults := DefaultEncodeLimits()
	if limits.MaxFrameBytes == 0 {
		limits.MaxFrameBytes = defaults.MaxFrameBytes
	}
	if limits.MaxVectorElements == 0 {
		limits.MaxVectorElements = defaults.MaxVectorElements
	}
	if limits.MaxStringBytes == 0 {
		limits.MaxStringBytes = defaults.MaxStringBytes
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxObjects == 0 {
		limits.MaxObjects = defaults.MaxObjects
	}
	if limits.MaxAllocationBytes == 0 {
		limits.MaxAllocationBytes = defaults.MaxAllocationBytes
	}
	if limits.MaxFrameBytes < 9 {
		return EncodeLimits{}, errors.New("maximum encoded frame must be at least 9 bytes")
	}
	if limits.MaxDepth < 1 {
		return EncodeLimits{}, errors.New("maximum encode depth must be positive")
	}
	return limits, nil
}

type encodeSizer struct {
	limits            EncodeLimits
	objects           uint64
	active            map[*K]struct{}
	errorTexts        map[*K]string
	captureErrorTexts bool
}

func encodedIPCType(data *K) (int8, error) {
	if data == nil {
		return 0, errors.New("cannot encode nil K value")
	}
	switch data.Type {
	case XD:
		switch data.Attr {
		case NONE:
			return XD, nil
		case SORTED:
			return SD, nil
		default:
			return 0, fmt.Errorf("dictionary has invalid q attribute %d", data.Attr)
		}
	case SD:
		if data.Attr != NONE && data.Attr != SORTED {
			return 0, fmt.Errorf("sorted dictionary has invalid q attribute %d", data.Attr)
		}
		return SD, nil
	default:
		return data.Type, nil
	}
}

func addSize(current, addition uint64) (uint64, error) {
	if addition > math.MaxUint64-current {
		return 0, errors.New("encoded q IPC size overflow")
	}
	return current + addition, nil
}

func (s *encodeSizer) count(count int, description string) (uint64, error) {
	if count < 0 {
		return 0, fmt.Errorf("%s has a negative length", description)
	}
	value := uint64(count)
	if value > math.MaxInt32 {
		return 0, fmt.Errorf("%s length %d exceeds q IPC int32 count", description, count)
	}
	if value > s.limits.MaxVectorElements {
		return 0, fmt.Errorf("%s length %d exceeds limit %d", description, count, s.limits.MaxVectorElements)
	}
	return value, nil
}

func (s *encodeSizer) stringSize(value, description string, terminated bool) (uint64, error) {
	if uint64(len(value)) > s.limits.MaxStringBytes {
		return 0, fmt.Errorf("%s length %d exceeds limit %d", description, len(value), s.limits.MaxStringBytes)
	}
	if terminated && strings.IndexByte(value, 0) >= 0 {
		return 0, fmt.Errorf("%s contains NUL", description)
	}
	size := uint64(len(value))
	if terminated {
		size++
	}
	return size, nil
}

func (s *encodeSizer) vectorSize(count int, width uint64, description string) (uint64, error) {
	n, err := s.count(count, description)
	if err != nil {
		return 0, err
	}
	if width != 0 && n > math.MaxUint64/width {
		return 0, fmt.Errorf("%s size overflows", description)
	}
	return 4 + n*width, nil
}

func (s *encodeSizer) size(data *K, depth int) (uint64, error) {
	if data == nil {
		return 0, errors.New("cannot encode nil K value")
	}
	if depth > s.limits.MaxDepth {
		return 0, fmt.Errorf("q IPC encode nesting depth exceeds limit %d", s.limits.MaxDepth)
	}
	if s.objects >= s.limits.MaxObjects {
		return 0, fmt.Errorf("q IPC encode object count exceeds limit %d", s.limits.MaxObjects)
	}
	if _, exists := s.active[data]; exists {
		return 0, errors.New("q IPC object graph contains a cycle")
	}
	s.objects++
	s.active[data] = struct{}{}
	defer delete(s.active, data)

	encodedType, err := encodedIPCType(data)
	if err != nil {
		return 0, err
	}
	size := uint64(1)
	if encodedType >= K0 && encodedType < XD {
		if data.Attr < NONE || data.Attr > GROUPED {
			return 0, fmt.Errorf("invalid q attribute %d", data.Attr)
		}
		size++
	}
	add := func(payload uint64, err error) error {
		if err != nil {
			return err
		}
		size, err = addSize(size, payload)
		return err
	}

	switch data.Type {
	case K0:
		values, ok := data.Data.([]*K)
		if !ok {
			return 0, errors.New("generic list requires []*K")
		}
		count, err := s.count(len(values), "generic list")
		if err := add(4, err); err != nil {
			return 0, err
		}
		if count > s.limits.MaxObjects-s.objects {
			return 0, fmt.Errorf("generic list needs %d objects but only %d remain", count, s.limits.MaxObjects-s.objects)
		}
		for i, value := range values {
			itemSize, err := s.size(value, depth+1)
			if err != nil {
				return 0, fmt.Errorf("generic list item %d: %w", i, err)
			}
			if err := add(itemSize, nil); err != nil {
				return 0, err
			}
		}
	case -KB:
		if _, ok := data.Data.(bool); !ok {
			return 0, errors.New("boolean atom requires bool")
		}
		if err := add(1, nil); err != nil {
			return 0, err
		}
	case -UU:
		if _, ok := data.Data.(uuid.UUID); !ok {
			return 0, errors.New("guid atom requires uuid.UUID")
		}
		if err := add(16, nil); err != nil {
			return 0, err
		}
	case -KG, -KC:
		if _, ok := data.Data.(byte); !ok {
			return 0, errors.New("byte/char atom requires byte")
		}
		if err := add(1, nil); err != nil {
			return 0, err
		}
	case -KH:
		if _, ok := data.Data.(int16); !ok {
			return 0, errors.New("short atom requires int16")
		}
		if err := add(2, nil); err != nil {
			return 0, err
		}
	case -KI:
		if _, ok := data.Data.(int32); !ok {
			return 0, errors.New("int atom requires int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KJ:
		if _, ok := data.Data.(int64); !ok {
			return 0, errors.New("long atom requires int64")
		}
		if err := add(8, nil); err != nil {
			return 0, err
		}
	case -KE:
		if _, ok := data.Data.(float32); !ok {
			return 0, errors.New("real atom requires float32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KF:
		if _, ok := data.Data.(float64); !ok {
			return 0, errors.New("float atom requires float64")
		}
		if err := add(8, nil); err != nil {
			return 0, err
		}
	case -KS:
		value, ok := data.Data.(string)
		if !ok {
			return 0, errors.New("symbol atom requires string")
		}
		if err := add(s.stringSize(value, "symbol atom", true)); err != nil {
			return 0, err
		}
	case -KP:
		if _, ok := data.Data.(time.Time); !ok {
			return 0, errors.New("timestamp atom requires time.Time")
		}
		if err := add(8, nil); err != nil {
			return 0, err
		}
	case -KM:
		switch data.Data.(type) {
		case Month, int32:
		default:
			return 0, errors.New("month atom requires Month or int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KD:
		switch data.Data.(type) {
		case time.Time, int32:
		default:
			return 0, errors.New("date atom requires time.Time or int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KZ:
		switch data.Data.(type) {
		case time.Time, float64:
		default:
			return 0, errors.New("datetime atom requires time.Time or float64")
		}
		if err := add(8, nil); err != nil {
			return 0, err
		}
	case -KN:
		if _, ok := data.Data.(time.Duration); !ok {
			return 0, errors.New("timespan atom requires time.Duration")
		}
		if err := add(8, nil); err != nil {
			return 0, err
		}
	case -KU:
		switch data.Data.(type) {
		case Minute, int32:
		default:
			return 0, errors.New("minute atom requires Minute or int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KV:
		switch data.Data.(type) {
		case Second, int32:
		default:
			return 0, errors.New("second atom requires Second or int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case -KT:
		switch data.Data.(type) {
		case Time, int32:
		default:
			return 0, errors.New("time atom requires Time or int32")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
	case KB:
		values, ok := data.Data.([]bool)
		if !ok {
			return 0, errors.New("boolean vector requires []bool")
		}
		if err := add(s.vectorSize(len(values), 1, "boolean vector")); err != nil {
			return 0, err
		}
	case UU:
		values, ok := data.Data.([]uuid.UUID)
		if !ok {
			return 0, errors.New("guid vector requires []uuid.UUID")
		}
		if err := add(s.vectorSize(len(values), 16, "guid vector")); err != nil {
			return 0, err
		}
	case KG:
		values, ok := data.Data.([]byte)
		if !ok {
			return 0, errors.New("byte vector requires []byte")
		}
		if err := add(s.vectorSize(len(values), 1, "byte vector")); err != nil {
			return 0, err
		}
	case KC:
		value, ok := data.Data.(string)
		if !ok {
			return 0, errors.New("char vector requires string")
		}
		if _, err := s.stringSize(value, "char vector", false); err != nil {
			return 0, err
		}
		if err := add(s.vectorSize(len(value), 1, "char vector")); err != nil {
			return 0, err
		}
	case KS:
		values, ok := data.Data.([]string)
		if !ok {
			return 0, errors.New("symbol vector requires []string")
		}
		if _, err := s.count(len(values), "symbol vector"); err != nil {
			return 0, err
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
		for i, value := range values {
			if err := add(s.stringSize(value, fmt.Sprintf("symbol vector item %d", i), true)); err != nil {
				return 0, err
			}
		}
	case KH:
		values, ok := data.Data.([]int16)
		if !ok {
			return 0, errors.New("short vector requires []int16")
		}
		if err := add(s.vectorSize(len(values), 2, "short vector")); err != nil {
			return 0, err
		}
	case KI:
		values, ok := data.Data.([]int32)
		if !ok {
			return 0, errors.New("int vector requires []int32")
		}
		if err := add(s.vectorSize(len(values), 4, "int vector")); err != nil {
			return 0, err
		}
	case KJ:
		values, ok := data.Data.([]int64)
		if !ok {
			return 0, errors.New("long vector requires []int64")
		}
		if err := add(s.vectorSize(len(values), 8, "long vector")); err != nil {
			return 0, err
		}
	case KE:
		values, ok := data.Data.([]float32)
		if !ok {
			return 0, errors.New("real vector requires []float32")
		}
		if err := add(s.vectorSize(len(values), 4, "real vector")); err != nil {
			return 0, err
		}
	case KF:
		values, ok := data.Data.([]float64)
		if !ok {
			return 0, errors.New("float vector requires []float64")
		}
		if err := add(s.vectorSize(len(values), 8, "float vector")); err != nil {
			return 0, err
		}
	case KP:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return 0, errors.New("timestamp vector requires []time.Time")
		}
		if err := add(s.vectorSize(len(values), 8, "timestamp vector")); err != nil {
			return 0, err
		}
	case KM:
		values, ok := data.Data.([]Month)
		if !ok {
			return 0, errors.New("month vector requires []Month")
		}
		if err := add(s.vectorSize(len(values), 4, "month vector")); err != nil {
			return 0, err
		}
	case KD:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return 0, errors.New("date vector requires []time.Time")
		}
		if err := add(s.vectorSize(len(values), 4, "date vector")); err != nil {
			return 0, err
		}
	case KZ:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return 0, errors.New("datetime vector requires []time.Time")
		}
		if err := add(s.vectorSize(len(values), 8, "datetime vector")); err != nil {
			return 0, err
		}
	case KN:
		values, ok := data.Data.([]time.Duration)
		if !ok {
			return 0, errors.New("timespan vector requires []time.Duration")
		}
		if err := add(s.vectorSize(len(values), 8, "timespan vector")); err != nil {
			return 0, err
		}
	case KU:
		var count int
		switch values := data.Data.(type) {
		case []Minute:
			count = len(values)
		case []int32:
			count = len(values)
		default:
			return 0, errors.New("minute vector requires []Minute or []int32")
		}
		if err := add(s.vectorSize(count, 4, "minute vector")); err != nil {
			return 0, err
		}
	case KV:
		var count int
		switch values := data.Data.(type) {
		case []Second:
			count = len(values)
		case []int32:
			count = len(values)
		default:
			return 0, errors.New("second vector requires []Second or []int32")
		}
		if err := add(s.vectorSize(count, 4, "second vector")); err != nil {
			return 0, err
		}
	case KT:
		var count int
		switch values := data.Data.(type) {
		case []Time:
			count = len(values)
		case []int32:
			count = len(values)
		default:
			return 0, errors.New("time vector requires []Time or []int32")
		}
		if err := add(s.vectorSize(count, 4, "time vector")); err != nil {
			return 0, err
		}
	case XD, SD:
		dict, ok := data.Data.(Dict)
		if !ok || dict.Key == nil || dict.Value == nil {
			return 0, errors.New("dictionary requires key and value")
		}
		keyLength, keyOK := logicalLength(dict.Key)
		valueLength, valueOK := logicalLength(dict.Value)
		if !keyOK || !valueOK || keyLength != valueLength {
			return 0, fmt.Errorf("dictionary key/value lengths differ (%d/%d)", keyLength, valueLength)
		}
		keySize, err := s.size(dict.Key, depth+1)
		if err != nil {
			return 0, fmt.Errorf("dictionary key: %w", err)
		}
		if err := add(keySize, nil); err != nil {
			return 0, err
		}
		valueSize, err := s.size(dict.Value, depth+1)
		if err != nil {
			return 0, fmt.Errorf("dictionary value: %w", err)
		}
		if err := add(valueSize, nil); err != nil {
			return 0, err
		}
	case XT:
		table, ok := data.Data.(Table)
		if !ok {
			return 0, errors.New("table requires Table")
		}
		if len(table.Columns) != len(table.Data) {
			return 0, errors.New("table column name/value counts differ")
		}
		rows := -1
		for i, column := range table.Data {
			if column == nil || column.Type < K0 || column.Type > KT {
				return 0, fmt.Errorf("table column %d is not a q list", i)
			}
			length, ok := logicalLength(column)
			if !ok {
				return 0, fmt.Errorf("table column %d has invalid shape", i)
			}
			if rows < 0 {
				rows = length
			} else if rows != length {
				return 0, fmt.Errorf("table column %d has %d rows; expected %d", i, length, rows)
			}
		}
		payload := NewDict(SymbolV(table.Columns), NewList(table.Data...))
		payloadSize, err := s.size(payload, depth+1)
		if err != nil {
			return 0, fmt.Errorf("table payload: %w", err)
		}
		if err := add(payloadSize, nil); err != nil {
			return 0, err
		}
	case KERR:
		value, ok := data.Data.(error)
		if !ok || value == nil {
			return 0, errors.New("q error requires error")
		}
		text, cached := s.errorTexts[data]
		if !cached {
			if !s.captureErrorTexts {
				return 0, errors.New("q error object was introduced after callback preflight")
			}
			var err error
			text, err = safeErrorText(value)
			if err != nil {
				return 0, fmt.Errorf("q error: %w", err)
			}
			s.errorTexts[data] = text
		}
		if err := add(s.stringSize(text, "q error", true)); err != nil {
			return 0, err
		}
	case KFUNC:
		value, ok := data.Data.(Function)
		if !ok {
			return 0, errors.New("q function requires Function")
		}
		if err := add(s.stringSize(value.Namespace, "function namespace", true)); err != nil {
			return 0, err
		}
		body := &K{KC, NONE, value.Body}
		bodySize, err := s.size(body, depth+1)
		if err != nil {
			return 0, fmt.Errorf("function body: %w", err)
		}
		if err := add(bodySize, nil); err != nil {
			return 0, err
		}
	case KFUNCUP, KFUNCBP, KFUNCTR:
		index, ok := data.Data.(byte)
		if !ok {
			return 0, errors.New("q primitive requires byte index")
		}
		valid := data.Type == KFUNCUP && (int(index) < len(unaryops) || index == 255)
		valid = valid || data.Type == KFUNCBP && int(index) < len(binaryops)
		valid = valid || data.Type == KFUNCTR && int(index) < len(ternaryops)
		if !valid {
			return 0, fmt.Errorf("invalid primitive index %d for q type %d", index, data.Type)
		}
		if err := add(1, nil); err != nil {
			return 0, err
		}
	case KPROJ, KCOMP:
		values, ok := data.Data.([]*K)
		if !ok {
			return 0, errors.New("projection/composition requires []*K")
		}
		count, err := s.count(len(values), "projection/composition")
		if err != nil {
			return 0, err
		}
		if count > s.limits.MaxObjects-s.objects {
			return 0, errors.New("projection/composition exceeds remaining object budget")
		}
		if err := add(4, nil); err != nil {
			return 0, err
		}
		for i, value := range values {
			itemSize, err := s.size(value, depth+1)
			if err != nil {
				return 0, fmt.Errorf("projection/composition item %d: %w", i, err)
			}
			if err := add(itemSize, nil); err != nil {
				return 0, err
			}
		}
	case KEACH, KOVER, KSCAN, KPRIOR, KEACHRIGHT, KEACHLEFT:
		value, ok := data.Data.(*K)
		if !ok || value == nil {
			return 0, errors.New("q iterator requires K value")
		}
		valueSize, err := s.size(value, depth+1)
		if err != nil {
			return 0, err
		}
		if err := add(valueSize, nil); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unknown q type %d", data.Type)
	}
	return size, nil
}

func writeBinary(w io.Writer, order binary.ByteOrder, value interface{}) error {
	if err := binary.Write(w, order, value); err != nil {
		return err
	}
	return nil
}

func writeCString(w io.Writer, value string) error {
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("q zero-terminated string contains NUL")
	}
	if _, err := io.WriteString(w, value); err != nil {
		return err
	}
	return writeBinary(w, binary.LittleEndian, byte(0))
}

type encodeWriteState struct {
	limits     EncodeLimits
	objects    uint64
	active     map[*K]struct{}
	errorTexts map[*K]string
}

func (s *encodeWriteState) writeData(w io.Writer, order binary.ByteOrder, data *K, depth int) error {
	if data == nil {
		return errors.New("cannot encode nil K value")
	}
	if depth > s.limits.MaxDepth {
		return fmt.Errorf("q IPC serialization nesting depth exceeds limit %d", s.limits.MaxDepth)
	}
	if s.objects >= s.limits.MaxObjects {
		return fmt.Errorf("q IPC serialization object count exceeds limit %d", s.limits.MaxObjects)
	}
	if _, exists := s.active[data]; exists {
		return errors.New("q IPC object graph mutated to contain a cycle after preflight")
	}
	s.objects++
	s.active[data] = struct{}{}
	defer delete(s.active, data)

	encodedType, err := encodedIPCType(data)
	if err != nil {
		return err
	}
	if err := writeBinary(w, order, encodedType); err != nil {
		return err
	}
	if encodedType >= K0 && encodedType < XD {
		if data.Attr < NONE || data.Attr > GROUPED {
			return fmt.Errorf("invalid q attribute %d", data.Attr)
		}
		if err := writeBinary(w, order, data.Attr); err != nil {
			return err
		}
	}

	writeCount := func(value interface{}) error {
		v := reflect.ValueOf(value)
		if !v.IsValid() || (v.Kind() != reflect.Array && v.Kind() != reflect.Slice && v.Kind() != reflect.String) {
			return fmt.Errorf("q type %d requires a list value", data.Type)
		}
		if uint64(v.Len()) > math.MaxInt32 {
			return fmt.Errorf("q list length %d exceeds int32", v.Len())
		}
		return writeBinary(w, order, int32(v.Len()))
	}

	switch data.Type {
	case K0:
		values, ok := data.Data.([]*K)
		if !ok {
			return errors.New("generic list requires []*K")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			if err := s.writeData(w, order, value, depth+1); err != nil {
				return err
			}
		}
	case -KS:
		value, ok := data.Data.(string)
		if !ok {
			return errors.New("symbol atom requires string")
		}
		return writeCString(w, value)
	case KC:
		value, ok := data.Data.(string)
		if !ok {
			return errors.New("char vector requires string")
		}
		if err := writeCount(value); err != nil {
			return err
		}
		_, err := io.WriteString(w, value)
		return err
	case KS:
		values, ok := data.Data.([]string)
		if !ok {
			return errors.New("symbol vector requires []string")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			if err := writeCString(w, value); err != nil {
				return err
			}
		}
	case -KB:
		value, ok := data.Data.(bool)
		if !ok {
			return errors.New("boolean atom requires bool")
		}
		if value {
			return writeBinary(w, order, byte(1))
		}
		return writeBinary(w, order, byte(0))
	case -KG, -KC:
		value, ok := data.Data.(byte)
		if !ok {
			return fmt.Errorf("q byte/char atom requires byte")
		}
		return writeBinary(w, order, value)
	case -KH:
		value, ok := data.Data.(int16)
		if !ok {
			return errors.New("short atom requires int16")
		}
		return writeBinary(w, order, value)
	case -KI:
		value, ok := data.Data.(int32)
		if !ok {
			return errors.New("int atom requires int32")
		}
		return writeBinary(w, order, value)
	case -KJ:
		value, ok := data.Data.(int64)
		if !ok {
			return errors.New("long atom requires int64")
		}
		return writeBinary(w, order, value)
	case -KE:
		value, ok := data.Data.(float32)
		if !ok {
			return errors.New("real atom requires float32")
		}
		return writeBinary(w, order, value)
	case -KF:
		value, ok := data.Data.(float64)
		if !ok {
			return errors.New("float atom requires float64")
		}
		return writeBinary(w, order, value)
	case -UU:
		return writeBinary(w, order, data.Data)
	case -KP:
		value, ok := data.Data.(time.Time)
		if !ok {
			return errors.New("timestamp atom requires time.Time")
		}
		return writeBinary(w, order, value.Sub(qEpoch))
	case -KM:
		switch value := data.Data.(type) {
		case Month:
			return writeBinary(w, order, value)
		case int32:
			return writeBinary(w, order, value)
		default:
			return errors.New("month atom requires Month or int32")
		}
	case -KD:
		switch value := data.Data.(type) {
		case time.Time:
			return writeBinary(w, order, int32(value.Sub(qEpoch)/(24*time.Hour)))
		case int32:
			return writeBinary(w, order, value)
		default:
			return errors.New("date atom requires time.Time or int32")
		}
	case -KZ:
		switch value := data.Data.(type) {
		case time.Time:
			return writeBinary(w, order, float64(value.Sub(qEpoch))/float64(24*time.Hour))
		case float64:
			return writeBinary(w, order, value)
		default:
			return errors.New("datetime atom requires time.Time or float64")
		}
	case -KN:
		value, ok := data.Data.(time.Duration)
		if !ok {
			return errors.New("timespan atom requires time.Duration")
		}
		return writeBinary(w, order, value)
	case -KU:
		switch value := data.Data.(type) {
		case Minute:
			t := time.Time(value)
			return writeBinary(w, order, int32(t.Hour()*60+t.Minute()))
		case int32:
			return writeBinary(w, order, value)
		default:
			return errors.New("minute atom requires Minute or int32")
		}
	case -KV:
		switch value := data.Data.(type) {
		case Second:
			t := time.Time(value)
			return writeBinary(w, order, int32(t.Hour()*3600+t.Minute()*60+t.Second()))
		case int32:
			return writeBinary(w, order, value)
		default:
			return errors.New("second atom requires Second or int32")
		}
	case -KT:
		switch value := data.Data.(type) {
		case Time:
			t := time.Time(value)
			millis := ((t.Hour()*60+t.Minute())*60+t.Second())*1000 + t.Nanosecond()/1_000_000
			return writeBinary(w, order, int32(millis))
		case int32:
			return writeBinary(w, order, value)
		default:
			return errors.New("time atom requires Time or int32")
		}
	case KP:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return errors.New("timestamp vector requires []time.Time")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			if err := writeBinary(w, order, value.Sub(qEpoch)); err != nil {
				return err
			}
		}
	case KD:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return errors.New("date vector requires []time.Time")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			if err := writeBinary(w, order, int32(value.Sub(qEpoch)/(24*time.Hour))); err != nil {
				return err
			}
		}
	case KZ:
		values, ok := data.Data.([]time.Time)
		if !ok {
			return errors.New("datetime vector requires []time.Time")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			days := float64(value.Sub(qEpoch)) / float64(24*time.Hour)
			if err := writeBinary(w, order, days); err != nil {
				return err
			}
		}
	case KU:
		switch values := data.Data.(type) {
		case []Minute:
			if err := writeCount(values); err != nil {
				return err
			}
			for _, value := range values {
				t := time.Time(value)
				if err := writeBinary(w, order, int32(t.Hour()*60+t.Minute())); err != nil {
					return err
				}
			}
			return nil
		case []int32:
			if err := writeCount(values); err != nil {
				return err
			}
			return writeBinary(w, order, values)
		default:
			return errors.New("minute vector requires []Minute or []int32")
		}
	case KV:
		switch values := data.Data.(type) {
		case []Second:
			if err := writeCount(values); err != nil {
				return err
			}
			for _, value := range values {
				t := time.Time(value)
				if err := writeBinary(w, order, int32(t.Hour()*3600+t.Minute()*60+t.Second())); err != nil {
					return err
				}
			}
			return nil
		case []int32:
			if err := writeCount(values); err != nil {
				return err
			}
			return writeBinary(w, order, values)
		default:
			return errors.New("second vector requires []Second or []int32")
		}
	case KT:
		switch values := data.Data.(type) {
		case []Time:
			if err := writeCount(values); err != nil {
				return err
			}
			for _, value := range values {
				t := time.Time(value)
				millis := ((t.Hour()*60+t.Minute())*60+t.Second())*1000 + t.Nanosecond()/1_000_000
				if err := writeBinary(w, order, int32(millis)); err != nil {
					return err
				}
			}
			return nil
		case []int32:
			if err := writeCount(values); err != nil {
				return err
			}
			return writeBinary(w, order, values)
		default:
			return errors.New("time vector requires []Time or []int32")
		}
	case KB:
		values, ok := data.Data.([]bool)
		if !ok {
			return errors.New("boolean vector requires []bool")
		}
		if err := writeCount(values); err != nil {
			return err
		}
		for _, value := range values {
			var encoded byte
			if value {
				encoded = 1
			}
			if err := writeBinary(w, order, encoded); err != nil {
				return err
			}
		}
	case KG, KH, KI, KJ, KE, KF, KM, KN, UU:
		if err := writeCount(data.Data); err != nil {
			return err
		}
		return writeBinary(w, order, data.Data)
	case XD, SD:
		dict, ok := data.Data.(Dict)
		if !ok || dict.Key == nil || dict.Value == nil {
			return errors.New("dictionary requires key and value")
		}
		if err := s.writeData(w, order, dict.Key, depth+1); err != nil {
			return err
		}
		return s.writeData(w, order, dict.Value, depth+1)
	case XT:
		table, ok := data.Data.(Table)
		if !ok {
			return errors.New("table requires Table")
		}
		return s.writeData(w, order, NewDict(SymbolV(table.Columns), &K{K0, NONE, table.Data}), depth+1)
	case KERR:
		text, ok := s.errorTexts[data]
		if !ok {
			return errors.New("q error text missing from encode preflight")
		}
		return writeCString(w, text)
	case KFUNC:
		value, ok := data.Data.(Function)
		if !ok {
			return errors.New("q function requires Function")
		}
		if err := writeCString(w, value.Namespace); err != nil {
			return err
		}
		return s.writeData(w, order, &K{KC, NONE, value.Body}, depth+1)
	case KPROJ, KCOMP:
		values, ok := data.Data.([]*K)
		if !ok {
			return errors.New("projection/composition requires []*K")
		}
		if err := writeBinary(w, order, int32(len(values))); err != nil {
			return err
		}
		for _, value := range values {
			if err := s.writeData(w, order, value, depth+1); err != nil {
				return err
			}
		}
	case KEACH, KOVER, KSCAN, KPRIOR, KEACHRIGHT, KEACHLEFT:
		value, ok := data.Data.(*K)
		if !ok || value == nil {
			return errors.New("q iterator requires K value")
		}
		return s.writeData(w, order, value, depth+1)
	case KFUNCUP, KFUNCBP, KFUNCTR:
		value, ok := data.Data.(byte)
		if !ok {
			return errors.New("q primitive requires byte index")
		}
		return writeBinary(w, order, value)
	default:
		return fmt.Errorf("unknown q type %d", data.Type)
	}
	return nil
}

type exactFrameWriter struct {
	frame  []byte
	offset int
}

func (w *exactFrameWriter) Write(value []byte) (int, error) {
	if len(value) > len(w.frame)-w.offset {
		return 0, fmt.Errorf("q IPC serialization exceeds preflight size by at least %d bytes", len(value)-(len(w.frame)-w.offset))
	}
	copy(w.frame[w.offset:], value)
	w.offset += len(value)
	return len(value), nil
}

func (w *exactFrameWriter) WriteString(value string) (int, error) {
	if len(value) > len(w.frame)-w.offset {
		return 0, fmt.Errorf("q IPC serialization exceeds preflight size by at least %d bytes", len(value)-(len(w.frame)-w.offset))
	}
	copy(w.frame[w.offset:], value)
	w.offset += len(value)
	return len(value), nil
}

// Encode serializes data as a little-endian q IPC frame using the default
// outbound limits.
func Encode(w io.Writer, msgtype ReqType, data *K) error {
	return EncodeWithLimits(w, msgtype, data, DefaultEncodeLimits())
}

// EncodeWithLimits validates and sizes the complete object graph before
// allocating or writing a frame.
func EncodeWithLimits(w io.Writer, msgtype ReqType, data *K, requested EncodeLimits) error {
	if w == nil {
		return errors.New("cannot encode q IPC to a nil writer")
	}
	if msgtype < ASYNC || msgtype > RESPONSE {
		return fmt.Errorf("invalid q IPC request type %d", msgtype)
	}
	limits, err := requested.normalized()
	if err != nil {
		return fmt.Errorf("encode q IPC limits: %w", err)
	}
	sizer := encodeSizer{
		limits:            limits,
		active:            make(map[*K]struct{}),
		errorTexts:        make(map[*K]string),
		captureErrorTexts: true,
	}
	initialObjectSize, err := sizer.size(data, 1)
	if err != nil {
		return fmt.Errorf("encode q IPC object: %w", err)
	}
	validationSizer := encodeSizer{
		limits:     limits,
		active:     make(map[*K]struct{}),
		errorTexts: sizer.errorTexts,
	}
	objectSize, err := validationSizer.size(data, 1)
	if err != nil {
		return fmt.Errorf("encode q IPC object after error callbacks: %w", err)
	}
	if objectSize != initialObjectSize {
		return fmt.Errorf(
			"encode q IPC object changed size after error callbacks: before %d, after %d",
			initialObjectSize,
			objectSize,
		)
	}
	frameSize, err := addSize(8, objectSize)
	if err != nil {
		return err
	}
	if frameSize > limits.MaxFrameBytes {
		return fmt.Errorf("encoded q IPC frame size %d exceeds limit %d", frameSize, limits.MaxFrameBytes)
	}
	if frameSize > math.MaxUint32 {
		return fmt.Errorf("q IPC frame size %d exceeds uint32", frameSize)
	}
	maxInt := uint64(^uint(0) >> 1)
	if frameSize > maxInt {
		return fmt.Errorf("q IPC frame size %d does not fit in memory", frameSize)
	}
	peakAllocation := frameSize
	if frameSize > 17 {
		peakAllocation, err = addSize(peakAllocation, frameSize)
		if err != nil {
			return err
		}
	}
	if peakAllocation > limits.MaxAllocationBytes {
		return fmt.Errorf("q IPC encode allocation %d exceeds limit %d", peakAllocation, limits.MaxAllocationBytes)
	}

	frame := make([]byte, int(frameSize))
	buf := exactFrameWriter{frame: frame, offset: 8}
	writerState := encodeWriteState{
		limits:     limits,
		active:     make(map[*K]struct{}),
		errorTexts: sizer.errorTexts,
	}
	if err := writerState.writeData(&buf, binary.LittleEndian, data, 1); err != nil {
		return fmt.Errorf("encode q IPC object after preflight: %w", err)
	}
	if uint64(buf.offset) != frameSize {
		return fmt.Errorf("q IPC internal size mismatch: preflight %d, encoded %d", frameSize, buf.offset)
	}
	frame[0] = 1
	frame[1] = byte(msgtype)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(frame)))
	frame = Compress(frame)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if n < 0 || n > len(frame) {
			return fmt.Errorf("%w: writer returned invalid count %d for %d bytes", io.ErrShortWrite, n, len(frame))
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}
