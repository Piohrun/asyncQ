package plugin

import (
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	uuid "github.com/nu7hatch/gouuid"
	kdb "github.com/sv/kdbgo"
)

const (
	maxKdbObjectDepth           = 256
	maxKdbBinaryPrimitiveIndex  = 33
	maxKdbTernaryPrimitiveIndex = 2
)

func validateKdbObject(value *kdb.K) error {
	return validateKdbObjectAt(value, "object", 0, make(map[*kdb.K]struct{}))
}

func validateKdbObjectAt(value *kdb.K, location string, depth int, ancestors map[*kdb.K]struct{}) error {
	if value == nil {
		return fmt.Errorf("%s is nil", location)
	}
	if depth > maxKdbObjectDepth {
		return fmt.Errorf("%s exceeds maximum nesting depth", location)
	}
	if _, exists := ancestors[value]; exists {
		return fmt.Errorf("%s contains a cycle", location)
	}
	if value.Type >= kdb.K0 && (value.Attr < kdb.NONE || value.Attr > kdb.GROUPED) {
		return fmt.Errorf("%s has invalid attribute %d", location, value.Attr)
	}
	ancestors[value] = struct{}{}
	defer delete(ancestors, value)

	switch {
	case value.Type < kdb.K0:
		if err := validateKdbAtom(value); err != nil {
			return fmt.Errorf("%s: %w", location, err)
		}
		return nil
	case value.Type == kdb.K0:
		items, ok := value.Data.([]*kdb.K)
		if !ok {
			return fmt.Errorf("%s has invalid data for generic list: expected []*kdb.K, got %T", location, value.Data)
		}
		for i, item := range items {
			if err := validateKdbObjectAt(item, fmt.Sprintf("%s item %d", location, i), depth+1, ancestors); err != nil {
				return err
			}
		}
		return nil
	case value.Type > kdb.K0 && value.Type <= kdb.KT:
		if _, err := kdbVectorLength(value); err != nil {
			return fmt.Errorf("%s: %w", location, err)
		}
		return nil
	case value.Type == kdb.XT:
		table, ok := value.Data.(kdb.Table)
		if !ok {
			return fmt.Errorf("%s has invalid table data: expected kdb.Table, got %T", location, value.Data)
		}
		return validateKdbTableAt(table, location, depth, ancestors)
	case value.Type == kdb.XD:
		dict, ok := value.Data.(kdb.Dict)
		if !ok {
			return fmt.Errorf("%s has invalid dictionary data: expected kdb.Dict, got %T", location, value.Data)
		}
		if dict.Key == nil {
			return fmt.Errorf("%s dictionary key is nil", location)
		}
		if dict.Value == nil {
			return fmt.Errorf("%s dictionary value is nil", location)
		}
		if err := validateKdbObjectAt(dict.Key, location+" dictionary key", depth+1, ancestors); err != nil {
			return err
		}
		if err := validateKdbObjectAt(dict.Value, location+" dictionary value", depth+1, ancestors); err != nil {
			return err
		}
		if dict.Key.Type == kdb.XT && dict.Value.Type == kdb.XT {
			keyTable := dict.Key.Data.(kdb.Table)
			valueTable := dict.Value.Data.(kdb.Table)
			keyRows, err := tableRowCount(keyTable)
			if err != nil {
				return fmt.Errorf("%s keyed table key rows: %w", location, err)
			}
			valueRows, err := tableRowCount(valueTable)
			if err != nil {
				return fmt.Errorf("%s keyed table value rows: %w", location, err)
			}
			if keyRows != valueRows {
				return fmt.Errorf("%s keyed table key/value row counts differ: %d and %d", location, keyRows, valueRows)
			}
		}
		return nil
	case value.Type == kdb.KFUNC:
		if _, ok := value.Data.(kdb.Function); !ok {
			return fmt.Errorf("%s has invalid function data: expected kdb.Function, got %T", location, value.Data)
		}
		return nil
	case value.Type == kdb.KFUNCUP:
		if _, ok := value.Data.(byte); !ok {
			return fmt.Errorf("%s has invalid unary-function data: expected byte, got %T", location, value.Data)
		}
		return nil
	case value.Type == kdb.KFUNCBP:
		operator, ok := value.Data.(byte)
		if !ok {
			return fmt.Errorf("%s has invalid binary-function data: expected byte, got %T", location, value.Data)
		}
		if operator > maxKdbBinaryPrimitiveIndex {
			return fmt.Errorf("%s has invalid binary-function index %d", location, operator)
		}
		return nil
	case value.Type == kdb.KFUNCTR:
		operator, ok := value.Data.(byte)
		if !ok {
			return fmt.Errorf("%s has invalid ternary-function data: expected byte, got %T", location, value.Data)
		}
		if operator > maxKdbTernaryPrimitiveIndex {
			return fmt.Errorf("%s has invalid ternary-function index %d", location, operator)
		}
		return nil
	case value.Type == kdb.KPROJ || value.Type == kdb.KCOMP:
		items, ok := value.Data.([]*kdb.K)
		if !ok {
			return fmt.Errorf("%s has invalid function-list data: expected []*kdb.K, got %T", location, value.Data)
		}
		for i, item := range items {
			if err := validateKdbObjectAt(item, fmt.Sprintf("%s function item %d", location, i), depth+1, ancestors); err != nil {
				return err
			}
		}
		return nil
	case value.Type >= kdb.KEACH && value.Type <= kdb.KEACHLEFT:
		operand, ok := value.Data.(*kdb.K)
		if !ok {
			return fmt.Errorf("%s has invalid adverb data: expected *kdb.K, got %T", location, value.Data)
		}
		return validateKdbObjectAt(operand, location+" adverb operand", depth+1, ancestors)
	default:
		return fmt.Errorf("%s has unsupported kdb+ type %d", location, value.Type)
	}
}

