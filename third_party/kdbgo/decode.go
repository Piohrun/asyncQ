package kdb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	uuid "github.com/nu7hatch/gouuid"
)

const (
	defaultMaxFrame       = uint64(64 << 20)
	defaultMaxVector      = uint64(16 << 20)
	defaultMaxString      = uint64(1 << 20)
	defaultMaxObjects     = uint64(1 << 20)
	defaultMaxDepth       = 64
	decodedObjectOverhead = uint64(32)
)

// DecodeLimits bounds all peer-controlled frame and object dimensions.
// MaxAllocationBytes accounts for decoded value storage, independently of the
// bounded wire/expanded frame buffers.
type DecodeLimits struct {
	MaxWireFrame         uint64
	MaxUncompressedFrame uint64
	MaxVectorElements    uint64
	MaxStringBytes       uint64
	MaxDepth             int
	MaxObjects           uint64
	MaxAllocationBytes   uint64
}

// DefaultDecodeLimits returns conservative limits suitable for network peers.
func DefaultDecodeLimits() DecodeLimits {
	return DecodeLimits{
		MaxWireFrame:         defaultMaxFrame,
		MaxUncompressedFrame: defaultMaxFrame,
		MaxVectorElements:    defaultMaxVector,
		MaxStringBytes:       defaultMaxString,
		MaxDepth:             defaultMaxDepth,
		MaxObjects:           defaultMaxObjects,
		MaxAllocationBytes:   defaultMaxFrame,
	}
}

func (limits DecodeLimits) normalized() (DecodeLimits, error) {
	defaults := DefaultDecodeLimits()
	if limits.MaxWireFrame == 0 {
		limits.MaxWireFrame = defaults.MaxWireFrame
	}
	if limits.MaxUncompressedFrame == 0 {
		limits.MaxUncompressedFrame = defaults.MaxUncompressedFrame
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
	if limits.MaxWireFrame < 9 {
		return DecodeLimits{}, errors.New("maximum wire frame must be at least 9 bytes")
	}
	if limits.MaxUncompressedFrame < 9 {
		return DecodeLimits{}, errors.New("maximum uncompressed frame must be at least 9 bytes")
	}
	if limits.MaxDepth < 1 {
		return DecodeLimits{}, errors.New("maximum decode depth must be positive")
	}
	return limits, nil
}

// Decode deserializes one q IPC frame with DefaultDecodeLimits.
func Decode(src *bufio.Reader) (*K, ReqType, error) {
	return DecodeWithLimits(src, DefaultDecodeLimits())
}

// DecodeWithLimits deserializes exactly one declared q IPC frame. It never
// reads object bytes from a following frame.
func DecodeWithLimits(src *bufio.Reader, requested DecodeLimits) (*K, ReqType, error) {
	if src == nil {
		return nil, -1, errors.New("decode q IPC: nil reader")
	}
	limits, err := requested.normalized()
	if err != nil {
		return nil, -1, fmt.Errorf("decode q IPC limits: %w", err)
	}

	var header [8]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return nil, -1, fmt.Errorf("decode q IPC header: %w", err)
	}
	order, ok := byteOrder(header[0])
	if !ok {
		return nil, -1, fmt.Errorf("%w: invalid byte-order marker %d", ErrBadHeader, header[0])
	}
	request := ReqType(int8(header[1]))
	if request < ASYNC || request > RESPONSE {
		return nil, -1, fmt.Errorf("%w: invalid request type %d", ErrBadHeader, header[1])
	}
	if header[2] > 1 {
		return nil, request, fmt.Errorf("%w: invalid compression marker %d", ErrBadHeader, header[2])
	}
	if header[3] != 0 {
		return nil, request, fmt.Errorf("%w: reserved header byte is %d", ErrBadHeader, header[3])
	}
	frameSize := uint64(order.Uint32(header[4:8]))
	if frameSize < 9 {
		return nil, request, fmt.Errorf("%w: declared frame size %d is smaller than 9", ErrBadHeader, frameSize)
	}
	if frameSize > limits.MaxWireFrame {
		return nil, request, fmt.Errorf("%w: declared wire frame size %d exceeds limit %d", ErrBadHeader, frameSize, limits.MaxWireFrame)
	}
	bodySize := frameSize - 8
	maxInt := uint64(^uint(0) >> 1)
	if bodySize > maxInt {
		return nil, request, fmt.Errorf("%w: declared frame body %d does not fit in memory", ErrBadHeader, bodySize)
	}
	body := make([]byte, int(bodySize))
	if _, err := io.ReadFull(src, body); err != nil {
		return nil, request, fmt.Errorf("decode q IPC frame body (%d bytes): %w", bodySize, err)
	}

	if header[2] == 1 {
		expanded, err := uncompressBounded(body, limits.MaxUncompressedFrame, order)
		if err != nil {
			return nil, request, fmt.Errorf("decode compressed q IPC frame: %w", err)
		}
		body = expanded[8:]
	} else if frameSize > limits.MaxUncompressedFrame {
		return nil, request, fmt.Errorf("q IPC frame size %d exceeds uncompressed limit %d", frameSize, limits.MaxUncompressedFrame)
	}

	decoder := objectDecoder{data: body, order: order, limits: limits}
	value, err := decoder.decodeObject(1)
	var remote remoteError
	if errors.As(err, &remote) {
		return nil, request, errors.New(remote.message)
	}
	if err != nil {
		return nil, request, err
	}
	if decoder.remaining() != 0 {
		return nil, request, fmt.Errorf("%w: q IPC frame has %d trailing bytes after the root object", ErrBadMsg, decoder.remaining())
	}
	return value, request, nil
}

