package plugin

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	kdb "github.com/greg/asyncq/third_party/kdbgo"
	uuid "github.com/nu7hatch/gouuid"
)

const (
	maxKdbObjectDepth = 64

	// These bounds mirror the q IPC primitive tables used by kdbgo's encoder
	// and decoder. Unary index 255 is the wire representation of identity.
	maxKdbUnaryPrimitiveIndex      = 41
	kdbUnaryIdentityPrimitiveIndex = 255
	maxKdbBinaryPrimitiveIndex     = 33
	maxKdbTernaryPrimitiveIndex    = 2

	maxKdbResponseFrames          = 1024
	maxKdbFieldsPerFrame          = 1024
	maxKdbResponseFields          = 4096
	maxKdbResponseRows            = 1_000_000
	maxKdbResponseCells           = 4_000_000
	maxKdbFieldNameBytes          = 256
	maxKdbFieldNameBytesTotal     = 1 << 20
	maxKdbMaterializedStringBytes = 32 << 20
	maxKdbMaterializedFrameBytes  = 128 << 20
	maxKdbVisitedObjects          = 1_000_000
	maxKdbTraversedEdges          = 1_000_000
	maxKdbCellStringBytes         = 1 << 20

	// Dictionary-list conversion can concurrently retain an interface slot,
	// boxed time/string data, a typed intermediate, and the parser-owned vector
	// handed to the SDK. 128 bytes per cell covers that peak plus alignment.
	estimatedKdbCellBytes          = 128
	estimatedKdbNullableValueBytes = 24
	estimatedKdbFieldBytes         = 256
	estimatedKdbFrameBytes         = 512
	kdbUUIDTextBytes               = 36
)

var maxKdbTransportStringBytes = kdb.DefaultDecodeLimits().MaxStringBytes

type kdbFrameParseLimits struct {
	MaxFrames          uint64
	MaxFieldsPerFrame  uint64
	MaxFieldsTotal     uint64
	MaxRowsTotal       uint64
	MaxCellsTotal      uint64
	MaxFieldNameBytes  uint64
	MaxNameBytesTotal  uint64
	MaxStringBytes     uint64
	MaxMaterialized    uint64
	MaxDepth           int
	MaxObjects         uint64
	MaxEdges           uint64
	MaxCellStringBytes uint64
}

var defaultKdbFrameParseLimits = kdbFrameParseLimits{
	MaxFrames:          maxKdbResponseFrames,
	MaxFieldsPerFrame:  maxKdbFieldsPerFrame,
	MaxFieldsTotal:     maxKdbResponseFields,
	MaxRowsTotal:       maxKdbResponseRows,
	MaxCellsTotal:      maxKdbResponseCells,
	MaxFieldNameBytes:  maxKdbFieldNameBytes,
	MaxNameBytesTotal:  maxKdbFieldNameBytesTotal,
	MaxStringBytes:     maxKdbMaterializedStringBytes,
	MaxMaterialized:    maxKdbMaterializedFrameBytes,
	MaxDepth:           maxKdbObjectDepth,
	MaxObjects:         maxKdbVisitedObjects,
	MaxEdges:           maxKdbTraversedEdges,
	MaxCellStringBytes: maxKdbCellStringBytes,
}