func validateKdbTableAt(table kdb.Table, location string, depth int, ancestors map[*kdb.K]struct{}) error {
	if len(table.Columns) != len(table.Data) {
		return fmt.Errorf("%s table column name/data counts differ: %d and %d", location, len(table.Columns), len(table.Data))
	}
	rowCount := -1
	for i, column := range table.Data {
		if column == nil {
			return fmt.Errorf("%s table column %d is nil", location, i)
		}
		if column.Type < kdb.K0 || column.Type > kdb.KT {
			return fmt.Errorf("%s table column %d is not a vector", location, i)
		}
		if err := validateKdbObjectAt(column, fmt.Sprintf("%s table column %d", location, i), depth+1, ancestors); err != nil {
			return err
		}
		length, err := kdbVectorLength(column)
		if err != nil {
			return fmt.Errorf("%s table column %d: %w", location, i, err)
		}
		if rowCount == -1 {
			rowCount = length
			continue
		}
		if rowCount != length {
			return fmt.Errorf("%s table columns have unequal row lengths: %d and %d", location, rowCount, length)
		}
	}
	return nil
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
		data, ok := value.Data.([]*kdb.K)
		if !ok {
			return 0, fmt.Errorf("invalid data for generic list: expected []*kdb.K, got %T", value.Data)
		}
		return len(data), nil
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
		switch data := value.Data.(type) {
		case string:
			return len(data), nil
		case []byte:
			return len(data), nil
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
	data, ok := value.Data.([]T)
	if !ok {
		return 0, invalidVectorDataError(value, expected)
	}
	return len(data), nil
}

func invalidVectorDataError(value *kdb.K, expected string) error {
	return fmt.Errorf("invalid data for kdb+ vector type %d: expected %s, got %T", value.Type, expected, value.Data)
}

func charParser(data *kdb.K) ([]string, error) {
	if data == nil || data.Type != kdb.KC {
		return nil, fmt.Errorf("object is not a character vector")
	}
	if text, ok := data.Data.(string); ok {
		out := make([]string, len(text))
		for i := range text {
			out[i] = string(text[i])
		}
		return out, nil
	}
	if bytes, ok := data.Data.([]byte); ok {
		out := make([]string, len(bytes))
		for i, value := range bytes {
			out[i] = string(value)
		}
		return out, nil
	}
	return nil, invalidVectorDataError(data, "string or []byte")
}

func stringParser(data *kdb.K) ([]string, error) {
	if data == nil || data.Type != kdb.K0 {
		return nil, fmt.Errorf("object is not a generic list")
	}
	stringCol, ok := data.Data.([]*kdb.K)
	if !ok {
		return nil, fmt.Errorf("invalid generic list data: expected []*kdb.K, got %T", data.Data)
	}
	stringArray := make([]string, len(stringCol))
	for i, word := range stringCol {
		if word == nil {
			return nil, fmt.Errorf("generic list item %d is nil", i)
		}
		if word.Type != kdb.KC {
			return nil, fmt.Errorf("generic list item %d is not a character vector: type %d", i, word.Type)
		}
		switch text := word.Data.(type) {
		case string:
			stringArray[i] = text
		case []byte:
			stringArray[i] = string(text)
		default:
			return nil, fmt.Errorf("generic list item %d has invalid character data: got %T", i, word.Data)
		}
	}
	return stringArray, nil
}

func genericListStringColumn(data *kdb.K) ([]string, error) {
	if data == nil || data.Type != kdb.K0 {
		return nil, fmt.Errorf("object is not a generic list")
	}
	items, ok := data.Data.([]*kdb.K)
	if !ok {
		return nil, fmt.Errorf("invalid generic list data: expected []*kdb.K, got %T", data.Data)
	}
	out := make([]string, len(items))
	for i, item := range items {
		value, err := kdbObjectString(item)
		if err != nil {
			return nil, fmt.Errorf("generic list item %d: %w", i, err)
		}
		out[i] = value
	}
	return out, nil
}

func standardColumnParser(inputData *kdb.K) (interface{}, error) {
	if err := validateKdbObject(inputData); err != nil {
		return nil, err
	}

	switch {
	case inputData.Type == kdb.K0:
		stringColumn, err := stringParser(inputData)
		if err == nil {
			return stringColumn, nil
		}
		return genericListStringColumn(inputData)
	case inputData.Type == kdb.KC:
		return charParser(inputData)

	case inputData.Type == kdb.KN:
		//timespan
		durArr := inputData.Data.([]time.Duration)
		durIntArr := make([]int64, len(durArr))
		for i, dur := range durArr {
			durIntArr[i] = int64(dur)
		}
		return durIntArr, nil

	case inputData.Type == kdb.KT:
		//Time
		kdbTimeArr := inputData.Data.([]kdb.Time)
		timeArr := make([]int32, len(kdbTimeArr))
		for index, entry := range kdbTimeArr {
			timeArr[index] = int32(time.Time(entry).Hour()*3600000 + time.Time(entry).Minute()*60000 + time.Time(entry).Second()*1000 + time.Time(entry).Nanosecond()/1000000)

		}
		return timeArr, nil

	case inputData.Type == kdb.UU:
		//GUID

		uuidArr := inputData.Data.([]uuid.UUID)
		guidArr := make([]string, len(uuidArr))
		for i, entry := range uuidArr {
			guidArr[i] = entry.String()
		}

		return guidArr, nil

	case inputData.Type == kdb.KU:
		//Minute
		minArr := inputData.Data.([]kdb.Minute)
		minTimeArr := make([]int32, len(minArr))
		for index, entry := range minArr {
			minTimeArr[index] = int32(time.Time(entry).Minute() + time.Time(entry).Hour()*60)
		}
		return minTimeArr, nil

	case inputData.Type == kdb.KV:
		//Second
		secArr := inputData.Data.([]kdb.Second)
		secTimeArr := make([]int32, len(secArr))
		for index, entry := range secArr {
			secTimeArr[index] = int32(time.Time(entry).Second() + time.Time(entry).Minute()*60 + time.Time(entry).Hour()*3600)
		}
		return secTimeArr, nil

	case inputData.Type == kdb.KM:
		// Month
		monthArr := inputData.Data.([]kdb.Month)
		monthIntArr := make([]int32, len(monthArr))
		for index, val := range monthArr {
			monthIntArr[index] = int32(val)
		}
		return monthIntArr, nil

	default:
		return inputData.Data, nil
	}
}

func ParseSimpleKdbTable(res *kdb.K) (*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid table: %w", err)
	}
	if res.Type != kdb.XT {
		return nil, fmt.Errorf("object is not a table")
	}
	frame := data.NewFrame("response")
	kdbTable := res.Data.(kdb.Table)
	tabData := kdbTable.Data
	frame.Fields = make([]*data.Field, 0, len(kdbTable.Columns))

	for colIndex, columnName := range kdbTable.Columns {
		column, err := standardColumnParser(tabData[colIndex])
		if err != nil {
			return nil, fmt.Errorf("table column %d: %w", colIndex, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(columnName, nil, column))
	}
	return frame, nil
}

func ParseKeyedKdbTableAsFrame(res *kdb.K) (*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid keyed table: %w", err)
	}
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	kdbDict := res.Data.(kdb.Dict)
	if kdbDict.Key.Type != kdb.XT || kdbDict.Value.Type != kdb.XT {
		return nil, fmt.Errorf("dictionary is not a keyed table")
	}
	keyFrame, err := ParseSimpleKdbTable(kdbDict.Key)
	if err != nil {
		return nil, err
	}
	valueFrame, err := ParseSimpleKdbTable(kdbDict.Value)
	if err != nil {
		return nil, err
	}
	keyRows, err := tableRowCount(kdbDict.Key.Data.(kdb.Table))
	if err != nil {
		return nil, fmt.Errorf("key table row count: %w", err)
	}
	valueRows, err := tableRowCount(kdbDict.Value.Data.(kdb.Table))
	if err != nil {
		return nil, fmt.Errorf("value table row count: %w", err)
	}
	if keyRows != valueRows {
		return nil, fmt.Errorf("key and value table row counts differ")
	}
	frame := data.NewFrame("response")
	frame.Fields = append(frame.Fields, keyFrame.Fields...)
	frame.Fields = append(frame.Fields, valueFrame.Fields...)
	makeFrameFieldNamesUnique(frame)
	return frame, nil
}