type remoteError struct {
	message string
}

func (e remoteError) Error() string { return e.message }

type objectDecoder struct {
	data       []byte
	offset     int
	order      binary.ByteOrder
	limits     DecodeLimits
	objects    uint64
	allocBytes uint64
}

func (d *objectDecoder) remaining() int {
	return len(d.data) - d.offset
}

func (d *objectDecoder) read(size int, description string) ([]byte, error) {
	if size < 0 || size > d.remaining() {
		return nil, fmt.Errorf("%w: truncated %s at offset %d: need %d bytes, have %d", ErrBadMsg, description, d.offset, size, d.remaining())
	}
	value := d.data[d.offset : d.offset+size]
	d.offset += size
	return value, nil
}

func (d *objectDecoder) readByte(description string) (byte, error) {
	value, err := d.read(1, description)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (d *objectDecoder) readUint32(description string) (uint32, error) {
	value, err := d.read(4, description)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(value), nil
}

func (d *objectDecoder) reserve(size uint64, description string) error {
	if size > d.limits.MaxAllocationBytes-d.allocBytes {
		return fmt.Errorf("%w: decoded %s allocation would exceed limit %d", ErrBadMsg, description, d.limits.MaxAllocationBytes)
	}
	d.allocBytes += size
	return nil
}

func (d *objectDecoder) readCString(description string) (string, error) {
	remaining := d.data[d.offset:]
	scan := uint64(len(remaining))
	if scan > d.limits.MaxStringBytes {
		scan = d.limits.MaxStringBytes + 1
	}
	index := bytes.IndexByte(remaining[:int(scan)], 0)
	if index < 0 {
		if uint64(len(remaining)) > d.limits.MaxStringBytes {
			return "", fmt.Errorf("%w: %s exceeds %d bytes or is not terminated", ErrBadMsg, description, d.limits.MaxStringBytes)
		}
		return "", fmt.Errorf("%w: unterminated %s at offset %d", ErrBadMsg, description, d.offset)
	}
	if err := d.reserve(uint64(index), description); err != nil {
		return "", err
	}
	value := string(remaining[:index])
	d.offset += index + 1
	return value, nil
}

func (d *objectDecoder) readAttrCount(typeName string) (Attr, uint64, error) {
	rawAttr, err := d.readByte(typeName + " attribute")
	if err != nil {
		return NONE, 0, err
	}
	attr := Attr(int8(rawAttr))
	if attr < NONE || attr > GROUPED {
		return NONE, 0, fmt.Errorf("%w: invalid %s attribute %d", ErrBadMsg, typeName, rawAttr)
	}
	rawCount, err := d.readUint32(typeName + " count")
	if err != nil {
		return NONE, 0, err
	}
	if rawCount > math.MaxInt32 {
		return NONE, 0, fmt.Errorf("%w: %s count %d exceeds q IPC int32 count", ErrBadMsg, typeName, rawCount)
	}
	count := uint64(rawCount)
	if count > d.limits.MaxVectorElements {
		return NONE, 0, fmt.Errorf("%w: %s count %d exceeds limit %d", ErrBadMsg, typeName, count, d.limits.MaxVectorElements)
	}
	if count > uint64(^uint(0)>>1) {
		return NONE, 0, fmt.Errorf("%w: %s count %d does not fit in memory", ErrBadMsg, typeName, count)
	}
	return attr, count, nil
}

func (d *objectDecoder) fixedBytes(count uint64, width uint64, description string) ([]byte, error) {
	if width == 0 || count > uint64(d.remaining())/width {
		return nil, fmt.Errorf("%w: %s count %d needs %d bytes each but only %d bytes remain", ErrBadMsg, description, count, width, d.remaining())
	}
	total := count * width
	return d.read(int(total), description)
}

func (d *objectDecoder) decodeObject(depth int) (*K, error) {
	if depth > d.limits.MaxDepth {
		return nil, fmt.Errorf("%w: q IPC nesting depth exceeds limit %d", ErrBadMsg, d.limits.MaxDepth)
	}
	if d.objects >= d.limits.MaxObjects {
		return nil, fmt.Errorf("%w: decoded object count exceeds limit %d", ErrBadMsg, d.limits.MaxObjects)
	}
	d.objects++
	if err := d.reserve(decodedObjectOverhead, "object"); err != nil {
		return nil, err
	}
	typeOffset := d.offset
	rawType, err := d.readByte("q type")
	if err != nil {
		return nil, err
	}
	qtype := int8(rawType)

	switch qtype {
	case -KB:
		value, err := d.readByte("boolean atom")
		if err != nil {
			return nil, err
		}
		if value > 1 {
			return nil, fmt.Errorf("%w: invalid boolean atom value %d", ErrBadMsg, value)
		}
		return &K{qtype, NONE, value == 1}, nil
	case -UU:
		value, err := d.read(16, "guid atom")
		if err != nil {
			return nil, err
		}
		var id uuid.UUID
		copy(id[:], value)
		return &K{qtype, NONE, id}, nil
	case -KG, -KC:
		value, err := d.readByte("byte/char atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, value}, nil
	case -KH:
		value, err := d.read(2, "short atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int16(d.order.Uint16(value))}, nil
	case -KI:
		value, err := d.read(4, "int atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int32(d.order.Uint32(value))}, nil
	case -KJ:
		value, err := d.read(8, "long atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int64(d.order.Uint64(value))}, nil
	case -KE:
		value, err := d.read(4, "real atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, math.Float32frombits(d.order.Uint32(value))}, nil
	case -KF:
		value, err := d.read(8, "float atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, math.Float64frombits(d.order.Uint64(value))}, nil
	case -KS:
		value, err := d.readCString("symbol atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, value}, nil
	case -KP:
		value, err := d.read(8, "timestamp atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, qEpoch.Add(time.Duration(int64(d.order.Uint64(value))))}, nil
	case -KM:
		value, err := d.read(4, "month atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, Month(int32(d.order.Uint32(value)))}, nil
	case -KD:
		value, err := d.read(4, "date atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int32(d.order.Uint32(value))}, nil
	case -KZ:
		value, err := d.read(8, "datetime atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, math.Float64frombits(d.order.Uint64(value))}, nil
	case -KN:
		value, err := d.read(8, "timespan atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, time.Duration(int64(d.order.Uint64(value)))}, nil
	case -KU:
		value, err := d.read(4, "minute atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int32(d.order.Uint32(value))}, nil
	case -KV:
		value, err := d.read(4, "second atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int32(d.order.Uint32(value))}, nil
	case -KT:
		value, err := d.read(4, "time atom")
		if err != nil {
			return nil, err
		}
		return &K{qtype, NONE, int32(d.order.Uint32(value))}, nil
	case KB, UU, KG, KH, KI, KJ, KE, KF, KC, KS, KP, KM, KD, KZ, KN, KU, KV, KT:
		return d.decodeVector(qtype)
	case K0:
		attr, count, err := d.readAttrCount("generic list")
		if err != nil {
			return nil, err
		}
		if count > d.limits.MaxObjects-d.objects {
			return nil, fmt.Errorf("%w: generic list count %d exceeds remaining object budget %d", ErrBadMsg, count, d.limits.MaxObjects-d.objects)
		}
		if err := d.reserve(count*8, "generic list"); err != nil {
			return nil, err
		}
		values := make([]*K, int(count))
		for i := range values {
			values[i], err = d.decodeObject(depth + 1)
			if err != nil {
				return nil, fmt.Errorf("decode generic list item %d: %w", i, err)
			}
		}
		return &K{qtype, attr, values}, nil
	case XD, SD:
		key, err := d.decodeObject(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("decode dictionary key: %w", err)
		}
		value, err := d.decodeObject(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("decode dictionary value: %w", err)
		}
		keyLen, ok := logicalLength(key)
		if !ok {
			return nil, fmt.Errorf("%w: dictionary key has invalid shape", ErrBadMsg)
		}
		valueLen, ok := logicalLength(value)
		if !ok || keyLen != valueLen {
			return nil, fmt.Errorf("%w: dictionary key/value lengths differ (%d/%d)", ErrBadMsg, keyLen, valueLen)
		}
		result := NewDict(key, value)
		if qtype == SD {
			result.Attr = SORTED
		}
		return result, nil
	case XT:
		rawAttr, err := d.readByte("table attribute")
		if err != nil {
			return nil, err
		}
		attr := Attr(int8(rawAttr))
		if attr < NONE || attr > GROUPED {
			return nil, fmt.Errorf("%w: invalid table attribute %d", ErrBadMsg, rawAttr)
		}
		dictK, err := d.decodeObject(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("decode table dictionary: %w", err)
		}
		if dictK == nil || dictK.Type != XD {
			return nil, fmt.Errorf("%w: table payload must be a dictionary", ErrBadMsg)
		}
		dict, ok := dictK.Data.(Dict)
		if !ok || dict.Key == nil || dict.Value == nil || dict.Key.Type != KS || dict.Value.Type != K0 {
			return nil, fmt.Errorf("%w: table dictionary must contain symbol keys and a generic column list", ErrBadMsg)
		}
		columns, ok := dict.Key.Data.([]string)
		if !ok {
			return nil, fmt.Errorf("%w: table column names have invalid representation", ErrBadMsg)
		}
		values, ok := dict.Value.Data.([]*K)
		if !ok || len(columns) != len(values) {
			return nil, fmt.Errorf("%w: table column name/value counts differ", ErrBadMsg)
		}
		rows := -1
		for i, column := range values {
			if column == nil || column.Type < K0 || column.Type > KT {
				return nil, fmt.Errorf("%w: table column %d is not a q list", ErrBadMsg, i)
			}
			n, ok := logicalLength(column)
			if !ok {
				return nil, fmt.Errorf("%w: table column %d has invalid shape", ErrBadMsg, i)
			}
			if rows < 0 {
				rows = n
			} else if n != rows {
				return nil, fmt.Errorf("%w: table column %d has %d rows; expected %d", ErrBadMsg, i, n, rows)
			}
		}
		return &K{qtype, attr, Table{Columns: columns, Data: values}}, nil
	case KFUNC:
		namespace, err := d.readCString("function namespace")
		if err != nil {
			return nil, err
		}
		bodyK, err := d.decodeObject(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("decode function body: %w", err)
		}
		body, ok := bodyK.Data.(string)
		if bodyK.Type != KC || !ok {
			return nil, fmt.Errorf("%w: function body must be a char vector", ErrBadMsg)
		}
		if uint64(len(body)) > d.limits.MaxStringBytes {
			return nil, fmt.Errorf("%w: function body exceeds %d bytes", ErrBadMsg, d.limits.MaxStringBytes)
		}
		return &K{qtype, NONE, Function{Namespace: namespace, Body: body}}, nil
	case KFUNCUP, KFUNCBP, KFUNCTR:
		index, err := d.readByte("primitive index")
		if err != nil {
			return nil, err
		}
		valid := qtype == KFUNCUP && (int(index) < len(unaryops) || index == 255)
		valid = valid || qtype == KFUNCBP && int(index) < len(binaryops)
		valid = valid || qtype == KFUNCTR && int(index) < len(ternaryops)
		if !valid {
			return nil, fmt.Errorf("%w: invalid primitive index %d for type %d", ErrBadMsg, index, qtype)
		}
		return &K{qtype, NONE, index}, nil
	case KPROJ, KCOMP:
		rawCount, err := d.readUint32("projection/composition count")
		if err != nil {
			return nil, err
		}
		if rawCount > math.MaxInt32 {
			return nil, fmt.Errorf("%w: projection/composition count %d exceeds q IPC int32 count", ErrBadMsg, rawCount)
		}
		count := uint64(rawCount)
		if count > d.limits.MaxVectorElements || count > d.limits.MaxObjects-d.objects {
			return nil, fmt.Errorf("%w: projection/composition count %d exceeds decode limits", ErrBadMsg, count)
		}
		if count > uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("%w: projection/composition count %d does not fit in memory", ErrBadMsg, count)
		}
		if err := d.reserve(count*8, "projection/composition"); err != nil {
			return nil, err
		}
		values := make([]*K, int(count))
		for i := range values {
			values[i], err = d.decodeObject(depth + 1)
			if err != nil {
				return nil, fmt.Errorf("decode projection/composition item %d: %w", i, err)
			}
		}
		return &K{qtype, NONE, values}, nil
	case KEACH, KOVER, KSCAN, KPRIOR, KEACHRIGHT, KEACHLEFT:
		value, err := d.decodeObject(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("decode iterator operand: %w", err)
		}
		return &K{qtype, NONE, value}, nil
	case KDYNLOAD:
		return nil, fmt.Errorf("%w: dynamic-load type is unsupported over IPC", ErrBadMsg)
	case KERR:
		message, err := d.readCString("q error")
		if err != nil {
			return nil, err
		}
		return nil, remoteError{message: message}
	default:
		return nil, fmt.Errorf("%w: unsupported q type %d at offset %d", ErrBadMsg, qtype, typeOffset)
	}
}

func (d *objectDecoder) decodeVector(qtype int8) (*K, error) {
	attr, count, err := d.readAttrCount(fmt.Sprintf("q vector type %d", qtype))
	if err != nil {
		return nil, err
	}
	makeBytes := func(width uint64, description string) ([]byte, error) {
		if err := d.reserve(count*width, description); err != nil {
			return nil, err
		}
		return d.fixedBytes(count, width, description)
	}

	switch qtype {
	case KB:
		raw, err := makeBytes(1, "boolean vector")
		if err != nil {
			return nil, err
		}
		values := make([]bool, int(count))
		for i, value := range raw {
			if value > 1 {
				return nil, fmt.Errorf("%w: invalid boolean vector value %d at index %d", ErrBadMsg, value, i)
			}
			values[i] = value == 1
		}
		return &K{qtype, attr, values}, nil
	case UU:
		raw, err := makeBytes(16, "guid vector")
		if err != nil {
			return nil, err
		}
		values := make([]uuid.UUID, int(count))
		for i := range values {
			copy(values[i][:], raw[i*16:(i+1)*16])
		}
		return &K{qtype, attr, values}, nil
	case KG:
		raw, err := makeBytes(1, "byte vector")
		if err != nil {
			return nil, err
		}
		values := append([]byte(nil), raw...)
		return &K{qtype, attr, values}, nil
	case KC:
		if count > uint64(d.limits.MaxStringBytes) {
			return nil, fmt.Errorf(
				"%w: char vector length %d exceeds limit %d",
				ErrBadMsg,
				count,
				d.limits.MaxStringBytes,
			)
		}
		raw, err := makeBytes(1, "char vector")
		if err != nil {
			return nil, err
		}
		return &K{qtype, attr, string(raw)}, nil
	case KS:
		if err := d.reserve(count*16, "symbol vector"); err != nil {
			return nil, err
		}
		values := make([]string, int(count))
		for i := range values {
			values[i], err = d.readCString(fmt.Sprintf("symbol vector item %d", i))
			if err != nil {
				return nil, err
			}
		}
		return &K{qtype, attr, values}, nil
	case KH:
		raw, err := makeBytes(2, "short vector")
		if err != nil {
			return nil, err
		}
		values := make([]int16, int(count))
		for i := range values {
			values[i] = int16(d.order.Uint16(raw[i*2:]))
		}
		return &K{qtype, attr, values}, nil
	case KI:
		raw, err := makeBytes(4, "int vector")
		if err != nil {
			return nil, err
		}
		values := make([]int32, int(count))
		for i := range values {
			values[i] = int32(d.order.Uint32(raw[i*4:]))
		}
		return &K{qtype, attr, values}, nil
	case KJ:
		raw, err := makeBytes(8, "long vector")
		if err != nil {
			return nil, err
		}
		values := make([]int64, int(count))
		for i := range values {
			values[i] = int64(d.order.Uint64(raw[i*8:]))
		}
		return &K{qtype, attr, values}, nil
	case KE:
		raw, err := makeBytes(4, "real vector")
		if err != nil {
			return nil, err
		}
		values := make([]float32, int(count))
		for i := range values {
			values[i] = math.Float32frombits(d.order.Uint32(raw[i*4:]))
		}
		return &K{qtype, attr, values}, nil
	case KF:
		raw, err := makeBytes(8, "float vector")
		if err != nil {
			return nil, err
		}
		values := make([]float64, int(count))
		for i := range values {
			values[i] = math.Float64frombits(d.order.Uint64(raw[i*8:]))
		}
		return &K{qtype, attr, values}, nil
	case KP:
		if err := d.reserve(count*24, "timestamp vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 8, "timestamp vector")
		if err != nil {
			return nil, err
		}
		values := make([]time.Time, int(count))
		for i := range values {
			values[i] = qEpoch.Add(time.Duration(int64(d.order.Uint64(raw[i*8:]))))
		}
		return &K{qtype, attr, values}, nil
	case KM:
		raw, err := makeBytes(4, "month vector")
		if err != nil {
			return nil, err
		}
		values := make([]Month, int(count))
		for i := range values {
			values[i] = Month(int32(d.order.Uint32(raw[i*4:])))
		}
		return &K{qtype, attr, values}, nil
	case KD:
		if err := d.reserve(count*24, "date vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 4, "date vector")
		if err != nil {
			return nil, err
		}
		values := make([]time.Time, int(count))
		for i := range values {
			days := int32(d.order.Uint32(raw[i*4:]))
			values[i] = qEpoch.Add(time.Duration(days) * 24 * time.Hour)
		}
		return &K{qtype, attr, values}, nil
	case KZ:
		if err := d.reserve(count*24, "datetime vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 8, "datetime vector")
		if err != nil {
			return nil, err
		}
		values := make([]time.Time, int(count))
		for i := range values {
			days := math.Float64frombits(d.order.Uint64(raw[i*8:]))
			values[i] = qEpoch.Add(time.Duration(days * float64(24*time.Hour)))
		}
		return &K{qtype, attr, values}, nil
	case KN:
		raw, err := makeBytes(8, "timespan vector")
		if err != nil {
			return nil, err
		}
		values := make([]time.Duration, int(count))
		for i := range values {
			values[i] = time.Duration(int64(d.order.Uint64(raw[i*8:])))
		}
		return &K{qtype, attr, values}, nil
	case KU:
		if err := d.reserve(count*24, "minute vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 4, "minute vector")
		if err != nil {
			return nil, err
		}
		values := make([]Minute, int(count))
		for i := range values {
			minutes := int32(d.order.Uint32(raw[i*4:]))
			values[i] = Minute(time.Time{}.Add(time.Duration(minutes) * time.Minute))
		}
		return &K{qtype, attr, values}, nil
	case KV:
		if err := d.reserve(count*24, "second vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 4, "second vector")
		if err != nil {
			return nil, err
		}
		values := make([]Second, int(count))
		for i := range values {
			seconds := int32(d.order.Uint32(raw[i*4:]))
			values[i] = Second(time.Time{}.Add(time.Duration(seconds) * time.Second))
		}
		return &K{qtype, attr, values}, nil
	case KT:
		if err := d.reserve(count*24, "time vector"); err != nil {
			return nil, err
		}
		raw, err := d.fixedBytes(count, 4, "time vector")
		if err != nil {
			return nil, err
		}
		values := make([]Time, int(count))
		for i := range values {
			millis := int32(d.order.Uint32(raw[i*4:]))
			values[i] = Time(qEpoch.Add(time.Duration(millis) * time.Millisecond))
		}
		return &K{qtype, attr, values}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported q vector type %d", ErrBadMsg, qtype)
	}
}