func (limits kdbFrameParseLimits) normalized() kdbFrameParseLimits {
	defaults := defaultKdbFrameParseLimits
	if limits.MaxFrames == 0 {
		limits.MaxFrames = defaults.MaxFrames
	}
	if limits.MaxFieldsPerFrame == 0 {
		limits.MaxFieldsPerFrame = defaults.MaxFieldsPerFrame
	}
	if limits.MaxFieldsTotal == 0 {
		limits.MaxFieldsTotal = defaults.MaxFieldsTotal
	}
	if limits.MaxRowsTotal == 0 {
		limits.MaxRowsTotal = defaults.MaxRowsTotal
	}
	if limits.MaxCellsTotal == 0 {
		limits.MaxCellsTotal = defaults.MaxCellsTotal
	}
	if limits.MaxFieldNameBytes == 0 {
		limits.MaxFieldNameBytes = defaults.MaxFieldNameBytes
	}
	if limits.MaxNameBytesTotal == 0 {
		limits.MaxNameBytesTotal = defaults.MaxNameBytesTotal
	}
	if limits.MaxStringBytes == 0 {
		limits.MaxStringBytes = defaults.MaxStringBytes
	}
	if limits.MaxMaterialized == 0 {
		limits.MaxMaterialized = defaults.MaxMaterialized
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxObjects == 0 {
		limits.MaxObjects = defaults.MaxObjects
	}
	if limits.MaxEdges == 0 {
		limits.MaxEdges = defaults.MaxEdges
	}
	if limits.MaxCellStringBytes == 0 {
		limits.MaxCellStringBytes = defaults.MaxCellStringBytes
	}
	return limits
}

type kdbFrameParseBudget struct {
	limits            kdbFrameParseLimits
	frames            uint64
	fields            uint64
	rows              uint64
	cells             uint64
	nameBytes         uint64
	stringBytes       uint64
	materializedBytes uint64
	objects           uint64
	edges             uint64
}

func newKdbFrameParseBudget(limits kdbFrameParseLimits) *kdbFrameParseBudget {
	return &kdbFrameParseBudget{limits: limits.normalized()}
}

func checkedAddUint64(a uint64, b uint64) (uint64, bool) {
	sum := a + b
	return sum, sum >= a
}

func checkedMulUint64(a uint64, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	product := a * b
	return product, product/a == b
}

func (b *kdbFrameParseBudget) reserveRepeatedFrames(frameCount int, fieldCount int) error {
	if frameCount < 0 || fieldCount < 0 {
		return fmt.Errorf("kdb+ response dimensions cannot be negative")
	}
	frames := uint64(frameCount)
	fields := uint64(fieldCount)
	if frames > b.limits.MaxFrames {
		return fmt.Errorf("kdb+ response exceeds frame limit")
	}
	if fields > b.limits.MaxFieldsPerFrame {
		return fmt.Errorf("kdb+ response exceeds fields-per-frame limit")
	}
	totalFields, ok := checkedMulUint64(frames, fields)
	if !ok {
		return fmt.Errorf("kdb+ response dimensions overflow")
	}
	if totalFields > b.limits.MaxFieldsTotal {
		return fmt.Errorf("kdb+ response exceeds total field limit")
	}
	return nil
}

func (b *kdbFrameParseBudget) reserveFrame(frameName string, fieldNames []string, rowCount int, retainedRefID string) error {
	if rowCount < 0 {
		return fmt.Errorf("kdb+ response row count cannot be negative")
	}
	if err := validateEmittedName(frameName, b.limits.MaxFieldNameBytes, "frame name"); err != nil {
		return err
	}
	fieldCount := uint64(len(fieldNames))
	if fieldCount > b.limits.MaxFieldsPerFrame {
		return fmt.Errorf("kdb+ response exceeds fields-per-frame limit")
	}

	seen := make(map[string]struct{}, len(fieldNames))
	frameNameBytes := uint64(len(frameName))
	newNameBytes, ok := checkedAddUint64(frameNameBytes, uint64(len(retainedRefID)))
	if !ok {
		return fmt.Errorf("kdb+ response name bytes overflow")
	}
	for i, name := range fieldNames {
		if err := validateEmittedName(name, b.limits.MaxFieldNameBytes, "field name"); err != nil {
			return fmt.Errorf("field %d: %w", i, err)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate field name at index %d", i)
		}
		seen[name] = struct{}{}
		var ok bool
		newNameBytes, ok = checkedAddUint64(newNameBytes, uint64(len(name)))
		if !ok {
			return fmt.Errorf("kdb+ response name bytes overflow")
		}
	}

	rows := uint64(rowCount)
	cells, ok := checkedMulUint64(rows, fieldCount)
	if !ok {
		return fmt.Errorf("kdb+ response dimensions overflow")
	}
	nextFrames, ok := checkedAddUint64(b.frames, 1)
	if !ok || nextFrames > b.limits.MaxFrames {
		return fmt.Errorf("kdb+ response exceeds frame limit")
	}
	nextFields, ok := checkedAddUint64(b.fields, fieldCount)
	if !ok || nextFields > b.limits.MaxFieldsTotal {
		return fmt.Errorf("kdb+ response exceeds total field limit")
	}
	nextRows, ok := checkedAddUint64(b.rows, rows)
	if !ok || nextRows > b.limits.MaxRowsTotal {
		return fmt.Errorf("kdb+ response exceeds total row limit")
	}
	nextCells, ok := checkedAddUint64(b.cells, cells)
	if !ok || nextCells > b.limits.MaxCellsTotal {
		return fmt.Errorf("kdb+ response exceeds materialized cell limit")
	}
	nextNameBytes, ok := checkedAddUint64(b.nameBytes, newNameBytes)
	if !ok || nextNameBytes > b.limits.MaxNameBytesTotal {
		return fmt.Errorf("kdb+ response exceeds field-name byte limit")
	}

	cellBytes, ok := checkedMulUint64(cells, estimatedKdbCellBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	fieldBytes, ok := checkedMulUint64(fieldCount, estimatedKdbFieldBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	frameBytes, ok := checkedAddUint64(cellBytes, fieldBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	frameBytes, ok = checkedAddUint64(frameBytes, estimatedKdbFrameBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	frameBytes, ok = checkedAddUint64(frameBytes, newNameBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	nextMaterialized, ok := checkedAddUint64(b.materializedBytes, frameBytes)
	if !ok || nextMaterialized > b.limits.MaxMaterialized {
		return fmt.Errorf("kdb+ response exceeds materialized byte limit")
	}

	b.frames = nextFrames
	b.fields = nextFields
	b.rows = nextRows
	b.cells = nextCells
	b.nameBytes = nextNameBytes
	b.materializedBytes = nextMaterialized
	return nil
}

func validateEmittedName(name string, maxBytes uint64, kind string) error {
	if name == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%s is not valid UTF-8", kind)
	}
	if uint64(len(name)) > maxBytes {
		return fmt.Errorf("%s exceeds byte limit", kind)
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", kind)
		}
	}
	return nil
}

func (b *kdbFrameParseBudget) chargeString(value string, copies int) error {
	if copies < 0 {
		return fmt.Errorf("kdb+ response string multiplicity cannot be negative")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("kdb+ response contains invalid UTF-8 string data")
	}
	length := uint64(len(value))
	if length > b.limits.MaxCellStringBytes {
		return fmt.Errorf("kdb+ response string exceeds per-value byte limit")
	}
	bytes, ok := checkedMulUint64(length, uint64(copies))
	if !ok {
		return fmt.Errorf("kdb+ response string size overflows")
	}
	return b.chargeStringBytes(bytes)
}

func (b *kdbFrameParseBudget) chargeStringBytes(bytes uint64) error {
	nextStrings, ok := checkedAddUint64(b.stringBytes, bytes)
	if !ok || nextStrings > b.limits.MaxStringBytes {
		return fmt.Errorf("kdb+ response exceeds materialized string byte limit")
	}
	nextMaterialized, ok := checkedAddUint64(b.materializedBytes, bytes)
	if !ok || nextMaterialized > b.limits.MaxMaterialized {
		return fmt.Errorf("kdb+ response exceeds materialized byte limit")
	}
	b.stringBytes = nextStrings
	b.materializedBytes = nextMaterialized
	return nil
}

func (b *kdbFrameParseBudget) chargeNullablePointers(count int) error {
	if count < 0 {
		return fmt.Errorf("kdb+ response pointer count cannot be negative")
	}
	// Nullable fields use a pointer slice plus individually escaped values.
	// Charge the largest supported pointed-to scalar (time.Time).
	bytes, ok := checkedMulUint64(uint64(count), estimatedKdbNullableValueBytes)
	if !ok {
		return fmt.Errorf("kdb+ response materialized size overflows")
	}
	next, ok := checkedAddUint64(b.materializedBytes, bytes)
	if !ok || next > b.limits.MaxMaterialized {
		return fmt.Errorf("kdb+ response exceeds materialized byte limit")
	}
	b.materializedBytes = next
	return nil
}

func (b *kdbFrameParseBudget) reserveEdges(count int) error {
	if count < 0 {
		return fmt.Errorf("kdb+ response edge count cannot be negative")
	}
	next, ok := checkedAddUint64(b.edges, uint64(count))
	if !ok || next > b.limits.MaxEdges {
		return fmt.Errorf("kdb+ response exceeds traversed edge limit")
	}
	b.edges = next
	return nil
}

type kdbObjectVisitState uint8

const (
	kdbObjectVisiting kdbObjectVisitState = iota + 1
	kdbObjectVisited
)

type kdbObjectValidationMemo struct {
	state  kdbObjectVisitState
	height int
}

func validateKdbObject(value *kdb.K) error {
	budget := newKdbFrameParseBudget(defaultKdbFrameParseLimits)
	return validateKdbObjectWithBudget(value, budget)
}

func validateKdbObjectWithBudget(value *kdb.K, budget *kdbFrameParseBudget) error {
	if budget == nil {
		return fmt.Errorf("parser budget is nil")
	}
	// Match the IPC decoder: the root object is depth one and nested children
	// increment from there.
	_, err := validateKdbObjectAt(value, "object", 1, budget, make(map[*kdb.K]*kdbObjectValidationMemo))
	return err
}

func validateKdbObjectAt(
	value *kdb.K,
	location string,
	depth int,
	budget *kdbFrameParseBudget,
	states map[*kdb.K]*kdbObjectValidationMemo,
) (height int, err error) {
	if value == nil {
		return 0, fmt.Errorf("%s is nil", location)
	}
	if err := validateKdbPathDepth(depth, 1, budget.limits.MaxDepth, location); err != nil {
		return 0, err
	}
	memo := states[value]
	if memo != nil {
		switch memo.state {
		case kdbObjectVisiting:
			return 0, fmt.Errorf("%s contains a cycle", location)
		case kdbObjectVisited:
			if err := validateKdbPathDepth(depth, memo.height, budget.limits.MaxDepth, location); err != nil {
				return 0, err
			}
			return memo.height, nil
		}
	}
	nextObjects, ok := checkedAddUint64(budget.objects, 1)
	if !ok || nextObjects > budget.limits.MaxObjects {
		return 0, fmt.Errorf("kdb+ response exceeds visited object limit")
	}
	budget.objects = nextObjects
	memo = &kdbObjectValidationMemo{state: kdbObjectVisiting}
	states[value] = memo
	validated := false
	defer func() {
		if !validated {
			delete(states, value)
		}
	}()

	if value.Type >= kdb.K0 && (value.Attr < kdb.NONE || value.Attr > kdb.GROUPED) {
		return 0, fmt.Errorf("%s has invalid attribute %d", location, value.Attr)
	}
	if value.Type < kdb.K0 && value.Attr != kdb.NONE {
		return 0, fmt.Errorf("%s atom has invalid attribute %d", location, value.Attr)
	}

	height = 1
	switch {
	case value.Type < kdb.K0:
		if err := validateKdbAtom(value); err != nil {
			return 0, fmt.Errorf("%s: %w", location, err)
		}
	case value.Type == kdb.K0:
		items, ok := value.Data.([]*kdb.K)
		if !ok {
			return 0, fmt.Errorf("%s has invalid data for generic list: expected []*kdb.K, got %T", location, value.Data)
		}
		if err := budget.reserveEdges(len(items)); err != nil {
			return 0, err
		}
		childLocation := location + " list item"
		for i, item := range items {
			if item == nil {
				return 0, fmt.Errorf("%s item %d is nil", location, i)
			}
			childHeight, err := validateKdbObjectAt(item, childLocation, depth+1, budget, states)
			if err != nil {
				return 0, fmt.Errorf("%s item %d: %w", location, i, err)
			}
			height, err = includeKdbChildHeight(height, childHeight)
			if err != nil {
				return 0, err
			}
		}
	case value.Type > kdb.K0 && value.Type <= kdb.KT:
		if _, err := kdbVectorLength(value); err != nil {
			return 0, fmt.Errorf("%s: %w", location, err)
		}
	case value.Type == kdb.XT:
		table, ok := value.Data.(kdb.Table)
		if !ok {
			return 0, fmt.Errorf("%s has invalid table data: expected kdb.Table, got %T", location, value.Data)
		}
		height, err = validateKdbTableAt(table, location, depth, budget, states)
		if err != nil {
			return 0, err
		}
	case value.Type == kdb.XD:
		dict, ok := value.Data.(kdb.Dict)
		if !ok {
			return 0, fmt.Errorf("%s has invalid dictionary data: expected kdb.Dict, got %T", location, value.Data)
		}
		if err := budget.reserveEdges(2); err != nil {
			return 0, err
		}
		if dict.Key == nil {
			return 0, fmt.Errorf("%s dictionary key is nil", location)
		}
		if dict.Value == nil {
			return 0, fmt.Errorf("%s dictionary value is nil", location)
		}
		keyHeight, err := validateKdbObjectAt(dict.Key, location+" dictionary key", depth+1, budget, states)
		if err != nil {
			return 0, err
		}
		height, err = includeKdbChildHeight(height, keyHeight)
		if err != nil {
			return 0, err
		}
		valueHeight, err := validateKdbObjectAt(dict.Value, location+" dictionary value", depth+1, budget, states)
		if err != nil {
			return 0, err
		}
		height, err = includeKdbChildHeight(height, valueHeight)
		if err != nil {
			return 0, err
		}
		if dict.Key.Type == kdb.XT && dict.Value.Type == kdb.XT {
			keyRows, err := tableRowCount(dict.Key.Data.(kdb.Table))
			if err != nil {
				return 0, fmt.Errorf("%s keyed table key rows: %w", location, err)
			}
			valueRows, err := tableRowCount(dict.Value.Data.(kdb.Table))
			if err != nil {
				return 0, fmt.Errorf("%s keyed table value rows: %w", location, err)
			}
			if keyRows != valueRows {
				return 0, fmt.Errorf("%s keyed table key/value row counts differ: %d and %d", location, keyRows, valueRows)
			}
		}
	case value.Type == kdb.KFUNC:
		function, ok := value.Data.(kdb.Function)
		if !ok {
			return 0, fmt.Errorf("%s has invalid function data: expected kdb.Function, got %T", location, value.Data)
		}
		if err := validateKdbFunction(function); err != nil {
			return 0, fmt.Errorf("%s has invalid function data: %w", location, err)
		}
		// A KFUNC is flattened into kdb.Function in memory, but on the wire its
		// body is a nested KC object. Preserve that edge and subtree height.
		if err := budget.reserveEdges(1); err != nil {
			return 0, err
		}
		height = 2
	case value.Type == kdb.KFUNCUP:
		operator, ok := value.Data.(byte)
		if !ok {
			return 0, fmt.Errorf("%s has invalid unary-function data: expected byte, got %T", location, value.Data)
		}
		if operator > maxKdbUnaryPrimitiveIndex && operator != kdbUnaryIdentityPrimitiveIndex {
			return 0, fmt.Errorf("%s has invalid unary-function index %d", location, operator)
		}
	case value.Type == kdb.KFUNCBP:
		operator, ok := value.Data.(byte)
		if !ok {
			return 0, fmt.Errorf("%s has invalid binary-function data: expected byte, got %T", location, value.Data)
		}
		if operator > maxKdbBinaryPrimitiveIndex {
			return 0, fmt.Errorf("%s has invalid binary-function index %d", location, operator)
		}
	case value.Type == kdb.KFUNCTR:
		operator, ok := value.Data.(byte)
		if !ok {
			return 0, fmt.Errorf("%s has invalid ternary-function data: expected byte, got %T", location, value.Data)
		}
		if operator > maxKdbTernaryPrimitiveIndex {
			return 0, fmt.Errorf("%s has invalid ternary-function index %d", location, operator)
		}
	case value.Type == kdb.KPROJ || value.Type == kdb.KCOMP:
		items, ok := value.Data.([]*kdb.K)
		if !ok {
			return 0, fmt.Errorf("%s has invalid function-list data: expected []*kdb.K, got %T", location, value.Data)
		}
		if err := budget.reserveEdges(len(items)); err != nil {
			return 0, err
		}
		childLocation := location + " function item"
		for i, item := range items {
			if item == nil {
				return 0, fmt.Errorf("%s function item %d is nil", location, i)
			}
			childHeight, err := validateKdbObjectAt(item, childLocation, depth+1, budget, states)
			if err != nil {
				return 0, fmt.Errorf("%s function item %d: %w", location, i, err)
			}
			height, err = includeKdbChildHeight(height, childHeight)
			if err != nil {
				return 0, err
			}
		}
	case value.Type >= kdb.KEACH && value.Type <= kdb.KEACHLEFT:
		operand, ok := value.Data.(*kdb.K)
		if !ok {
			return 0, fmt.Errorf("%s has invalid adverb data: expected *kdb.K, got %T", location, value.Data)
		}
		if err := budget.reserveEdges(1); err != nil {
			return 0, err
		}
		childHeight, err := validateKdbObjectAt(operand, location+" adverb operand", depth+1, budget, states)
		if err != nil {
			return 0, err
		}
		height, err = includeKdbChildHeight(height, childHeight)
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("%s has unsupported kdb+ type %d", location, value.Type)
	}

	if err := validateKdbPathDepth(depth, height, budget.limits.MaxDepth, location); err != nil {
		return 0, err
	}
	memo.state = kdbObjectVisited
	memo.height = height
	validated = true
	return height, nil
}

func validateKdbFunction(function kdb.Function) error {
	if uint64(len(function.Namespace)) > maxKdbTransportStringBytes {
		return fmt.Errorf("function namespace exceeds transport string limit")
	}
	if strings.IndexByte(function.Namespace, 0) >= 0 {
		return fmt.Errorf("function namespace contains NUL")
	}
	if uint64(len(function.Body)) > maxKdbTransportStringBytes {
		return fmt.Errorf("function body exceeds transport string limit")
	}
	return nil
}

func validateKdbPathDepth(depth int, height int, maximum int, location string) error {
	if depth < 1 || height < 1 || maximum < 1 {
		return fmt.Errorf("%s has invalid nesting metadata", location)
	}
	endDepth, ok := checkedAddUint64(uint64(depth), uint64(height-1))
	if !ok || endDepth > uint64(maximum) {
		return fmt.Errorf("%s exceeds maximum nesting depth", location)
	}
	return nil
}

func includeKdbChildHeight(current int, child int) (int, error) {
	next, ok := checkedAddUint64(uint64(child), 1)
	if !ok || next > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("kdb+ response subtree height overflows")
	}
	if int(next) > current {
		return int(next), nil
	}
	return current, nil
}

func validateKdbTableAt(
	table kdb.Table,
	location string,
	depth int,
	budget *kdbFrameParseBudget,
	states map[*kdb.K]*kdbObjectValidationMemo,
) (int, error) {
	if len(table.Columns) != len(table.Data) {
		return 0, fmt.Errorf("%s table column name/data counts differ: %d and %d", location, len(table.Columns), len(table.Data))
	}
	if uint64(len(table.Columns)) > budget.limits.MaxFieldsPerFrame {
		return 0, fmt.Errorf("%s table exceeds column limit", location)
	}
	if err := budget.reserveEdges(len(table.Data)); err != nil {
		return 0, err
	}
	rowCount := -1
	height := 1
	columnLocation := location + " table column"
	for i, column := range table.Data {
		if column == nil {
			return 0, fmt.Errorf("%s table column %d is nil", location, i)
		}
		if column.Type < kdb.K0 || column.Type > kdb.KT {
			return 0, fmt.Errorf("%s table column %d is not a vector", location, i)
		}
		childHeight, err := validateKdbObjectAt(column, columnLocation, depth+1, budget, states)
		if err != nil {
			return 0, fmt.Errorf("%s table column %d: %w", location, i, err)
		}
		height, err = includeKdbChildHeight(height, childHeight)
		if err != nil {
			return 0, err
		}
		length, err := kdbVectorLength(column)
		if err != nil {
			return 0, fmt.Errorf("%s table column %d: %w", location, i, err)
		}
		if rowCount == -1 {
			rowCount = length
		} else if rowCount != length {
			return 0, fmt.Errorf("%s table columns have unequal row lengths: %d and %d", location, rowCount, length)
		}
	}
	return height, nil
}

func tableRowCount(table kdb.Table) (int, error) {
	if len(table.Columns) != len(table.Data) {
		return 0, fmt.Errorf("table column name/data counts differ: %d and %d", len(table.Columns), len(table.Data))
	}
	if len(table.Data) == 0 {
		return 0, nil
	}
	return kdbVectorLength(table.Data[0])
}

func validateKdbAtom(value *kdb.K) error {
	valid := false
	switch value.Type {
	case -kdb.KB:
		_, valid = value.Data.(bool)
	case -kdb.UU:
		_, valid = value.Data.(uuid.UUID)
	case -kdb.KG, -kdb.KC:
		_, valid = value.Data.(byte)
	case -kdb.KH:
		_, valid = value.Data.(int16)
	case -kdb.KI:
		_, valid = value.Data.(int32)
	case -kdb.KJ:
		_, valid = value.Data.(int64)
	case -kdb.KE:
		_, valid = value.Data.(float32)
	case -kdb.KF:
		_, valid = value.Data.(float64)
	case -kdb.KS:
		_, valid = value.Data.(string)
	case -kdb.KP:
		switch value.Data.(type) {
		case time.Time, time.Duration:
			valid = true
		}
	case -kdb.KM:
		switch value.Data.(type) {
		case kdb.Month, int32:
			valid = true
		}
	case -kdb.KD:
		switch value.Data.(type) {
		case time.Time, int32:
			valid = true
		}
	case -kdb.KZ:
		switch value.Data.(type) {
		case time.Time, float64:
			valid = true
		}
	case -kdb.KN:
		_, valid = value.Data.(time.Duration)
	case -kdb.KU:
		switch value.Data.(type) {
		case kdb.Minute, int32:
			valid = true
		}
	case -kdb.KV:
		switch value.Data.(type) {
		case kdb.Second, int32:
			valid = true
		}
	case -kdb.KT:
		switch value.Data.(type) {
		case kdb.Time, int32:
			valid = true
		}
	default:
		return fmt.Errorf("unsupported kdb+ atom type %d", value.Type)
	}
	if !valid {
		return fmt.Errorf("invalid data for kdb+ atom type %d: got %T", value.Type, value.Data)
	}
	return nil
}

func kdbVectorLength(value *kdb.K) (int, error) {
	if value == nil {
		return 0, fmt.Errorf("vector is nil")
	}
	switch value.Type {
	case kdb.K0:
		return typedKdbVectorLength[*kdb.K](value, "[]*kdb.K")
	case kdb.KB:
		return typedKdbVectorLength[bool](value, "[]bool")
	case kdb.UU:
		return typedKdbVectorLength[uuid.UUID](value, "[]uuid.UUID")
	case kdb.KG:
		return typedKdbVectorLength[byte](value, "[]byte")
	case kdb.KH:
		return typedKdbVectorLength[int16](value, "[]int16")
	case kdb.KI:
		return typedKdbVectorLength[int32](value, "[]int32")
	case kdb.KJ:
		return typedKdbVectorLength[int64](value, "[]int64")
	case kdb.KE:
		return typedKdbVectorLength[float32](value, "[]float32")
	case kdb.KF:
		return typedKdbVectorLength[float64](value, "[]float64")
	case kdb.KC:
		switch values := value.Data.(type) {
		case string:
			return len(values), nil
		case []byte:
			return len(values), nil
		default:
			return 0, invalidVectorDataError(value, "string or []byte")
		}
	case kdb.KS:
		return typedKdbVectorLength[string](value, "[]string")
	case kdb.KP, kdb.KD, kdb.KZ:
		return typedKdbVectorLength[time.Time](value, "[]time.Time")
	case kdb.KM:
		return typedKdbVectorLength[kdb.Month](value, "[]kdb.Month")
	case kdb.KN:
		return typedKdbVectorLength[time.Duration](value, "[]time.Duration")
	case kdb.KU:
		return typedKdbVectorLength[kdb.Minute](value, "[]kdb.Minute")
	case kdb.KV:
		return typedKdbVectorLength[kdb.Second](value, "[]kdb.Second")
	case kdb.KT:
		return typedKdbVectorLength[kdb.Time](value, "[]kdb.Time")
	default:
		return 0, fmt.Errorf("unsupported kdb+ vector type %d", value.Type)
	}
}

func typedKdbVectorLength[T any](value *kdb.K, expected string) (int, error) {
	values, ok := value.Data.([]T)
	if !ok {
		return 0, invalidVectorDataError(value, expected)
	}
	return len(values), nil
}

func invalidVectorDataError(value *kdb.K, expected string) error {
	return fmt.Errorf("invalid data for kdb+ vector type %d: expected %s, got %T", value.Type, expected, value.Data)
}

type kdbFrameParser struct {
	budget        *kdbFrameParseBudget
	retainedRefID string
}

func newKdbFrameParser(value *kdb.K, limits kdbFrameParseLimits) (*kdbFrameParser, error) {
	budget := newKdbFrameParseBudget(limits)
	if err := validateKdbObjectWithBudget(value, budget); err != nil {
		return nil, fmt.Errorf("invalid kdb+ response: %w", err)
	}
	return &kdbFrameParser{budget: budget}, nil
}

func (p *kdbFrameParser) reserveFrame(frameName string, fieldNames []string, rowCount int) error {
	return p.budget.reserveFrame(frameName, fieldNames, rowCount, p.retainedRefID)
}

func parseKdbResponseToFramesWithLimits(kdbResponse *kdb.K, model QueryModel, refID string, limits kdbFrameParseLimits) ([]*data.Frame, error) {
	if kdbResponse == nil {
		return nil, fmt.Errorf("kdb+ returned nil response")
	}
	if err := validateRefID(refID); err != nil {
		return nil, fmt.Errorf("invalid RefID: %w", err)
	}
	parser, err := newKdbFrameParser(kdbResponse, limits)
	if err != nil {
		return nil, err
	}
	parser.retainedRefID = refID

	var frames []*data.Frame
	switch {
	case kdbResponse.Type == kdb.XT:
		frame, parseErr := parser.parseSimpleTable(kdbResponse, refID)
		if parseErr != nil {
			return nil, parseErr
		}
		frames = []*data.Frame{frame}
	case kdbResponse.Type == kdb.XD && model.CompatibilityMode == CompatibilityModePanopticon:
		dict := kdbResponse.Data.(kdb.Dict)
		var frame *data.Frame
		var parseErr error
		if dict.Key.Type == kdb.XT || dict.Value.Type == kdb.XT {
			frame, parseErr = parser.parseKeyedTable(kdbResponse, refID)
		} else {
			frame, parseErr = parser.parseDictionary(kdbResponse, refID)
		}
		if parseErr != nil {
			return nil, fmt.Errorf("unable to parse Panopticon dictionary result: %w", parseErr)
		}
		frames = []*data.Frame{frame}
	case kdbResponse.Type == kdb.XD:
		frames, err = parser.parseGroupedTable(kdbResponse, model.IncludeKeyColumns)
		if err != nil {
			return nil, err
		}
	case model.CompatibilityMode == CompatibilityModePanopticon && kdbResponse.Type == kdb.K0:
		items := kdbResponse.Data.([]*kdb.K)
		var frame *data.Frame
		if len(items) > 0 && items[0] != nil && items[0].Type == kdb.XD {
			frame, err = parser.parseDictionaryList(kdbResponse, refID)
		} else {
			frame, err = parser.parseObject(kdbResponse, refID)
		}
		if err != nil {
			return nil, fmt.Errorf("unable to parse Panopticon generic list result: %w", err)
		}
		frames = []*data.Frame{frame}
	case model.CompatibilityMode == CompatibilityModePanopticon &&
		(kdbResponse.Type < kdb.K0 || (kdbResponse.Type > kdb.K0 && kdbResponse.Type <= kdb.KT)):
		frame, parseErr := parser.parseObject(kdbResponse, refID)
		if parseErr != nil {
			return nil, fmt.Errorf("unable to parse Panopticon scalar/vector result: %w", parseErr)
		}
		frames = []*data.Frame{frame}
	default:
		return nil, fmt.Errorf("returned unsupported kdb+ object type %d for %s compatibility mode", kdbResponse.Type, model.CompatibilityMode)
	}

	for _, frame := range frames {
		frame.RefID = refID
	}
	if model.UseTimeColumn {
		for _, frame := range frames {
			if err := moveTimeColumnToFront(frame, model.TimeColumn); err != nil {
				return nil, err
			}
		}
	}
	return frames, nil
}

func ParseSimpleKdbTable(res *kdb.K) (*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid table: %w", err)
	}
	return parser.parseSimpleTable(res, "response")
}

func (p *kdbFrameParser) parseSimpleTable(res *kdb.K, frameName string) (*data.Frame, error) {
	if res.Type != kdb.XT {
		return nil, fmt.Errorf("object is not a table")
	}
	table := res.Data.(kdb.Table)
	rows, err := tableRowCount(table)
	if err != nil {
		return nil, err
	}
	if err := p.reserveFrame(frameName, table.Columns, rows); err != nil {
		return nil, err
	}

	frame := data.NewFrame(frameName)
	frame.Fields = make([]*data.Field, 0, len(table.Columns))
	for i, name := range table.Columns {
		column, err := p.convertColumn(table.Data[i], rows, columnOptions{characterVectorRows: true})
		if err != nil {
			return nil, fmt.Errorf("table column %d: %w", i, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
	}
	return frame, nil
}

func ParseKeyedKdbTableAsFrame(res *kdb.K) (*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid keyed table: %w", err)
	}
	return parser.parseKeyedTable(res, "response")
}

func (p *kdbFrameParser) parseKeyedTable(res *kdb.K, frameName string) (*data.Frame, error) {
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	dict := res.Data.(kdb.Dict)
	if dict.Key.Type != kdb.XT || dict.Value.Type != kdb.XT {
		return nil, fmt.Errorf("dictionary is not a keyed table")
	}
	keyTable := dict.Key.Data.(kdb.Table)
	valueTable := dict.Value.Data.(kdb.Table)
	keyRows, err := tableRowCount(keyTable)
	if err != nil {
		return nil, fmt.Errorf("key table row count: %w", err)
	}
	valueRows, err := tableRowCount(valueTable)
	if err != nil {
		return nil, fmt.Errorf("value table row count: %w", err)
	}
	if keyRows != valueRows {
		return nil, fmt.Errorf("key and value table row counts differ")
	}
	names, err := combinedFieldNames(keyTable.Columns, valueTable.Columns, p.budget.limits.MaxFieldsPerFrame)
	if err != nil {
		return nil, err
	}
	if err := p.reserveFrame(frameName, names, keyRows); err != nil {
		return nil, err
	}

	frame := data.NewFrame(frameName)
	frame.Fields = make([]*data.Field, 0, len(names))
	for i, name := range keyTable.Columns {
		column, err := p.convertColumn(keyTable.Data[i], keyRows, columnOptions{characterVectorRows: true})
		if err != nil {
			return nil, fmt.Errorf("key table column %d: %w", i, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
	}
	for i, name := range valueTable.Columns {
		column, err := p.convertColumn(valueTable.Data[i], valueRows, columnOptions{characterVectorRows: true})
		if err != nil {
			return nil, fmt.Errorf("value table column %d: %w", i, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
	}
	return frame, nil
}

func ParseKdbObjectAsFrame(res *kdb.K) (*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid object: %w", err)
	}
	return parser.parseObject(res, "response")
}

func (p *kdbFrameParser) parseObject(res *kdb.K, frameName string) (*data.Frame, error) {
	rows, err := kdbObjectDepthValidated(res)
	if err != nil {
		return nil, err
	}
	if err := p.reserveFrame(frameName, []string{"value"}, rows); err != nil {
		return nil, err
	}
	column, err := p.convertColumn(res, rows, columnOptions{characterVectorText: true})
	if err != nil {
		return nil, err
	}
	return data.NewFrame(frameName, data.NewField("value", nil, column)), nil
}

func ParseKdbDictAsFrame(res *kdb.K) (*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid dictionary: %w", err)
	}
	return parser.parseDictionary(res, "response")
}

func (p *kdbFrameParser) parseDictionary(res *kdb.K, frameName string) (*data.Frame, error) {
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	dict := res.Data.(kdb.Dict)
	names, err := dictColumnNamesValidated(dict.Key)
	if err != nil {
		return nil, err
	}
	values, err := dictValuesValidated(dict.Value, len(names))
	if err != nil {
		return nil, err
	}
	if len(names) != len(values) {
		return nil, fmt.Errorf("dictionary key/value lengths differ")
	}
	rows, err := dictFrameDepth(values)
	if err != nil {
		return nil, err
	}
	if err := p.reserveFrame(frameName, names, rows); err != nil {
		return nil, err
	}

	frame := data.NewFrame(frameName)
	frame.Fields = make([]*data.Field, 0, len(names))
	for i, name := range names {
		column, err := p.convertColumn(values[i], rows, columnOptions{
			broadcastSingleton:  true,
			characterVectorText: true,
		})
		if err != nil {
			return nil, fmt.Errorf("dictionary value %d: %w", i, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
	}
	return frame, nil
}

func ParseKdbDictListAsFrame(res *kdb.K) (*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid dictionary list: %w", err)
	}
	return parser.parseDictionaryList(res, "response")
}

func (p *kdbFrameParser) parseDictionaryList(res *kdb.K, frameName string) (*data.Frame, error) {
	if res.Type != kdb.K0 {
		return nil, fmt.Errorf("object is not a generic list")
	}
	rows := res.Data.([]*kdb.K)
	if len(rows) == 0 {
		return nil, fmt.Errorf("dictionary list is empty")
	}
	if uint64(len(rows)) > p.budget.limits.MaxRowsTotal {
		return nil, fmt.Errorf("kdb+ response exceeds total row limit")
	}

	columnNames := make([]string, 0)
	columnIndexes := make(map[string]int)
	metadataNameBytes, ok := checkedAddUint64(uint64(len(frameName)), uint64(len(p.retainedRefID)))
	if !ok {
		return nil, fmt.Errorf("kdb+ response name bytes overflow")
	}
	unionNameBytes := uint64(0)
	directStringBytes := uint64(0)
	for rowIndex, row := range rows {
		if row == nil || row.Type != kdb.XD {
			return nil, fmt.Errorf("item %d is not a dictionary", rowIndex)
		}
		dict := row.Data.(kdb.Dict)
		names, err := dictColumnNamesValidated(dict.Key)
		if err != nil {
			return nil, fmt.Errorf("item %d keys: %w", rowIndex, err)
		}
		for i, name := range names {
			if err := validateEmittedName(name, p.budget.limits.MaxFieldNameBytes, "field name"); err != nil {
				return nil, fmt.Errorf("item %d key %d: %w", rowIndex, i, err)
			}
		}
		values, err := dictValuesValidated(dict.Value, len(names))
		if err != nil {
			return nil, fmt.Errorf("item %d values: %w", rowIndex, err)
		}
		if len(names) != len(values) {
			return nil, fmt.Errorf("item %d key/value lengths differ", rowIndex)
		}
		for i, value := range values {
			stringBytes, directString, err := directScalarCellStringBytes(
				value,
				p.budget.limits.MaxCellStringBytes,
			)
			if err != nil {
				return nil, fmt.Errorf("item %d value %d: %w", rowIndex, i, err)
			}
			if !directString {
				continue
			}
			directStringBytes, ok = checkedAddUint64(directStringBytes, stringBytes)
			if !ok {
				return nil, fmt.Errorf("kdb+ response string size overflows")
			}
			projectedStringBytes, ok := checkedAddUint64(p.budget.stringBytes, directStringBytes)
			if !ok || projectedStringBytes > p.budget.limits.MaxStringBytes {
				return nil, fmt.Errorf("kdb+ response exceeds materialized string byte limit")
			}
		}
		rowSeen := make(map[string]struct{}, len(names))
		for _, name := range names {
			if _, exists := rowSeen[name]; exists {
				return nil, fmt.Errorf("item %d contains duplicate dictionary key", rowIndex)
			}
			rowSeen[name] = struct{}{}
			if _, exists := columnIndexes[name]; !exists {
				nextFieldCount := uint64(len(columnNames) + 1)
				if nextFieldCount > p.budget.limits.MaxFieldsPerFrame {
					return nil, fmt.Errorf("dictionary-list union exceeds fields-per-frame limit")
				}
				totalFields, ok := checkedAddUint64(p.budget.fields, nextFieldCount)
				if !ok || totalFields > p.budget.limits.MaxFieldsTotal {
					return nil, fmt.Errorf("kdb+ response exceeds total field limit")
				}
				projectedCells, ok := checkedMulUint64(uint64(len(rows)), nextFieldCount)
				if !ok {
					return nil, fmt.Errorf("kdb+ response dimensions overflow")
				}
				totalCells, ok := checkedAddUint64(p.budget.cells, projectedCells)
				if !ok || totalCells > p.budget.limits.MaxCellsTotal {
					return nil, fmt.Errorf("kdb+ response exceeds materialized cell limit")
				}
				nextUnionNameBytes, ok := checkedAddUint64(unionNameBytes, uint64(len(name)))
				if !ok {
					return nil, fmt.Errorf("kdb+ response name bytes overflow")
				}
				totalNameBytes, ok := checkedAddUint64(p.budget.nameBytes, metadataNameBytes)
				if !ok {
					return nil, fmt.Errorf("kdb+ response name bytes overflow")
				}
				totalNameBytes, ok = checkedAddUint64(totalNameBytes, nextUnionNameBytes)
				if !ok || totalNameBytes > p.budget.limits.MaxNameBytesTotal {
					return nil, fmt.Errorf("kdb+ response exceeds field-name byte limit")
				}
				columnIndexes[name] = len(columnNames)
				columnNames = append(columnNames, name)
				unionNameBytes = nextUnionNameBytes
			}
		}
	}
	if err := p.reserveFrame(frameName, columnNames, len(rows)); err != nil {
		return nil, err
	}
	// All source values that become strings are sized before any such
	// conversion. Numeric/time fallback strings remain incrementally charged
	// when their final textual representation is produced.
	if err := p.budget.chargeStringBytes(directStringBytes); err != nil {
		return nil, err
	}

	columns := make([][]interface{}, len(columnNames))
	prechargedStrings := make([][]bool, len(columnNames))
	for i := range columns {
		columns[i] = make([]interface{}, len(rows))
		prechargedStrings[i] = make([]bool, len(rows))
	}
	for rowIndex, row := range rows {
		dict := row.Data.(kdb.Dict)
		names, _ := dictColumnNamesValidated(dict.Key)
		values, _ := dictValuesValidated(dict.Value, len(names))
		for i, name := range names {
			value, prechargedString, err := scalarCellValue(values[i], p.budget.limits.MaxCellStringBytes)
			if err != nil {
				return nil, fmt.Errorf("item %d value %d: %w", rowIndex, i, err)
			}
			columnIndex := columnIndexes[name]
			columns[columnIndex][rowIndex] = value
			prechargedStrings[columnIndex][rowIndex] = prechargedString
		}
	}

	frame := data.NewFrame(frameName)
	frame.Fields = make([]*data.Field, 0, len(columnNames))
	for i, name := range columnNames {
		column, err := p.interfaceColumn(columns[i], prechargedStrings[i])
		if err != nil {
			return nil, fmt.Errorf("dictionary-list column %d: %w", i, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
	}
	return frame, nil
}

type groupedFramePlan struct {
	name   string
	rows   int
	keys   []*kdb.K
	values []*kdb.K
}

func ParseGroupedKdbTable(res *kdb.K, includeKeys bool) ([]*data.Frame, error) {
	parser, err := newKdbFrameParser(res, defaultKdbFrameParseLimits)
	if err != nil {
		return nil, fmt.Errorf("invalid grouped table: %w", err)
	}
	return parser.parseGroupedTable(res, includeKeys)
}

func (p *kdbFrameParser) parseGroupedTable(res *kdb.K, includeKeys bool) ([]*data.Frame, error) {
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	dict := res.Data.(kdb.Dict)
	if dict.Key.Type != kdb.XT || dict.Value.Type != kdb.XT {
		return nil, fmt.Errorf("grouped table key and value must both be tables")
	}
	keyTable := dict.Key.Data.(kdb.Table)
	valueTable := dict.Value.Data.(kdb.Table)
	if len(keyTable.Data) == 0 {
		return nil, fmt.Errorf("grouped table key table has no columns")
	}
	frameCount, err := tableRowCount(keyTable)
	if err != nil {
		return nil, fmt.Errorf("grouped table key rows: %w", err)
	}
	if len(valueTable.Data) == 0 {
		if frameCount == 0 {
			return []*data.Frame{}, nil
		}
		return nil, fmt.Errorf("grouped table value table has no columns")
	}
	valueRows, err := tableRowCount(valueTable)
	if err != nil {
		return nil, fmt.Errorf("grouped table value rows: %w", err)
	}
	if frameCount != valueRows {
		return nil, fmt.Errorf("grouped table key/value row counts differ: %d and %d", frameCount, valueRows)
	}

	fieldNames := valueTable.Columns
	if includeKeys {
		fieldNames, err = combinedFieldNames(keyTable.Columns, valueTable.Columns, p.budget.limits.MaxFieldsPerFrame)
		if err != nil {
			return nil, err
		}
	}
	if err := p.budget.reserveRepeatedFrames(frameCount, len(fieldNames)); err != nil {
		return nil, err
	}

	plans := make([]groupedFramePlan, frameCount)
	for row := 0; row < frameCount; row++ {
		var keys []*kdb.K
		var name string
		if includeKeys {
			keys, err = tableRowValuesValidated(keyTable, row)
			if err != nil {
				return nil, fmt.Errorf("grouped table key row %d: %w", row, err)
			}
			name, err = frameNameFromValues(keys, p.budget.limits.MaxFieldNameBytes)
		} else {
			name, err = frameNameFromTableRowValidated(keyTable, row, p.budget.limits.MaxFieldNameBytes)
		}
		if err != nil {
			return nil, fmt.Errorf("grouped table key row %d name: %w", row, err)
		}
		values, err := tableRowValuesValidated(valueTable, row)
		if err != nil {
			return nil, fmt.Errorf("grouped table value row %d: %w", row, err)
		}
		depth, err := getDepthValidated(values)
		if err != nil {
			return nil, fmt.Errorf("grouped table value row %d: %w", row, err)
		}
		if err := p.reserveFrame(name, fieldNames, depth); err != nil {
			return nil, fmt.Errorf("grouped table row %d: %w", row, err)
		}
		plans[row] = groupedFramePlan{name: name, rows: depth, keys: keys, values: values}
	}

	frames := make([]*data.Frame, frameCount)
	for row, plan := range plans {
		frame := data.NewFrame(plan.name)
		frame.Fields = make([]*data.Field, 0, len(fieldNames))
		if includeKeys {
			for i, name := range keyTable.Columns {
				column, err := p.convertColumn(plan.keys[i], plan.rows, columnOptions{
					broadcastSingleton:  true,
					characterVectorText: true,
				})
				if err != nil {
					return nil, fmt.Errorf("grouped table row %d key column %d: %w", row, i, err)
				}
				frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
			}
		}
		for i, name := range valueTable.Columns {
			value := plan.values[i]
			options := columnOptions{}
			if value.Type < kdb.K0 {
				options.broadcastSingleton = true
			}
			if value.Type == kdb.KC {
				length, _ := kdbVectorLength(value)
				if length == plan.rows {
					options.characterVectorRows = true
				} else {
					options.characterVectorText = true
					options.broadcastSingleton = true
				}
			}
			column, err := p.convertColumn(value, plan.rows, options)
			if err != nil {
				return nil, fmt.Errorf("grouped table row %d value column %d: %w", row, i, err)
			}
			frame.Fields = append(frame.Fields, data.NewField(name, nil, column))
		}
		frames[row] = frame
	}
	return frames, nil
}

func combinedFieldNames(first []string, second []string, limit uint64) ([]string, error) {
	firstCount := uint64(len(first))
	secondCount := uint64(len(second))
	total, ok := checkedAddUint64(firstCount, secondCount)
	if !ok {
		return nil, fmt.Errorf("kdb+ response field count overflows")
	}
	if total > limit {
		return nil, fmt.Errorf("kdb+ response exceeds fields-per-frame limit")
	}
	if total > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("kdb+ response field count exceeds platform capacity")
	}
	names := make([]string, 0, int(total))
	names = append(names, first...)
	names = append(names, second...)
	return names, nil
}

type columnOptions struct {
	broadcastSingleton  bool
	characterVectorRows bool
	characterVectorText bool
}

func (p *kdbFrameParser) convertColumn(value *kdb.K, rows int, options columnOptions) (interface{}, error) {
	if value == nil {
		return nil, fmt.Errorf("column is nil")
	}
	if rows < 0 {
		return nil, fmt.Errorf("column row count cannot be negative")
	}
	if value.Type < kdb.K0 {
		normalized, err := normalizedAtomValue(value)
		if err != nil {
			return nil, err
		}
		if rows != 1 && !options.broadcastSingleton {
			return nil, fmt.Errorf("atom cannot populate %d rows", rows)
		}
		return p.projectScalar(normalized, rows)
	}
	if value.Type == kdb.KC {
		if options.characterVectorText {
			text, err := boundedCharacterVectorText(value, p.budget.limits.MaxCellStringBytes)
			if err != nil {
				return nil, err
			}
			if rows != 1 && !options.broadcastSingleton {
				return nil, fmt.Errorf("character scalar cannot populate %d rows", rows)
			}
			return p.projectScalar(text, rows)
		}
		if !options.characterVectorRows {
			return nil, fmt.Errorf("character-vector interpretation is ambiguous")
		}
		return p.convertCharacterVectorRows(value, rows)
	}

	length, err := kdbVectorLength(value)
	if err != nil {
		return nil, err
	}
	if length != rows {
		if !(options.broadcastSingleton && length == 1) {
			return nil, fmt.Errorf("column length %d does not match frame rows %d", length, rows)
		}
		item, ok := correctedIndexValidated(value, 0)
		if !ok {
			return nil, fmt.Errorf("singleton column could not be indexed")
		}
		if item.Type == kdb.KC {
			text, err := boundedCharacterVectorText(item, p.budget.limits.MaxCellStringBytes)
			if err != nil {
				return nil, err
			}
			return p.projectScalar(text, rows)
		}
		if item.Type >= kdb.K0 {
			return nil, fmt.Errorf("nested singleton column is not a scalar")
		}
		normalized, err := normalizedAtomValue(item)
		if err != nil {
			return nil, err
		}
		return p.projectScalar(normalized, rows)
	}

	switch value.Type {
	case kdb.K0:
		items := value.Data.([]*kdb.K)
		out := make([]string, len(items))
		for i, item := range items {
			text, err := p.scalarKdbText(item)
			if err != nil {
				return nil, fmt.Errorf("generic list item %d: %w", i, err)
			}
			if err := p.budget.chargeString(text, 1); err != nil {
				return nil, err
			}
			out[i] = text
		}
		return out, nil
	case kdb.KB:
		return cloneKdbColumn(value.Data.([]bool)), nil
	case kdb.UU:
		values := value.Data.([]uuid.UUID)
		if err := p.budget.chargeString(strings.Repeat("x", 36), len(values)); err != nil {
			return nil, err
		}
		out := make([]string, len(values))
		for i, entry := range values {
			out[i] = entry.String()
		}
		return out, nil
	case kdb.KG:
		return cloneKdbColumn(value.Data.([]byte)), nil
	case kdb.KH:
		return cloneKdbColumn(value.Data.([]int16)), nil
	case kdb.KI:
		return cloneKdbColumn(value.Data.([]int32)), nil
	case kdb.KJ:
		return cloneKdbColumn(value.Data.([]int64)), nil
	case kdb.KE:
		return cloneKdbColumn(value.Data.([]float32)), nil
	case kdb.KF:
		return cloneKdbColumn(value.Data.([]float64)), nil
	case kdb.KS:
		values := value.Data.([]string)
		for _, text := range values {
			if err := p.budget.chargeString(text, 1); err != nil {
				return nil, err
			}
		}
		return cloneKdbColumn(values), nil
	case kdb.KP, kdb.KD, kdb.KZ:
		return cloneKdbColumn(value.Data.([]time.Time)), nil
	case kdb.KM:
		values := value.Data.([]kdb.Month)
		out := make([]int32, len(values))
		for i, entry := range values {
			out[i] = int32(entry)
		}
		return out, nil
	case kdb.KN:
		values := value.Data.([]time.Duration)
		out := make([]int64, len(values))
		for i, entry := range values {
			out[i] = int64(entry)
		}
		return out, nil
	case kdb.KU:
		values := value.Data.([]kdb.Minute)
		out := make([]int32, len(values))
		for i, entry := range values {
			out[i] = int32(time.Time(entry).Sub(time.Time{}) / time.Minute)
		}
		return out, nil
	case kdb.KV:
		values := value.Data.([]kdb.Second)
		out := make([]int32, len(values))
		for i, entry := range values {
			t := time.Time(entry)
			out[i] = int32(t.Second() + t.Minute()*60 + t.Hour()*3600)
		}
		return out, nil
	case kdb.KT:
		values := value.Data.([]kdb.Time)
		out := make([]int32, len(values))
		for i, entry := range values {
			t := time.Time(entry)
			out[i] = int32(t.Hour()*3_600_000 + t.Minute()*60_000 + t.Second()*1_000 + t.Nanosecond()/1_000_000)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported column type %d", value.Type)
	}
}

func cloneKdbColumn[T any](values []T) []T {
	return append([]T(nil), values...)
}

func (p *kdbFrameParser) convertCharacterVectorRows(value *kdb.K, rows int) ([]string, error) {
	if value == nil || value.Type != kdb.KC {
		return nil, fmt.Errorf("object is not a character vector")
	}
	length, err := kdbVectorLength(value)
	if err != nil {
		return nil, err
	}
	if length != rows {
		return nil, fmt.Errorf("character vector length does not match frame rows")
	}

	highBytes := 0
	switch raw := value.Data.(type) {
	case string:
		for i := range raw {
			if raw[i] >= utf8.RuneSelf {
				highBytes++
			}
		}
	case []byte:
		for _, value := range raw {
			if value >= utf8.RuneSelf {
				highBytes++
			}
		}
	default:
		return nil, invalidVectorDataError(value, "string or []byte")
	}
	emittedBytes, ok := checkedAddUint64(uint64(length), uint64(highBytes))
	if !ok {
		return nil, fmt.Errorf("kdb+ response string size overflows")
	}
	if highBytes > 0 && p.budget.limits.MaxCellStringBytes < 2 {
		return nil, fmt.Errorf("kdb+ response string exceeds per-value byte limit")
	}
	if err := p.budget.chargeStringBytes(emittedBytes); err != nil {
		return nil, err
	}

	out := make([]string, length)
	switch raw := value.Data.(type) {
	case string:
		for i := range raw {
			out[i] = string(rune(raw[i]))
		}
	case []byte:
		for i, value := range raw {
			out[i] = string(rune(value))
		}
	}
	return out, nil
}

func (p *kdbFrameParser) scalarKdbText(value *kdb.K) (string, error) {
	if value != nil && value.Type == kdb.KC {
		return boundedCharacterVectorText(value, p.budget.limits.MaxCellStringBytes)
	}
	return scalarKdbText(value)
}

func (p *kdbFrameParser) projectScalar(value interface{}, rows int) (interface{}, error) {
	if rows < 0 {
		return nil, fmt.Errorf("projection row count cannot be negative")
	}
	switch value := value.(type) {
	case bool:
		return repeatedColumn(value, rows), nil
	case byte:
		return repeatedColumn(value, rows), nil
	case int16:
		return repeatedColumn(value, rows), nil
	case int32:
		return repeatedColumn(value, rows), nil
	case int64:
		return repeatedColumn(value, rows), nil
	case float32:
		return repeatedColumn(value, rows), nil
	case float64:
		return repeatedColumn(value, rows), nil
	case string:
		if err := p.budget.chargeString(value, rows); err != nil {
			return nil, err
		}
		return repeatedColumn(value, rows), nil
	case time.Time:
		return repeatedColumn(value, rows), nil
	default:
		return nil, fmt.Errorf("unsupported projected scalar type %T", value)
	}
}

func repeatedColumn[T any](value T, rows int) []T {
	out := make([]T, rows)
	for i := range out {
		out[i] = value
	}
	return out
}

func normalizedAtomValue(value *kdb.K) (interface{}, error) {
	if value == nil || value.Type >= kdb.K0 {
		return nil, fmt.Errorf("object is not a kdb+ atom")
	}
	if err := validateKdbAtom(value); err != nil {
		return nil, err
	}
	switch value.Type {
	case -kdb.KC:
		return string(value.Data.(byte)), nil
	case -kdb.UU:
		entry := value.Data.(uuid.UUID)
		return entry.String(), nil
	case -kdb.KP:
		switch entry := value.Data.(type) {
		case time.Time:
			return entry, nil
		case time.Duration:
			return int64(entry), nil
		}
	case -kdb.KM:
		switch entry := value.Data.(type) {
		case kdb.Month:
			return int32(entry), nil
		case int32:
			return entry, nil
		}
	case -kdb.KD:
		switch entry := value.Data.(type) {
		case time.Time:
			return entry, nil
		case int32:
			return entry, nil
		}
	case -kdb.KZ:
		switch entry := value.Data.(type) {
		case time.Time:
			return entry, nil
		case float64:
			return entry, nil
		}
	case -kdb.KN:
		return int64(value.Data.(time.Duration)), nil
	case -kdb.KU:
		switch entry := value.Data.(type) {
		case kdb.Minute:
			return int32(time.Time(entry).Sub(time.Time{}) / time.Minute), nil
		case int32:
			return entry, nil
		}
	case -kdb.KV:
		switch entry := value.Data.(type) {
		case kdb.Second:
			t := time.Time(entry)
			return int32(t.Second() + t.Minute()*60 + t.Hour()*3600), nil
		case int32:
			return entry, nil
		}
	case -kdb.KT:
		switch entry := value.Data.(type) {
		case kdb.Time:
			t := time.Time(entry)
			return int32(t.Hour()*3_600_000 + t.Minute()*60_000 + t.Second()*1_000 + t.Nanosecond()/1_000_000), nil
		case int32:
			return entry, nil
		}
	}
	return value.Data, nil
}

func scalarKdbText(value *kdb.K) (string, error) {
	if value == nil {
		return "", fmt.Errorf("value is nil")
	}
	if value.Type == kdb.KC {
		return boundedCharacterVectorText(value, maxKdbCellStringBytes)
	}
	if value.Type >= kdb.K0 {
		return "", fmt.Errorf("nested or complex value type %d is not supported", value.Type)
	}
	normalized, err := normalizedAtomValue(value)
	if err != nil {
		return "", err
	}
	return scalarValueText(normalized)
}

func scalarValueText(value interface{}) (string, error) {
	switch value := value.(type) {
	case bool:
		return strconv.FormatBool(value), nil
	case byte:
		return strconv.FormatUint(uint64(value), 10), nil
	case int16:
		return strconv.FormatInt(int64(value), 10), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float32:
		return strconv.FormatFloat(float64(value), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case string:
		return value, nil
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano), nil
	default:
		return "", fmt.Errorf("unsupported scalar value type %T", value)
	}
}

func characterVectorText(value *kdb.K) (string, error) {
	return boundedCharacterVectorText(value, maxKdbCellStringBytes)
}

func boundedCharacterVectorText(value *kdb.K, maximum uint64) (string, error) {
	if value == nil || value.Type != kdb.KC {
		return "", fmt.Errorf("object is not a character vector")
	}
	length, err := kdbVectorLength(value)
	if err != nil {
		return "", err
	}
	if uint64(length) > maximum {
		return "", fmt.Errorf("character vector exceeds per-value byte limit")
	}
	switch text := value.Data.(type) {
	case string:
		if !utf8.ValidString(text) {
			return "", fmt.Errorf("character vector text is not valid UTF-8")
		}
		return text, nil
	case []byte:
		if !utf8.Valid(text) {
			return "", fmt.Errorf("character vector text is not valid UTF-8")
		}
		return string(text), nil
	default:
		return "", invalidVectorDataError(value, "string or []byte")
	}
}

func kdbObjectDepthValidated(value *kdb.K) (int, error) {
	if value == nil {
		return 0, fmt.Errorf("value is nil")
	}
	if value.Type < kdb.K0 || value.Type == kdb.KC {
		return 1, nil
	}
	return kdbVectorLength(value)
}

func dictColumnNames(keys *kdb.K) ([]string, error) {
	if err := validateKdbObject(keys); err != nil {
		return nil, fmt.Errorf("invalid dictionary keys: %w", err)
	}
	return dictColumnNamesValidated(keys)
}

func dictColumnNamesValidated(keys *kdb.K) ([]string, error) {
	if keys == nil {
		return nil, fmt.Errorf("dictionary keys are nil")
	}
	switch keys.Type {
	case -kdb.KS:
		return []string{keys.Data.(string)}, nil
	case kdb.KS:
		names := keys.Data.([]string)
		if len(names) > maxKdbFieldsPerFrame {
			return nil, fmt.Errorf("dictionary key count exceeds field limit")
		}
		return names, nil
	case kdb.KC:
		length, err := kdbVectorLength(keys)
		if err != nil {
			return nil, err
		}
		if length > maxKdbFieldNameBytes {
			return nil, fmt.Errorf("dictionary field name exceeds byte limit")
		}
		text, err := characterVectorText(keys)
		if err != nil {
			return nil, err
		}
		return []string{text}, nil
	case kdb.K0:
		items := keys.Data.([]*kdb.K)
		if len(items) > maxKdbFieldsPerFrame {
			return nil, fmt.Errorf("dictionary key count exceeds field limit")
		}
		names := make([]string, len(items))
		for i, item := range items {
			if item != nil && item.Type == kdb.KC {
				length, err := kdbVectorLength(item)
				if err != nil {
					return nil, fmt.Errorf("dictionary key %d: %w", i, err)
				}
				if length > maxKdbFieldNameBytes {
					return nil, fmt.Errorf("dictionary key %d exceeds field-name byte limit", i)
				}
			}
			name, err := scalarKdbText(item)
			if err != nil {
				return nil, fmt.Errorf("dictionary key %d: %w", i, err)
			}
			names[i] = name
		}
		return names, nil
	default:
		return nil, fmt.Errorf("unsupported dictionary key type %d", keys.Type)
	}
}

func dictValues(values *kdb.K, keyCount int) ([]*kdb.K, error) {
	if err := validateKdbObject(values); err != nil {
		return nil, fmt.Errorf("invalid dictionary values: %w", err)
	}
	return dictValuesValidated(values, keyCount)
}

func dictValuesValidated(values *kdb.K, keyCount int) ([]*kdb.K, error) {
	if values == nil {
		return nil, fmt.Errorf("dictionary values are nil")
	}
	if keyCount < 0 || keyCount > maxKdbFieldsPerFrame {
		return nil, fmt.Errorf("dictionary key count exceeds field limit")
	}
	if list, ok := values.Data.([]*kdb.K); ok && values.Type == kdb.K0 {
		return list, nil
	}
	if keyCount == 1 {
		return []*kdb.K{values}, nil
	}
	if values.Type > kdb.K0 && values.Type <= kdb.KT {
		length, err := kdbVectorLength(values)
		if err != nil {
			return nil, err
		}
		if length != keyCount {
			return nil, fmt.Errorf("dictionary values are not compatible with %d keys", keyCount)
		}
		out := make([]*kdb.K, keyCount)
		for i := range out {
			item, ok := correctedIndexValidated(values, i)
			if !ok {
				return nil, fmt.Errorf("dictionary value at index %d could not be indexed", i)
			}
			out[i] = item
		}
		return out, nil
	}
	return nil, fmt.Errorf("dictionary values are not compatible with %d keys", keyCount)
}

func dictFrameDepth(values []*kdb.K) (int, error) {
	depth := 1
	targetLength := -1
	hasSingleton := false
	for i, value := range values {
		length := 1
		if value != nil && value.Type >= kdb.K0 && value.Type != kdb.KC {
			var err error
			length, err = kdbVectorLength(value)
			if err != nil {
				return 0, fmt.Errorf("dictionary value %d: %w", i, err)
			}
		}
		if length > depth {
			depth = length
		}
		if length == 1 {
			hasSingleton = true
			continue
		}
		if targetLength == -1 {
			targetLength = length
		} else if targetLength != length {
			return 0, fmt.Errorf("dictionary values have incompatible lengths at index %d: %d and %d", i, length, targetLength)
		}
	}
	if targetLength == 0 && hasSingleton {
		return 0, fmt.Errorf("dictionary values mix empty and singleton columns")
	}
	return depth, nil
}

func directScalarCellStringBytes(value *kdb.K, maxStringBytes uint64) (uint64, bool, error) {
	if value == nil {
		return 0, false, fmt.Errorf("value is nil")
	}
	switch value.Type {
	case -kdb.KS:
		text := value.Data.(string)
		if !utf8.ValidString(text) {
			return 0, false, fmt.Errorf("kdb+ response contains invalid UTF-8 string data")
		}
		return checkedDirectStringLength(uint64(len(text)), maxStringBytes)
	case -kdb.UU:
		return checkedDirectStringLength(kdbUUIDTextBytes, maxStringBytes)
	case -kdb.KC:
		length := uint64(1)
		if value.Data.(byte) >= utf8.RuneSelf {
			length = 2
		}
		return checkedDirectStringLength(length, maxStringBytes)
	}
	if value.Type == kdb.KC {
		length, err := kdbVectorLength(value)
		if err != nil {
			return 0, false, err
		}
		if uint64(length) > maxStringBytes {
			return 0, false, fmt.Errorf("character vector exceeds per-value byte limit")
		}
		switch text := value.Data.(type) {
		case string:
			if !utf8.ValidString(text) {
				return 0, false, fmt.Errorf("character vector text is not valid UTF-8")
			}
		case []byte:
			if !utf8.Valid(text) {
				return 0, false, fmt.Errorf("character vector text is not valid UTF-8")
			}
		default:
			return 0, false, invalidVectorDataError(value, "string or []byte")
		}
		return uint64(length), true, nil
	}
	if value.Type > kdb.K0 && value.Type <= kdb.KT {
		length, err := kdbVectorLength(value)
		if err != nil {
			return 0, false, err
		}
		if length != 1 {
			return 0, false, fmt.Errorf("nested vector cell must contain exactly one scalar")
		}
		switch value.Type {
		case kdb.KS:
			text := value.Data.([]string)[0]
			if !utf8.ValidString(text) {
				return 0, false, fmt.Errorf("kdb+ response contains invalid UTF-8 string data")
			}
			return checkedDirectStringLength(uint64(len(text)), maxStringBytes)
		case kdb.UU:
			return checkedDirectStringLength(kdbUUIDTextBytes, maxStringBytes)
		}
		return 0, false, nil
	}
	return 0, false, nil
}

func checkedDirectStringLength(length uint64, maximum uint64) (uint64, bool, error) {
	if length > maximum {
		return 0, false, fmt.Errorf("kdb+ response string exceeds per-value byte limit")
	}
	return length, true, nil
}

func scalarCellValue(value *kdb.K, maxStringBytes uint64) (interface{}, bool, error) {
	if value == nil {
		return nil, false, fmt.Errorf("value is nil")
	}
	if value.Type < kdb.K0 {
		normalized, err := normalizedAtomValue(value)
		return normalized, isDirectStringAtom(value.Type), err
	}
	if value.Type == kdb.KC {
		text, err := boundedCharacterVectorText(value, maxStringBytes)
		return text, true, err
	}
	if value.Type > kdb.K0 && value.Type <= kdb.KT {
		length, err := kdbVectorLength(value)
		if err != nil {
			return nil, false, err
		}
		if length != 1 {
			return nil, false, fmt.Errorf("nested vector cell must contain exactly one scalar")
		}
		item, ok := correctedIndexValidated(value, 0)
		if !ok {
			return nil, false, fmt.Errorf("nested vector cell could not be indexed")
		}
		normalized, err := normalizedAtomValue(item)
		return normalized, isDirectStringAtom(item.Type), err
	}
	return nil, false, fmt.Errorf("nested or complex cell type %d is not supported", value.Type)
}

func isDirectStringAtom(valueType int8) bool {
	return valueType == -kdb.KS || valueType == -kdb.UU || valueType == -kdb.KC
}

func (p *kdbFrameParser) interfaceColumn(values []interface{}, prechargedStrings []bool) (interface{}, error) {
	if err := validatePrechargedStrings(values, prechargedStrings); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return []string{}, nil
	}
	if hasNilValue(values) {
		return p.nullableInterfaceColumn(values, prechargedStrings)
	}
	if converted, ok := mixedNumericColumn(values); ok {
		return converted, nil
	}
	switch values[0].(type) {
	case string:
		out, ok := typedColumn[string](values)
		if ok {
			for i, value := range out {
				if prechargedStrings[i] {
					continue
				}
				if err := p.budget.chargeString(value, 1); err != nil {
					return nil, err
				}
			}
			return out, nil
		}
	case bool:
		if out, ok := typedColumn[bool](values); ok {
			return out, nil
		}
	case byte:
		if out, ok := typedColumn[byte](values); ok {
			return out, nil
		}
	case int16:
		if out, ok := typedColumn[int16](values); ok {
			return out, nil
		}
	case int32:
		if out, ok := typedColumn[int32](values); ok {
			return out, nil
		}
	case int64:
		if out, ok := typedColumn[int64](values); ok {
			return out, nil
		}
	case float32:
		if out, ok := typedColumn[float32](values); ok {
			return out, nil
		}
	case float64:
		if out, ok := typedColumn[float64](values); ok {
			return out, nil
		}
	case time.Time:
		if out, ok := typedColumn[time.Time](values); ok {
			return out, nil
		}
	}
	return p.stringInterfaceColumn(values, prechargedStrings)
}

func validatePrechargedStrings(values []interface{}, prechargedStrings []bool) error {
	if len(values) != len(prechargedStrings) {
		return fmt.Errorf("precharged string metadata length differs from column")
	}
	for i, precharged := range prechargedStrings {
		if !precharged {
			continue
		}
		if _, ok := values[i].(string); !ok {
			return fmt.Errorf("precharged string metadata marks non-string cell %d", i)
		}
	}
	return nil
}

func (p *kdbFrameParser) nullableInterfaceColumn(values []interface{}, prechargedStrings []bool) (interface{}, error) {
	if converted, ok := nullableMixedNumericColumn(values); ok {
		if err := p.budget.chargeNullablePointers(nonNilCount(values)); err != nil {
			return nil, err
		}
		return converted, nil
	}
	for _, value := range values {
		if value == nil {
			continue
		}
		switch value.(type) {
		case string:
			if out, ok := nullableTypedColumn[string](values); ok {
				if err := p.budget.chargeNullablePointers(nonNilCount(values)); err != nil {
					return nil, err
				}
				for i, entry := range out {
					if entry != nil && !prechargedStrings[i] {
						if err := p.budget.chargeString(*entry, 1); err != nil {
							return nil, err
						}
					}
				}
				return out, nil
			}
		case bool:
			if out, ok := nullableTypedColumn[bool](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case byte:
			if out, ok := nullableTypedColumn[byte](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case int16:
			if out, ok := nullableTypedColumn[int16](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case int32:
			if out, ok := nullableTypedColumn[int32](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case int64:
			if out, ok := nullableTypedColumn[int64](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case float32:
			if out, ok := nullableTypedColumn[float32](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case float64:
			if out, ok := nullableTypedColumn[float64](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		case time.Time:
			if out, ok := nullableTypedColumn[time.Time](values); ok {
				return p.chargeNullableColumn(values, out)
			}
		}
		break
	}
	return p.nullableStringColumn(values, prechargedStrings)
}

func (p *kdbFrameParser) chargeNullableColumn(source []interface{}, column interface{}) (interface{}, error) {
	if err := p.budget.chargeNullablePointers(nonNilCount(source)); err != nil {
		return nil, err
	}
	return column, nil
}

func (p *kdbFrameParser) stringInterfaceColumn(values []interface{}, prechargedStrings []bool) ([]string, error) {
	out := make([]string, len(values))
	for i, value := range values {
		text, err := scalarValueText(value)
		if err != nil {
			return nil, fmt.Errorf("cell %d: %w", i, err)
		}
		if !prechargedStrings[i] {
			if err := p.budget.chargeString(text, 1); err != nil {
				return nil, err
			}
		}
		out[i] = text
	}
	return out, nil
}

func (p *kdbFrameParser) nullableStringColumn(values []interface{}, prechargedStrings []bool) ([]*string, error) {
	if err := p.budget.chargeNullablePointers(nonNilCount(values)); err != nil {
		return nil, err
	}
	out := make([]*string, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		text, err := scalarValueText(value)
		if err != nil {
			return nil, fmt.Errorf("cell %d: %w", i, err)
		}
		if !prechargedStrings[i] {
			if err := p.budget.chargeString(text, 1); err != nil {
				return nil, err
			}
		}
		out[i] = &text
	}
	return out, nil
}

func typedColumn[T any](values []interface{}) ([]T, bool) {
	for _, value := range values {
		if _, ok := value.(T); !ok {
			return nil, false
		}
	}
	out := make([]T, len(values))
	for i, value := range values {
		typed, ok := value.(T)
		if !ok {
			return nil, false
		}
		out[i] = typed
	}
	return out, true
}

func nullableTypedColumn[T any](values []interface{}) ([]*T, bool) {
	for _, value := range values {
		if value == nil {
			continue
		}
		if _, ok := value.(T); !ok {
			return nil, false
		}
	}
	out := make([]*T, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		typed := value.(T)
		out[i] = &typed
	}
	return out, true
}

func hasNilValue(values []interface{}) bool {
	for _, value := range values {
		if value == nil {
			return true
		}
	}
	return false
}

func nonNilCount(values []interface{}) int {
	count := 0
	for _, value := range values {
		if value != nil {
			count++
		}
	}
	return count
}

func mixedNumericColumn(values []interface{}) ([]float64, bool) {
	firstKind := ""
	mixed := false
	for _, value := range values {
		_, kind, ok := numericCellValue(value)
		if !ok {
			return nil, false
		}
		if firstKind == "" {
			firstKind = kind
		} else if firstKind != kind {
			mixed = true
		}
	}
	if !mixed {
		return nil, false
	}
	out := make([]float64, len(values))
	for i, value := range values {
		out[i], _, _ = numericCellValue(value)
	}
	return out, true
}

func nullableMixedNumericColumn(values []interface{}) ([]*float64, bool) {
	firstKind := ""
	mixed := false
	seen := false
	for _, value := range values {
		if value == nil {
			continue
		}
		_, kind, ok := numericCellValue(value)
		if !ok {
			return nil, false
		}
		if !seen {
			firstKind = kind
			seen = true
		} else if firstKind != kind {
			mixed = true
		}
	}
	if !seen || !mixed {
		return nil, false
	}
	out := make([]*float64, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		converted, _, _ := numericCellValue(value)
		out[i] = &converted
	}
	return out, true
}

func numericCellValue(value interface{}) (float64, string, bool) {
	switch value := value.(type) {
	case byte:
		return float64(value), "byte", true
	case int16:
		return float64(value), "int16", true
	case int32:
		return float64(value), "int32", true
	case int64:
		const maxExactFloat64Integer = int64(1 << 53)
		if value < -maxExactFloat64Integer || value > maxExactFloat64Integer {
			return 0, "", false
		}
		return float64(value), "int64", true
	case float32:
		return float64(value), "float32", true
	case float64:
		return value, "float64", true
	default:
		return 0, "", false
	}
}

func getDepth(colArray []*kdb.K) (int, error) {
	if len(colArray) > maxKdbFieldsPerFrame {
		return 0, fmt.Errorf("grouped column count exceeds field limit")
	}
	container := &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: colArray}
	if err := validateKdbObject(container); err != nil {
		return 0, fmt.Errorf("invalid grouped columns: %w", err)
	}
	return getDepthValidated(colArray)
}

func getDepthValidated(colArray []*kdb.K) (int, error) {
	depth := -1
	aggregatePresent := false
	for i, value := range colArray {
		if value == nil {
			return 0, fmt.Errorf("column %d is nil", i)
		}
		if value.Type < kdb.K0 {
			aggregatePresent = true
			continue
		}
		if value.Type == kdb.KC {
			continue
		}
		if value.Type > kdb.KT {
			return 0, fmt.Errorf("column %d has unsupported grouped type %d", i, value.Type)
		}
		length, err := kdbVectorLength(value)
		if err != nil {
			return 0, fmt.Errorf("column %d: %w", i, err)
		}
		if depth == -1 {
			depth = length
		} else if depth != length {
			return 0, fmt.Errorf("columns have unequal lengths: %d and %d", depth, length)
		}
	}
	if depth == -1 {
		if aggregatePresent {
			return 1, nil
		}
		return 0, fmt.Errorf("all column values are character vectors or the column list is empty")
	}
	return depth, nil
}

func parseFrameName(key *kdb.K) (string, error) {
	if err := validateKdbObject(key); err != nil {
		return "", err
	}
	return frameNameFromKdbValue(key, defaultKdbFrameParseLimits.MaxFieldNameBytes)
}

func frameNameFromKdbValue(key *kdb.K, maxBytes uint64) (string, error) {
	if key == nil {
		return "", fmt.Errorf("frame-name key is nil")
	}
	if key.Type == kdb.K0 {
		return frameNameFromValues(key.Data.([]*kdb.K), maxBytes)
	}
	if key.Type < kdb.K0 || key.Type == kdb.KC {
		if key.Type == kdb.KC {
			length, err := kdbVectorLength(key)
			if err != nil {
				return "", err
			}
			if uint64(length) > maxBytes {
				return "", fmt.Errorf("frame name exceeds byte limit")
			}
		}
		text, err := scalarKdbText(key)
		if err != nil {
			return "", err
		}
		if err := validateEmittedName(text, maxBytes, "frame name"); err != nil {
			return "", err
		}
		return text, nil
	}
	if key.Type > kdb.K0 && key.Type <= kdb.KT {
		length, err := kdbVectorLength(key)
		if err != nil {
			return "", err
		}
		if length == 0 {
			return "", fmt.Errorf("frame name has no key values")
		}
		var builder strings.Builder
		for i := 0; i < length; i++ {
			item, ok := correctedIndexValidated(key, i)
			if !ok {
				return "", fmt.Errorf("frame-name item %d could not be indexed", i)
			}
			text, err := scalarKdbText(item)
			if err != nil {
				return "", fmt.Errorf("frame-name item %d: %w", i, err)
			}
			added := len(text)
			if i > 0 {
				added += len(" - ")
			}
			if uint64(builder.Len()+added) > maxBytes {
				return "", fmt.Errorf("frame name exceeds byte limit")
			}
			if i > 0 {
				builder.WriteString(" - ")
			}
			builder.WriteString(text)
		}
		name := builder.String()
		if err := validateEmittedName(name, maxBytes, "frame name"); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("unsupported frame-name type %d", key.Type)
}

func frameNameFromValues(values []*kdb.K, maxBytes uint64) (string, error) {
	if len(values) == 0 {
		return "", fmt.Errorf("frame name has no key values")
	}
	var builder strings.Builder
	for i, value := range values {
		if value != nil && value.Type == kdb.KC {
			length, err := kdbVectorLength(value)
			if err != nil {
				return "", fmt.Errorf("key item %d: %w", i, err)
			}
			if uint64(length) > maxBytes {
				return "", fmt.Errorf("frame name exceeds byte limit")
			}
		}
		text, err := scalarKdbText(value)
		if err != nil {
			return "", fmt.Errorf("key item %d: %w", i, err)
		}
		added := len(text)
		if i > 0 {
			added += len(" - ")
		}
		if uint64(builder.Len()+added) > maxBytes {
			return "", fmt.Errorf("frame name exceeds byte limit")
		}
		if i > 0 {
			builder.WriteString(" - ")
		}
		builder.WriteString(text)
	}
	name := builder.String()
	if err := validateEmittedName(name, maxBytes, "frame name"); err != nil {
		return "", err
	}
	return name, nil
}

func frameNameFromTableRowValidated(table kdb.Table, row int, maxBytes uint64) (string, error) {
	if len(table.Columns) == 0 || len(table.Columns) != len(table.Data) {
		return "", fmt.Errorf("frame name has no valid key columns")
	}
	var builder strings.Builder
	estimatedBytes, ok := checkedMulUint64(uint64(len(table.Data)), uint64(len(" - ")+1))
	if ok {
		if estimatedBytes > maxBytes {
			estimatedBytes = maxBytes
		}
		if estimatedBytes <= uint64(^uint(0)>>1) {
			builder.Grow(int(estimatedBytes))
		}
	}
	for i, column := range table.Data {
		text, err := scalarKdbVectorTextAt(column, row, maxBytes)
		if err != nil {
			return "", fmt.Errorf("key item %d: %w", i, err)
		}
		added := len(text)
		if i > 0 {
			added += len(" - ")
		}
		if uint64(builder.Len()+added) > maxBytes {
			return "", fmt.Errorf("frame name exceeds byte limit")
		}
		if i > 0 {
			builder.WriteString(" - ")
		}
		builder.WriteString(text)
	}
	name := builder.String()
	if err := validateEmittedName(name, maxBytes, "frame name"); err != nil {
		return "", err
	}
	return name, nil
}

func scalarKdbVectorTextAt(value *kdb.K, index int, maxBytes uint64) (string, error) {
	if value == nil || index < 0 {
		return "", fmt.Errorf("value could not be indexed")
	}
	length, err := kdbVectorLength(value)
	if err != nil {
		return "", err
	}
	if index >= length {
		return "", fmt.Errorf("value could not be indexed")
	}

	switch value.Type {
	case kdb.K0:
		item := value.Data.([]*kdb.K)[index]
		if item == nil {
			return "", fmt.Errorf("value is nil")
		}
		if item.Type == kdb.KC {
			return boundedCharacterVectorText(item, maxBytes)
		}
		if item.Type >= kdb.K0 {
			return "", fmt.Errorf("nested or complex value type %d is not supported", item.Type)
		}
		normalized, err := normalizedAtomValue(item)
		if err != nil {
			return "", err
		}
		return scalarValueText(normalized)
	case kdb.KB:
		return strconv.FormatBool(value.Data.([]bool)[index]), nil
	case kdb.UU:
		return value.Data.([]uuid.UUID)[index].String(), nil
	case kdb.KG:
		return strconv.FormatUint(uint64(value.Data.([]byte)[index]), 10), nil
	case kdb.KH:
		return strconv.FormatInt(int64(value.Data.([]int16)[index]), 10), nil
	case kdb.KI:
		return strconv.FormatInt(int64(value.Data.([]int32)[index]), 10), nil
	case kdb.KJ:
		return strconv.FormatInt(value.Data.([]int64)[index], 10), nil
	case kdb.KE:
		return strconv.FormatFloat(float64(value.Data.([]float32)[index]), 'g', -1, 32), nil
	case kdb.KF:
		return strconv.FormatFloat(value.Data.([]float64)[index], 'g', -1, 64), nil
	case kdb.KC:
		switch raw := value.Data.(type) {
		case string:
			return string(rune(raw[index])), nil
		case []byte:
			return string(rune(raw[index])), nil
		default:
			return "", invalidVectorDataError(value, "string or []byte")
		}
	case kdb.KS:
		return value.Data.([]string)[index], nil
	case kdb.KP, kdb.KD, kdb.KZ:
		return value.Data.([]time.Time)[index].UTC().Format(time.RFC3339Nano), nil
	case kdb.KM:
		return strconv.FormatInt(int64(int32(value.Data.([]kdb.Month)[index])), 10), nil
	case kdb.KN:
		return strconv.FormatInt(int64(value.Data.([]time.Duration)[index]), 10), nil
	case kdb.KU:
		entry := time.Time(value.Data.([]kdb.Minute)[index])
		return strconv.FormatInt(int64(int32(entry.Sub(time.Time{})/time.Minute)), 10), nil
	case kdb.KV:
		entry := time.Time(value.Data.([]kdb.Second)[index])
		return strconv.FormatInt(int64(int32(entry.Second()+entry.Minute()*60+entry.Hour()*3600)), 10), nil
	case kdb.KT:
		entry := time.Time(value.Data.([]kdb.Time)[index])
		milliseconds := entry.Hour()*3_600_000 +
			entry.Minute()*60_000 +
			entry.Second()*1_000 +
			entry.Nanosecond()/1_000_000
		return strconv.FormatInt(int64(int32(milliseconds)), 10), nil
	default:
		return "", fmt.Errorf("unsupported frame-name vector type %d", value.Type)
	}
}

func tableRowValuesValidated(table kdb.Table, row int) ([]*kdb.K, error) {
	if len(table.Columns) != len(table.Data) {
		return nil, fmt.Errorf("table column name/data counts differ")
	}
	values := make([]*kdb.K, len(table.Data))
	for i, column := range table.Data {
		item, ok := correctedIndexValidated(column, row)
		if !ok {
			return nil, fmt.Errorf("column %d could not be indexed", i)
		}
		values[i] = item
	}
	return values, nil
}

func correctedIndex(value *kdb.K, index int) interface{} {
	if value == nil || index < 0 {
		return nil
	}
	if value.Type == kdb.XT {
		table, ok := value.Data.(kdb.Table)
		if !ok {
			return nil
		}
		row, err := correctedTableIndexSafe(table, index)
		if err != nil {
			return nil
		}
		return &kdb.K{Type: kdb.XD, Attr: kdb.NONE, Data: row}
	}
	item, ok := correctedIndexValidated(value, index)
	if !ok {
		return nil
	}
	return item
}

func correctedIndexValidated(value *kdb.K, index int) (*kdb.K, bool) {
	if value == nil || index < 0 || value.Type < kdb.K0 || value.Type > kdb.KT {
		return nil, false
	}
	length, err := kdbVectorLength(value)
	if err != nil || index >= length {
		return nil, false
	}
	if value.Type == kdb.K0 {
		item := value.Data.([]*kdb.K)[index]
		return item, item != nil
	}
	atom := &kdb.K{Type: -value.Type, Attr: kdb.NONE}
	switch value.Type {
	case kdb.KB:
		atom.Data = value.Data.([]bool)[index]
	case kdb.UU:
		atom.Data = value.Data.([]uuid.UUID)[index]
	case kdb.KG:
		atom.Data = value.Data.([]byte)[index]
	case kdb.KH:
		atom.Data = value.Data.([]int16)[index]
	case kdb.KI:
		atom.Data = value.Data.([]int32)[index]
	case kdb.KJ:
		atom.Data = value.Data.([]int64)[index]
	case kdb.KE:
		atom.Data = value.Data.([]float32)[index]
	case kdb.KF:
		atom.Data = value.Data.([]float64)[index]
	case kdb.KC:
		switch text := value.Data.(type) {
		case string:
			atom.Data = text[index]
		case []byte:
			atom.Data = text[index]
		default:
			return nil, false
		}
	case kdb.KS:
		atom.Data = value.Data.([]string)[index]
	case kdb.KP:
		atom.Data = value.Data.([]time.Time)[index]
	case kdb.KM:
		atom.Data = value.Data.([]kdb.Month)[index]
	case kdb.KD:
		atom.Data = value.Data.([]time.Time)[index]
	case kdb.KZ:
		atom.Data = value.Data.([]time.Time)[index]
	case kdb.KN:
		atom.Data = value.Data.([]time.Duration)[index]
	case kdb.KU:
		atom.Data = value.Data.([]kdb.Minute)[index]
	case kdb.KV:
		atom.Data = value.Data.([]kdb.Second)[index]
	case kdb.KT:
		atom.Data = value.Data.([]kdb.Time)[index]
	default:
		return nil, false
	}
	return atom, true
}

func correctedTableIndexSafe(table kdb.Table, row int) (kdb.Dict, error) {
	value := &kdb.K{Type: kdb.XT, Attr: kdb.NONE, Data: table}
	if err := validateKdbObject(value); err != nil {
		return kdb.Dict{}, err
	}
	return correctedTableIndexValidated(table, row)
}

func correctedTableIndexValidated(table kdb.Table, row int) (kdb.Dict, error) {
	if len(table.Columns) != len(table.Data) {
		return kdb.Dict{}, fmt.Errorf("table column name/data counts differ: %d and %d", len(table.Columns), len(table.Data))
	}
	if len(table.Data) == 0 {
		return kdb.Dict{}, fmt.Errorf("table has no columns")
	}
	if len(table.Columns) > maxKdbFieldsPerFrame {
		return kdb.Dict{}, fmt.Errorf("table exceeds column limit")
	}
	rowCount, err := tableRowCount(table)
	if err != nil {
		return kdb.Dict{}, err
	}
	if row < 0 || row >= rowCount {
		return kdb.Dict{}, fmt.Errorf("row index %d is out of range for %d rows", row, rowCount)
	}
	values, err := tableRowValuesValidated(table, row)
	if err != nil {
		return kdb.Dict{}, err
	}
	return kdb.Dict{
		Key:   &kdb.K{Type: kdb.KS, Attr: kdb.NONE, Data: table.Columns},
		Value: &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: values},
	}, nil
}

func projectAtom(value interface{}, depth int) (interface{}, error) {
	budget := newKdbFrameParseBudget(defaultKdbFrameParseLimits)
	if err := budget.reserveFrame("response", []string{"value"}, depth, ""); err != nil {
		return nil, err
	}
	parser := &kdbFrameParser{budget: budget}
	var normalized interface{} = value
	switch value := value.(type) {
	case time.Duration:
		normalized = int64(value)
	case kdb.Month:
		normalized = int32(value)
	case kdb.Minute:
		normalized = int32(time.Time(value).Sub(time.Time{}) / time.Minute)
	case kdb.Second:
		t := time.Time(value)
		normalized = int32(t.Second() + t.Minute()*60 + t.Hour()*3600)
	case kdb.Time:
		t := time.Time(value)
		normalized = int32(t.Hour()*3_600_000 + t.Minute()*60_000 + t.Second()*1_000 + t.Nanosecond()/1_000_000)
	case uuid.UUID:
		normalized = value.String()
	}
	return parser.projectScalar(normalized, depth)
}