func ParseKdbObjectAsFrame(res *kdb.K) (*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid object: %w", err)
	}
	frame := data.NewFrame("response")
	values, err := kdbObjectColumn(res)
	if err != nil {
		return nil, err
	}
	frame.Fields = append(frame.Fields, data.NewField("value", nil, values))
	return frame, nil
}

func ParseKdbDictAsFrame(res *kdb.K) (*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid dictionary: %w", err)
	}
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	d := res.Data.(kdb.Dict)
	columnNames, err := dictColumnNames(d.Key)
	if err != nil {
		return nil, err
	}
	values, err := dictValues(d.Value, len(columnNames))
	if err != nil {
		return nil, err
	}
	if len(columnNames) != len(values) {
		return nil, fmt.Errorf("dictionary key/value lengths differ")
	}
	depth, err := dictFrameDepth(values)
	if err != nil {
		return nil, err
	}
	frame := data.NewFrame("response")
	for i, name := range columnNames {
		col, err := kdbObjectColumnWithDepth(values[i], depth)
		if err != nil {
			return nil, fmt.Errorf("dictionary value %q: %w", name, err)
		}
		frame.Fields = append(frame.Fields, data.NewField(name, nil, col))
	}
	return frame, nil
}

func ParseKdbDictListAsFrame(res *kdb.K) (*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid dictionary list: %w", err)
	}
	if res.Type != kdb.K0 {
		return nil, fmt.Errorf("object is not a generic list")
	}
	rows := res.Data.([]*kdb.K)
	if len(rows) == 0 {
		return nil, fmt.Errorf("dictionary list is empty")
	}
	if rows[0] == nil || rows[0].Type != kdb.XD {
		return nil, fmt.Errorf("first item is not a dictionary")
	}

	if frame, ok, err := parseKdbDictListUniformRows(rows); ok || err != nil {
		return frame, err
	}

	columnNames := make([]string, 0)
	seenColumns := map[string]bool{}
	rowValues := make([]map[string]*kdb.K, len(rows))
	for rowIndex, row := range rows {
		if row == nil || row.Type != kdb.XD {
			return nil, fmt.Errorf("item %d is not a dictionary", rowIndex)
		}
		rowDict := row.Data.(kdb.Dict)
		names, err := dictColumnNames(rowDict.Key)
		if err != nil {
			return nil, fmt.Errorf("item %d keys: %w", rowIndex, err)
		}
		values, err := dictValues(rowDict.Value, len(names))
		if err != nil {
			return nil, fmt.Errorf("item %d values: %w", rowIndex, err)
		}
		if len(names) != len(values) {
			return nil, fmt.Errorf("item %d key/value lengths differ", rowIndex)
		}
		rowMap := make(map[string]*kdb.K, len(names))
		for colIndex, name := range names {
			if !seenColumns[name] {
				seenColumns[name] = true
				columnNames = append(columnNames, name)
			}
			rowMap[name] = values[colIndex]
		}
		rowValues[rowIndex] = rowMap
	}
	columns := make([][]interface{}, len(columnNames))
	for colIndex, name := range columnNames {
		columns[colIndex] = make([]interface{}, 0, len(rows))
		for _, row := range rowValues {
			value, ok := row[name]
			if !ok {
				columns[colIndex] = append(columns[colIndex], nil)
				continue
			}
			cell, err := kdbCellValue(value)
			if err != nil {
				return nil, fmt.Errorf("column %d: %w", colIndex, err)
			}
			columns[colIndex] = append(columns[colIndex], cell)
		}
	}
	return frameFromInterfaceColumns(columnNames, columns)
}

func parseKdbDictListUniformRows(rows []*kdb.K) (*data.Frame, bool, error) {
	var columnNames []string
	var columns [][]interface{}
	for rowIndex, row := range rows {
		if row == nil || row.Type != kdb.XD {
			return nil, false, fmt.Errorf("item %d is not a dictionary", rowIndex)
		}
		rowDict := row.Data.(kdb.Dict)
		names, err := dictColumnNames(rowDict.Key)
		if err != nil {
			return nil, false, fmt.Errorf("item %d keys: %w", rowIndex, err)
		}
		values, err := dictValues(rowDict.Value, len(names))
		if err != nil {
			return nil, false, fmt.Errorf("item %d values: %w", rowIndex, err)
		}
		if len(names) != len(values) {
			return nil, false, fmt.Errorf("item %d key/value lengths differ", rowIndex)
		}
		if rowIndex == 0 {
			columnNames = names
			columns = make([][]interface{}, len(columnNames))
			for i := range columns {
				columns[i] = make([]interface{}, 0, len(rows))
			}
		} else if !sameStringSlice(columnNames, names) {
			return nil, false, nil
		}
		for colIndex, value := range values {
			cell, err := kdbCellValue(value)
			if err != nil {
				return nil, false, fmt.Errorf("item %d value %d: %w", rowIndex, colIndex, err)
			}
			columns[colIndex] = append(columns[colIndex], cell)
		}
	}
	frame, err := frameFromInterfaceColumns(columnNames, columns)
	return frame, true, err
}

func frameFromInterfaceColumns(columnNames []string, columns [][]interface{}) (*data.Frame, error) {
	if len(columnNames) != len(columns) {
		return nil, fmt.Errorf("column name/data counts differ")
	}
	frame := data.NewFrame("response")
	frame.Fields = make([]*data.Field, 0, len(columnNames))
	for i, name := range columnNames {
		frame.Fields = append(frame.Fields, data.NewField(name, nil, typedInterfaceColumn(columns[i])))
	}
	return frame, nil
}

func dictColumnNames(keys *kdb.K) ([]string, error) {
	if err := validateKdbObject(keys); err != nil {
		return nil, fmt.Errorf("invalid dictionary keys: %w", err)
	}
	switch keys.Type {
	case -kdb.KS:
		return []string{keys.Data.(string)}, nil
	case kdb.KS:
		return keys.Data.([]string), nil
	case kdb.KC:
		switch value := keys.Data.(type) {
		case string:
			return []string{value}, nil
		case []byte:
			return []string{string(value)}, nil
		default:
			return nil, fmt.Errorf("invalid character dictionary key data: got %T", keys.Data)
		}
	case kdb.K0:
		items := keys.Data.([]*kdb.K)
		names := make([]string, len(items))
		for i, item := range items {
			name, err := kdbObjectString(item)
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

func makeFrameFieldNamesUnique(frame *data.Frame) {
	seen := map[string]int{}
	for _, field := range frame.Fields {
		count := seen[field.Name]
		seen[field.Name] = count + 1
		if count == 0 {
			continue
		}
		field.Name = fmt.Sprintf("%s_%d", field.Name, count+1)
	}
}

func sameStringSlice(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func kdbCellValue(value *kdb.K) (interface{}, error) {
	if value == nil {
		return nil, fmt.Errorf("value is nil")
	}
	if err := validateKdbObject(value); err != nil {
		return nil, err
	}
	if value.Type < kdb.K0 {
		return kdbAtomValue(value)
	}
	if value.Type == kdb.KC {
		switch text := value.Data.(type) {
		case string:
			return text, nil
		case []byte:
			return string(text), nil
		}
	}
	if value.Type > kdb.K0 && value.Type <= kdb.KT {
		length, err := kdbVectorLength(value)
		if err != nil {
			return nil, err
		}
		if length != 1 {
			text, err := kdbObjectString(value)
			return text, err
		}
		if item, ok := correctedIndex(value, 0).(*kdb.K); ok {
			return kdbAtomValue(item)
		}
		return nil, fmt.Errorf("value could not be indexed")
	}
	return kdbObjectString(value)
}

func typedInterfaceColumn(col []interface{}) interface{} {
	if len(col) == 0 {
		return []string{}
	}
	if hasNilValue(col) {
		return nullableInterfaceColumn(col)
	}
	if converted, ok := mixedNumericColumn(col); ok {
		return converted
	}
	for _, value := range col {
		switch value.(type) {
		case string:
			return typedStringColumn(col)
		case bool:
			return typedBoolColumn(col)
		case int16:
			return typedInt16Column(col)
		case int32:
			return typedInt32Column(col)
		case int64:
			return typedInt64Column(col)
		case float32:
			return typedFloat32Column(col)
		case float64:
			return typedFloat64Column(col)
		case time.Time:
			return typedTimeColumn(col)
		default:
			return stringInterfaceColumn(col)
		}
	}
	return stringInterfaceColumn(col)
}

func nullableInterfaceColumn(col []interface{}) interface{} {
	if converted, ok := nullableMixedNumericColumn(col); ok {
		return converted
	}
	for _, value := range col {
		if value == nil {
			continue
		}
		switch value.(type) {
		case string:
			if out, ok := nullableTypedColumn[string](col); ok {
				return out
			}
		case bool:
			if out, ok := nullableTypedColumn[bool](col); ok {
				return out
			}
		case int16:
			if out, ok := nullableTypedColumn[int16](col); ok {
				return out
			}
		case int32:
			if out, ok := nullableTypedColumn[int32](col); ok {
				return out
			}
		case int64:
			if out, ok := nullableTypedColumn[int64](col); ok {
				return out
			}
		case float32:
			if out, ok := nullableTypedColumn[float32](col); ok {
				return out
			}
		case float64:
			if out, ok := nullableTypedColumn[float64](col); ok {
				return out
			}
		case time.Time:
			if out, ok := nullableTypedColumn[time.Time](col); ok {
				return out
			}
		}
		return nullableStringColumn(col)
	}
	return nullableStringColumn(col)
}

func nullableTypedColumn[T any](col []interface{}) ([]*T, bool) {
	out := make([]*T, len(col))
	for i, value := range col {
		if value == nil {
			continue
		}
		typed, ok := value.(T)
		if !ok {
			return nil, false
		}
		out[i] = &typed
	}
	return out, true
}

func nullableStringColumn(col []interface{}) []*string {
	out := make([]*string, len(col))
	for i, value := range col {
		if value == nil {
			continue
		}
		text := fmt.Sprint(value)
		out[i] = &text
	}
	return out
}

func hasNilValue(col []interface{}) bool {
	for _, value := range col {
		if value == nil {
			return true
		}
	}
	return false
}

func mixedNumericColumn(col []interface{}) ([]float64, bool) {
	out := make([]float64, len(col))
	firstKind := ""
	mixed := false
	for i, value := range col {
		converted, kind, ok := numericCellValue(value)
		if !ok {
			return nil, false
		}
		if firstKind == "" {
			firstKind = kind
		} else if firstKind != kind {
			mixed = true
		}
		out[i] = converted
	}
	if !mixed {
		return nil, false
	}
	return out, true
}

func nullableMixedNumericColumn(col []interface{}) ([]*float64, bool) {
	out := make([]*float64, len(col))
	firstKind := ""
	mixed := false
	seen := false
	for i, value := range col {
		if value == nil {
			continue
		}
		converted, kind, ok := numericCellValue(value)
		if !ok {
			return nil, false
		}
		if !seen {
			firstKind = kind
			seen = true
		} else if firstKind != kind {
			mixed = true
		}
		out[i] = &converted
	}
	if !seen || !mixed {
		return nil, false
	}
	return out, true
}

func numericCellValue(value interface{}) (float64, string, bool) {
	switch v := value.(type) {
	case int8:
		return float64(v), "int8", true
	case int16:
		return float64(v), "int16", true
	case int32:
		return float64(v), "int32", true
	case int64:
		return float64(v), "int64", true
	case uint8:
		return float64(v), "uint8", true
	case uint16:
		return float64(v), "uint16", true
	case uint32:
		return float64(v), "uint32", true
	case uint64:
		return float64(v), "uint64", true
	case float32:
		return float64(v), "float32", true
	case float64:
		return v, "float64", true
	default:
		return 0, "", false
	}
}

func typedStringColumn(col []interface{}) interface{} {
	out := make([]string, len(col))
	for i, value := range col {
		v, ok := value.(string)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedBoolColumn(col []interface{}) interface{} {
	out := make([]bool, len(col))
	for i, value := range col {
		v, ok := value.(bool)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedInt16Column(col []interface{}) interface{} {
	out := make([]int16, len(col))
	for i, value := range col {
		v, ok := value.(int16)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedInt32Column(col []interface{}) interface{} {
	out := make([]int32, len(col))
	for i, value := range col {
		v, ok := value.(int32)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedInt64Column(col []interface{}) interface{} {
	out := make([]int64, len(col))
	for i, value := range col {
		v, ok := value.(int64)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedFloat32Column(col []interface{}) interface{} {
	out := make([]float32, len(col))
	for i, value := range col {
		v, ok := value.(float32)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedFloat64Column(col []interface{}) interface{} {
	out := make([]float64, len(col))
	for i, value := range col {
		v, ok := value.(float64)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func typedTimeColumn(col []interface{}) interface{} {
	out := make([]time.Time, len(col))
	for i, value := range col {
		v, ok := value.(time.Time)
		if !ok {
			return stringInterfaceColumn(col)
		}
		out[i] = v
	}
	return out
}

func stringInterfaceColumn(col []interface{}) []string {
	out := make([]string, len(col))
	for i, value := range col {
		out[i] = fmt.Sprint(value)
	}
	return out
}

func dictValues(values *kdb.K, keyCount int) ([]*kdb.K, error) {
	if values == nil {
		return nil, fmt.Errorf("dictionary values are nil")
	}
	if err := validateKdbObject(values); err != nil {
		return nil, fmt.Errorf("invalid dictionary values: %w", err)
	}
	if list, ok := values.Data.([]*kdb.K); ok {
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
		for i := 0; i < keyCount; i++ {
			item, ok := correctedIndex(values, i).(*kdb.K)
			if !ok || item == nil {
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
	lengths := make([]int, len(values))
	for i, value := range values {
		if value == nil || value.Type < kdb.K0 {
			lengths[i] = 1
			continue
		}
		if value.Type == kdb.KC {
			lengths[i] = 1
			continue
		}
		length, err := kdbVectorLength(value)
		if err != nil {
			return 0, fmt.Errorf("dictionary value %d: %w", i, err)
		}
		lengths[i] = length
		if length > depth {
			depth = length
		}
	}
	targetLength := -1
	hasSingleton := false
	for i, length := range lengths {
		if length == 1 {
			hasSingleton = true
			continue
		}
		if targetLength == -1 {
			targetLength = length
			continue
		}
		if length != targetLength {
			return 0, fmt.Errorf("dictionary values have incompatible lengths at index %d: %d and %d", i, length, targetLength)
		}
	}
	if targetLength == 0 && hasSingleton {
		return 0, fmt.Errorf("dictionary values mix empty and singleton columns")
	}
	return depth, nil
}

func kdbObjectColumn(value *kdb.K) (interface{}, error) {
	depth, err := kdbObjectDepth(value)
	if err != nil {
		return nil, err
	}
	return kdbObjectColumnWithDepth(value, depth)
}

func kdbObjectColumnWithDepth(value *kdb.K, depth int) (interface{}, error) {
	if value == nil {
		return nil, fmt.Errorf("value is nil")
	}
	if err := validateKdbObject(value); err != nil {
		return nil, err
	}
	if depth < 0 {
		return nil, fmt.Errorf("column depth cannot be negative")
	}
	if value.Type < kdb.K0 {
		atom, err := kdbAtomValue(value)
		if err != nil {
			return nil, err
		}
		return projectAtom(atom, depth)
	}
	if value.Type == kdb.KC {
		var text string
		switch data := value.Data.(type) {
		case string:
			text = data
		case []byte:
			text = string(data)
		default:
			return nil, fmt.Errorf("invalid character vector data: got %T", value.Data)
		}
		if depth <= 1 {
			return []string{text}, nil
		}
		return projectAtom(text, depth)
	}
	if value.Type == kdb.K0 {
		if strings, err := stringParser(value); err == nil {
			return resizeColumn(strings, depth), nil
		}
		strings, err := genericListStringColumn(value)
		if err != nil {
			return nil, err
		}
		return resizeColumn(strings, depth), nil
	}
	column, err := standardColumnParser(value)
	if err != nil {
		return nil, err
	}
	return resizeParsedColumn(column, depth), nil
}

func kdbObjectDepth(value *kdb.K) (int, error) {
	if value == nil {
		return 0, fmt.Errorf("value is nil")
	}
	if err := validateKdbObject(value); err != nil {
		return 0, err
	}
	if value.Type < kdb.K0 || value.Type == kdb.KC {
		return 1, nil
	}
	return kdbVectorLength(value)
}

func kdbAtomValue(value *kdb.K) (interface{}, error) {
	if value == nil || value.Type >= kdb.K0 {
		return nil, fmt.Errorf("object is not a kdb+ atom")
	}
	if err := validateKdbAtom(value); err != nil {
		return nil, err
	}
	if value.Type == -kdb.KC {
		return string(value.Data.(byte)), nil
	}
	return value.Data, nil
}

func kdbObjectString(value *kdb.K) (string, error) {
	if value == nil {
		return "", fmt.Errorf("object is nil")
	}
	if err := validateKdbObject(value); err != nil {
		return "", err
	}
	if value.Type == -kdb.KC {
		return string(value.Data.(byte)), nil
	}
	if value.Type == kdb.KC {
		switch text := value.Data.(type) {
		case string:
			return text, nil
		case []byte:
			return string(text), nil
		}
	}
	return fmt.Sprint(value.Data), nil
}

func resizeParsedColumn(col interface{}, depth int) interface{} {
	switch v := col.(type) {
	case []string:
		return resizeColumn(v, depth)
	case []bool:
		return resizeColumn(v, depth)
	case []byte:
		return resizeColumn(v, depth)
	case []int16:
		return resizeColumn(v, depth)
	case []int32:
		return resizeColumn(v, depth)
	case []int64:
		return resizeColumn(v, depth)
	case []float32:
		return resizeColumn(v, depth)
	case []float64:
		return resizeColumn(v, depth)
	case []time.Time:
		return resizeColumn(v, depth)
	default:
		return col
	}
}

func resizeColumn[T any](col []T, depth int) []T {
	if len(col) == depth || len(col) != 1 || depth <= 1 {
		return col
	}
	out := make([]T, depth)
	for i := range out {
		out[i] = col[0]
	}
	return out
}

func ParseGroupedKdbTable(res *kdb.K, includeKeys bool) ([]*data.Frame, error) {
	if err := validateKdbObject(res); err != nil {
		return nil, fmt.Errorf("invalid grouped table: %w", err)
	}
	if res.Type != kdb.XD {
		return nil, fmt.Errorf("object is not a dictionary")
	}
	kdbDict := res.Data.(kdb.Dict)
	if kdbDict.Key.Type != kdb.XT || kdbDict.Value.Type != kdb.XT {
		return nil, fmt.Errorf("grouped table key and value must both be tables")
	}
	keyTable := kdbDict.Key.Data.(kdb.Table)
	valData := kdbDict.Value.Data.(kdb.Table)
	if len(keyTable.Data) == 0 {
		return nil, fmt.Errorf("grouped table key table has no columns")
	}
	rc, err := kdbVectorLength(keyTable.Data[0])
	if err != nil {
		return nil, fmt.Errorf("grouped table key rows: %w", err)
	}
	if len(valData.Data) == 0 {
		if rc == 0 {
			return []*data.Frame{}, nil
		}
		return nil, fmt.Errorf("grouped table value table has no columns")
	}
	valueRows, err := kdbVectorLength(valData.Data[0])
	if err != nil {
		return nil, fmt.Errorf("grouped table value rows: %w", err)
	}
	if rc != valueRows {
		return nil, fmt.Errorf("grouped table key/value row counts differ: %d and %d", rc, valueRows)
	}
	frameArray := make([]*data.Frame, rc)
	keyColCount := len(keyTable.Columns)
	for row := 0; row < rc; row++ {
		keyData, err := correctedTableIndexValidated(keyTable, row)
		if err != nil {
			return nil, fmt.Errorf("grouped table key row %d: %w", row, err)
		}
		frameName, err := parseFrameName(keyData.Value)
		if err != nil {
			return nil, fmt.Errorf("grouped table key row %d name: %w", row, err)
		}
		frame := data.NewFrame(frameName)
		rowData, err := correctedTableIndexValidated(valData, row)
		if err != nil {
			return nil, fmt.Errorf("grouped table value row %d: %w", row, err)
		}
		depth, err := getDepth(rowData.Value.Data.([]*kdb.K))
		if err != nil {
			return nil, fmt.Errorf("grouped table value row %d: %w", row, err)
		}
		var masterCols []string
		var masterData []*kdb.K
		if includeKeys {
			masterCols = append(keyData.Key.Data.([]string), rowData.Key.Data.([]string)...)
			masterData = append(keyData.Value.Data.([]*kdb.K), rowData.Value.Data.([]*kdb.K)...)
		} else {
			masterCols = rowData.Key.Data.([]string)
			masterData = rowData.Value.Data.([]*kdb.K)
		}
		if len(masterCols) != len(masterData) {
			return nil, fmt.Errorf("grouped table row %d column name/data counts differ", row)
		}
		for i, colName := range masterCols {
			kObj := masterData[i]
			if kObj == nil {
				return nil, fmt.Errorf("grouped table row %d column %d is nil", row, i)
			}
			var dat interface{}
			if kObj.Type < 0 {
				atom, err := kdbAtomValue(kObj)
				if err != nil {
					return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
				}
				dat, err = projectAtom(atom, depth)
				if err != nil {
					return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
				}
			} else {
				switch {
				case kObj.Type == kdb.KC:
					// if the column is a key column, this is a string. Otherwise it is a char list
					length, err := kdbVectorLength(kObj)
					if err != nil {
						return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
					}
					if i < keyColCount || length != depth {
						text, err := kdbObjectString(kObj)
						if err != nil {
							return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
						}
						dat, err = projectAtom(text, depth)
						if err != nil {
							return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
						}
					} else {
						dat, err = charParser(kObj)
						if err != nil {
							return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
						}
					}
				case kObj.Type > kdb.K0:
					dat, err = standardColumnParser(kObj)
					if err != nil {
						return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
					}
				case kObj.Type == kdb.K0:
					stringColumn, stringErr := stringParser(kObj)
					if stringErr == nil {
						dat = stringColumn
					} else {
						dat, err = genericListStringColumn(kObj)
						if err != nil {
							return nil, fmt.Errorf("grouped table row %d column %d: %w", row, i, err)
						}
					}
				default:
					return nil, fmt.Errorf("grouped table row %d column %d has unsupported type %d", row, i, kObj.Type)
				}
			}
			frame.Fields = append(frame.Fields, data.NewField(colName, nil, dat))
		}
		frameArray[row] = frame
	}
	return frameArray, nil
}

func parseFrameName(key *kdb.K) (string, error) {
	if err := validateKdbObject(key); err != nil {
		return "", err
	}
	if key.Type == kdb.K0 {
		items := key.Data.([]*kdb.K)
		names := make([]string, len(items))
		for i, item := range items {
			name, err := kdbObjectString(item)
			if err != nil {
				return "", fmt.Errorf("key item %d: %w", i, err)
			}
			names[i] = name
		}
		return strings.Join(names, " - "), nil
	}
	if key.Type < kdb.K0 {
		return kdbObjectString(key)
	}
	if key.Type > kdb.K0 && key.Type <= kdb.KT {
		length, err := kdbVectorLength(key)
		if err != nil {
			return "", err
		}
		names := make([]string, length)
		for i := 0; i < length; i++ {
			item, ok := correctedIndex(key, i).(*kdb.K)
			if !ok || item == nil {
				return "", fmt.Errorf("key item %d could not be indexed", i)
			}
			name, err := kdbObjectString(item)
			if err != nil {
				return "", fmt.Errorf("key item %d: %w", i, err)
			}
			names[i] = name
		}
		return strings.Join(names, " - "), nil
	}
	return "", fmt.Errorf("unsupported frame-name type %d", key.Type)
}

func getDepth(colArray []*kdb.K) (int, error) {
	d := -1
	aggPresent := false
	for i, value := range colArray {
		if value == nil {
			return 0, fmt.Errorf("column %d is nil", i)
		}
		if err := validateKdbObject(value); err != nil {
			return 0, fmt.Errorf("column %d: %w", i, err)
		}
		if value.Type < kdb.K0 {
			aggPresent = true
			continue
		}
		if value.Type == kdb.KC {
			continue
		}
		length, err := kdbVectorLength(value)
		if err != nil {
			return 0, fmt.Errorf("column %d: %w", i, err)
		}
		if d == -1 {
			d = length
			continue
		}
		if d != length {
			return 0, fmt.Errorf("columns have unequal lengths: %d and %d", d, length)
		}
	}
	if d == -1 {
		if aggPresent {
			return 1, nil
		}
		return 0, fmt.Errorf("all column values are character vectors or the column list is empty")
	}
	return d, nil
}

func correctedIndex(value *kdb.K, i int) interface{} {
	if value == nil || i < 0 {
		return nil
	}
	if value.Type == kdb.XT {
		table, ok := value.Data.(kdb.Table)
		if !ok {
			return nil
		}
		row, err := correctedTableIndexSafe(table, i)
		if err != nil {
			return nil
		}
		return &kdb.K{Type: kdb.XD, Attr: kdb.NONE, Data: row}
	}
	if value.Type < kdb.K0 || value.Type > kdb.KT {
		return nil
	}
	length, err := kdbVectorLength(value)
	if err != nil || i >= length {
		return nil
	}
	if value.Type == kdb.K0 {
		return value.Data.([]*kdb.K)[i]
	}
	return indexKdbArray(value, i)
}

func indexKdbArray(value *kdb.K, i int) interface{} {
	length, err := kdbVectorLength(value)
	if err != nil || i < 0 || i >= length || value.Type <= kdb.K0 {
		return nil
	}
	atom := &kdb.K{Type: -value.Type, Attr: kdb.NONE}
	switch value.Type {
	case kdb.KB:
		atom.Data = value.Data.([]bool)[i]
	case kdb.UU:
		atom.Data = value.Data.([]uuid.UUID)[i]
	case kdb.KG:
		atom.Data = value.Data.([]byte)[i]
	case kdb.KH:
		atom.Data = value.Data.([]int16)[i]
	case kdb.KI:
		atom.Data = value.Data.([]int32)[i]
	case kdb.KJ:
		atom.Data = value.Data.([]int64)[i]
	case kdb.KE:
		atom.Data = value.Data.([]float32)[i]
	case kdb.KF:
		atom.Data = value.Data.([]float64)[i]
	case kdb.KC:
		switch text := value.Data.(type) {
		case string:
			atom.Data = text[i]
		case []byte:
			atom.Data = text[i]
		}
	case kdb.KS:
		atom.Data = value.Data.([]string)[i]
	case kdb.KP:
		atom.Data = value.Data.([]time.Time)[i]
	case kdb.KM:
		atom.Data = value.Data.([]kdb.Month)[i]
	case kdb.KD:
		atom.Data = value.Data.([]time.Time)[i]
	case kdb.KZ:
		atom.Data = value.Data.([]time.Time)[i]
	case kdb.KN:
		atom.Data = value.Data.([]time.Duration)[i]
	case kdb.KU:
		atom.Data = value.Data.([]kdb.Minute)[i]
	case kdb.KV:
		atom.Data = value.Data.([]kdb.Second)[i]
	case kdb.KT:
		atom.Data = value.Data.([]kdb.Time)[i]
	default:
		return nil
	}
	return atom
}

func correctedTableIndexSafe(tbl kdb.Table, i int) (kdb.Dict, error) {
	table := &kdb.K{Type: kdb.XT, Attr: kdb.NONE, Data: tbl}
	if err := validateKdbObject(table); err != nil {
		return kdb.Dict{}, err
	}
	return correctedTableIndexValidated(tbl, i)
}

// correctedTableIndexValidated indexes a table whose full shape and columns were
// already validated by the caller. Its work is bounded by the column count.
func correctedTableIndexValidated(tbl kdb.Table, i int) (kdb.Dict, error) {
	if len(tbl.Columns) != len(tbl.Data) {
		return kdb.Dict{}, fmt.Errorf("table column name/data counts differ: %d and %d", len(tbl.Columns), len(tbl.Data))
	}
	if len(tbl.Data) == 0 {
		return kdb.Dict{}, fmt.Errorf("table has no columns")
	}
	rowCount, err := kdbVectorLength(tbl.Data[0])
	if err != nil {
		return kdb.Dict{}, err
	}
	if i < 0 || i >= rowCount {
		return kdb.Dict{}, fmt.Errorf("row index %d is out of range for %d rows", i, rowCount)
	}
	values := make([]*kdb.K, len(tbl.Columns))
	for columnIndex := range tbl.Columns {
		column := tbl.Data[columnIndex]
		if column == nil || column.Type < kdb.K0 || column.Type > kdb.KT {
			return kdb.Dict{}, fmt.Errorf("column %d is not an indexable vector", columnIndex)
		}
		columnLength, err := kdbVectorLength(column)
		if err != nil {
			return kdb.Dict{}, fmt.Errorf("column %d: %w", columnIndex, err)
		}
		if i >= columnLength {
			return kdb.Dict{}, fmt.Errorf("row index %d is out of range for column %d with %d rows", i, columnIndex, columnLength)
		}
		item, ok := correctedIndex(column, i).(*kdb.K)
		if !ok || item == nil {
			return kdb.Dict{}, fmt.Errorf("column %d could not be indexed at row %d", columnIndex, i)
		}
		values[columnIndex] = item
	}
	return kdb.Dict{
		Key:   &kdb.K{Type: kdb.KS, Attr: kdb.NONE, Data: tbl.Columns},
		Value: &kdb.K{Type: kdb.K0, Attr: kdb.NONE, Data: values},
	}, nil
}

func projectAtom(a interface{}, d int) (interface{}, error) {
	if d < 0 {
		return nil, fmt.Errorf("projection depth cannot be negative")
	}
	var o interface{}
	switch v := a.(type) {
	case int8:
		arr := make([]int8, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *int8:
		arr := make([]*int8, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case int16:
		arr := make([]int16, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *int16:
		arr := make([]*int16, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case int32:
		arr := make([]int32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *int32:
		arr := make([]*int32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case int64:
		arr := make([]int64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *int64:
		arr := make([]*int64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case uint8:
		arr := make([]uint8, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *uint8:
		arr := make([]*uint8, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case uint16:
		arr := make([]uint16, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *uint16:
		arr := make([]*uint16, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case uint32:
		arr := make([]uint32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *uint32:
		arr := make([]*uint32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case uint64:
		arr := make([]uint64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *uint64:
		arr := make([]*uint64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case float32:
		arr := make([]float32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *float32:
		arr := make([]*float32, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case float64:
		arr := make([]float64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *float64:
		arr := make([]*float64, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case string:
		arr := make([]string, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *string:
		arr := make([]*string, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case bool:
		arr := make([]bool, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *bool:
		arr := make([]*bool, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case time.Time:
		arr := make([]time.Time, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case *time.Time:
		arr := make([]*time.Time, d)
		for i := 0; i < d; i++ {
			arr[i] = v
		}
		o = arr
	case time.Duration:
		arr := make([]int64, d)
		for i := 0; i < d; i++ {
			arr[i] = int64(v)
		}
		o = arr
	case kdb.Minute:
		arr := make([]int32, d)
		for i := 0; i < d; i++ {
			arr[i] = int32(time.Time(v).Sub(time.Time{}) / time.Minute)
		}
		o = arr
	case kdb.Month:
		arr := make([]int32, d)
		for i := 0; i < d; i++ {
			arr[i] = int32(v)
		}
		o = arr
	case kdb.Second:
		arr := make([]int32, d)
		for i := 0; i < d; i++ {
			arr[i] = int32(time.Time(v).Second() + time.Time(v).Minute()*60 + time.Time(v).Hour()*3600)
		}
		o = arr
	case uuid.UUID:
		arr := make([]string, d)
		for i := 0; i < d; i++ {
			arr[i] = v.String()
		}
		o = arr
	case kdb.Time:
		arr := make([]int32, d)
		for i := 0; i < d; i++ {
			arr[i] = int32(time.Time(v).Hour()*3600000 + time.Time(v).Minute()*60000 + time.Time(v).Second()*1000 + time.Time(v).Nanosecond()/1000000)
		}
		o = arr
	default:
		return nil, fmt.Errorf("unsupported projected atom type %T", a)
	}
	return o, nil
}
