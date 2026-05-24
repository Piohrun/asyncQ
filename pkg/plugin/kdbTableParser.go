package plugin

import (
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	uuid "github.com/nu7hatch/gouuid"
	kdb "github.com/sv/kdbgo"
)

func charParser(data *kdb.K) []string {
	byteArray := make([]string, data.Len())
	for i := 0; i < data.Len(); i++ {
		byteArray[i] = string(data.Index(i).(byte))
	}
	return byteArray
}

func stringParser(data *kdb.K) ([]string, error) {
	stringCol := data.Data.([]*kdb.K)
	stringArray := make([]string, data.Len())
	for i, word := range stringCol {
		if word.Type != kdb.KC {
			return nil, fmt.Errorf("A column is present which is neither a vector nor a string column. kdb+ type at index %v: %v", i, word.Type)
		}
		stringArray[i] = word.Data.(string)
	}
	return stringArray, nil
}

func standardColumnParser(inputData *kdb.K) interface{} {

	switch {
	case inputData.Type == kdb.K0:
		stringColumn, err := stringParser(inputData)
		if err != nil {
			//return nil, fmt.Errorf("The following column: %v return this error: %v", columnName, err)
		}
		return stringColumn
	case inputData.Type == kdb.KC:
		return charParser(inputData)

	case inputData.Type == kdb.KN:
		//timespan
		durArr := inputData.Data.([]time.Duration)
		durIntArr := make([]int64, len(durArr))
		for i, dur := range durArr {
			durIntArr[i] = int64(dur)
		}
		return durIntArr

	case inputData.Type == kdb.KT:
		//Time
		kdbTimeArr := inputData.Data.([]kdb.Time)
		timeArr := make([]int32, len(kdbTimeArr))
		for index, entry := range kdbTimeArr {
			timeArr[index] = int32(time.Time(entry).Hour()*3600000 + time.Time(entry).Minute()*60000 + time.Time(entry).Second()*1000 + time.Time(entry).Nanosecond()/1000000)

		}
		return timeArr

	case inputData.Type == kdb.UU:
		//GUID

		uuidArr := inputData.Data.([]uuid.UUID)
		guidArr := make([]string, len(uuidArr))
		for i, entry := range uuidArr {
			guidArr[i] = entry.String()
		}

		return guidArr

	case inputData.Type == kdb.KU:
		//Minute
		minArr := inputData.Data.([]kdb.Minute)
		minTimeArr := make([]int32, len(minArr))
		for index, entry := range minArr {
			minTimeArr[index] = int32(time.Time(entry).Minute() + time.Time(entry).Hour()*60)
		}
		return minTimeArr

	case inputData.Type == kdb.KV:
		//Second
		secArr := inputData.Data.([]kdb.Second)
		secTimeArr := make([]int32, len(secArr))
		for index, entry := range secArr {
			secTimeArr[index] = int32(time.Time(entry).Second() + time.Time(entry).Minute()*60 + time.Time(entry).Hour()*3600)
		}
		return secTimeArr

	case inputData.Type == kdb.KM:
		// Month
		monthArr := inputData.Data.([]kdb.Month)
		monthIntArr := make([]int32, len(monthArr))
		for index, val := range monthArr {
			monthIntArr[index] = int32(val)
		}
		return monthIntArr

	default:
		return inputData.Data
	}
}

func ParseSimpleKdbTable(res *kdb.K) (*data.Frame, error) {
	frame := data.NewFrame("response")
	kdbTable := res.Data.(kdb.Table)
	tabData := kdbTable.Data

	for colIndex, columnName := range kdbTable.Columns {
		frame.Fields = append(frame.Fields, data.NewField(columnName, nil, standardColumnParser(tabData[colIndex])))

	}
	return frame, nil
}

func ParseKeyedKdbTableAsFrame(res *kdb.K) (*data.Frame, error) {
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
	if len(keyFrame.Fields) > 0 && len(valueFrame.Fields) > 0 && keyFrame.Fields[0].Len() != valueFrame.Fields[0].Len() {
		return nil, fmt.Errorf("key and value table row counts differ")
	}
	frame := data.NewFrame("response")
	frame.Fields = append(frame.Fields, keyFrame.Fields...)
	frame.Fields = append(frame.Fields, valueFrame.Fields...)
	return frame, nil
}

func ParseKdbObjectAsFrame(res *kdb.K) (*data.Frame, error) {
	frame := data.NewFrame("response")
	values, err := kdbObjectColumn(res)
	if err != nil {
		return nil, err
	}
	frame.Fields = append(frame.Fields, data.NewField("value", nil, values))
	return frame, nil
}

func ParseKdbDictAsFrame(res *kdb.K) (*data.Frame, error) {
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
	depth := dictFrameDepth(values)
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
			columns[colIndex] = append(columns[colIndex], kdbCellValue(value))
		}
	}
	frame := data.NewFrame("response")
	for i, name := range columnNames {
		frame.Fields = append(frame.Fields, data.NewField(name, nil, typedInterfaceColumn(columns[i])))
	}
	return frame, nil
}

func dictColumnNames(keys *kdb.K) ([]string, error) {
	switch keys.Type {
	case -kdb.KS:
		return []string{keys.Data.(string)}, nil
	case kdb.KS:
		return keys.Data.([]string), nil
	case kdb.KC:
		return []string{keys.Data.(string)}, nil
	case kdb.K0:
		items := keys.Data.([]*kdb.K)
		names := make([]string, len(items))
		for i, item := range items {
			names[i] = kdbObjectString(item)
		}
		return names, nil
	default:
		return nil, fmt.Errorf("unsupported dictionary key type %v", keys.Type)
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

func kdbCellValue(value *kdb.K) interface{} {
	if value == nil {
		return nil
	}
	if value.Type < kdb.K0 {
		return kdbAtomValue(value)
	}
	if value.Type == kdb.KC {
		return value.Data.(string)
	}
	if value.Type > kdb.K0 && value.Type <= kdb.KT && value.Len() == 1 {
		if item, ok := correctedIndex(value, 0).(*kdb.K); ok {
			return kdbAtomValue(item)
		}
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
	if list, ok := values.Data.([]*kdb.K); ok {
		return list, nil
	}
	if keyCount == 1 {
		return []*kdb.K{values}, nil
	}
	if values.Type > kdb.K0 && values.Type <= kdb.KT && values.Len() == keyCount {
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

func dictFrameDepth(values []*kdb.K) int {
	depth := 1
	for _, value := range values {
		if value == nil || value.Type < kdb.K0 {
			continue
		}
		if value.Type == kdb.KC {
			continue
		}
		if value.Len() > depth {
			depth = value.Len()
		}
	}
	return depth
}

func kdbObjectColumn(value *kdb.K) (interface{}, error) {
	return kdbObjectColumnWithDepth(value, kdbObjectDepth(value))
}

func kdbObjectColumnWithDepth(value *kdb.K, depth int) (interface{}, error) {
	if value == nil {
		return []interface{}{nil}, nil
	}
	if value.Type < kdb.K0 {
		return projectAtom(kdbAtomValue(value), depth), nil
	}
	if value.Type == kdb.KC {
		if depth <= 1 {
			return []string{value.Data.(string)}, nil
		}
		return projectAtom(value.Data.(string), depth), nil
	}
	if value.Type == kdb.K0 {
		if strings, err := stringParser(value); err == nil {
			return resizeColumn(strings, depth), nil
		}
		items := value.Data.([]*kdb.K)
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = kdbObjectString(item)
		}
		return resizeColumn(out, depth), nil
	}
	return resizeParsedColumn(standardColumnParser(value), depth), nil
}

func kdbObjectDepth(value *kdb.K) int {
	if value == nil || value.Type < kdb.K0 || value.Type == kdb.KC {
		return 1
	}
	return value.Len()
}

func kdbAtomValue(value *kdb.K) interface{} {
	if value.Type == -kdb.KC {
		return string(value.Data.(byte))
	}
	return value.Data
}

func kdbObjectString(value *kdb.K) string {
	if value == nil {
		return ""
	}
	if value.Type == -kdb.KC {
		return string(value.Data.(byte))
	}
	if value.Type == kdb.KC {
		return value.Data.(string)
	}
	return fmt.Sprint(value.Data)
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
	kdbDict := res.Data.(kdb.Dict)
	if kdbDict.Key.Type != kdb.XT || kdbDict.Value.Type != kdb.XT {
		return nil, fmt.Errorf("Either the key or the value of the returned dictionary object is not a table of type 98.")
	}
	rc := kdbDict.Key.Len()
	valData := kdbDict.Value.Data.(kdb.Table)
	frameArray := make([]*data.Frame, rc)
	k := kdbDict.Key.Data.(kdb.Table)
	keyColCount := len(k.Columns)
	for row := 0; row < rc; row++ {
		keyData := correctedTableIndex(k, row)
		frameName := parseFrameName(keyData.Value)
		frame := data.NewFrame(frameName)
		rowData := correctedTableIndex(valData, row)
		depth, err := getDepth(rowData.Value.Data.([]*kdb.K))
		if err != nil {
			return nil, err
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
		for i, colName := range masterCols {
			KObj := masterData[i]
			var dat interface{}
			if KObj.Type < 0 {
				if KObj.Type == -kdb.KC {
					KObj.Data = string(KObj.Data.(byte))
				}
				dat = projectAtom(KObj.Data, depth)
			} else {
				switch {
				case KObj.Type == kdb.KC:
					// if the column is a key column, this is a string. Otherwise it is a char list
					if i < keyColCount || KObj.Len() != depth {
						dat = projectAtom(KObj.Data, depth)
					} else {
						dat = charParser(KObj)
					}
				case KObj.Type > kdb.K0:
					dat = standardColumnParser(KObj)
				case KObj.Type == kdb.K0:
					stringColumn, err := stringParser(KObj)
					if err != nil {
						return nil, fmt.Errorf("Error parsing data of type K0")
					}
					dat = stringColumn
				}
			}
			frame.Fields = append(frame.Fields, data.NewField(colName, nil, dat))
		}
		frameArray[row] = frame
	}
	return frameArray, nil
}

func parseFrameName(key *kdb.K) string {
	// handling for homogenous dictionaries
	var frameNameArray []string
	if key.Type != kdb.K0 {
		if key.Type == kdb.KC {
			for _, l := range key.Data.([]interface{}) {
				frameNameArray = append(frameNameArray, string(l.(byte)))
			}
		} else {
			for _, val := range key.Data.([]interface{}) {
				frameNameArray = append(frameNameArray, fmt.Sprint(val))
			}
		}
		// handling for heterogenous dictionaries
	} else {
		for _, obj := range key.Data.([]*kdb.K) {
			if obj.Type == -kdb.KC {
				frameNameArray = append(frameNameArray, string(obj.Data.(byte)))
			} else {
				frameNameArray = append(frameNameArray, fmt.Sprint(obj.Data))
			}
		}
	}
	// concat all key strings together
	return strings.Join(frameNameArray, " - ")
}

func getDepth(colArray []*kdb.K) (int, error) {
	d := -1
	aggPresent := false
	for _, K := range colArray {
		if K.Type < 0 {
			aggPresent = true
			continue
		}
		if K.Type == kdb.KC {
			continue
		}
		if d == -1 {
			d = K.Len()
			continue
		}
		if d != K.Len() {
			return 0, fmt.Errorf("Columns are present of non-equal length")
		}
	}
	if d == -1 {
		if aggPresent {
			return 1, nil
		}
		return 0, fmt.Errorf("At least one key's value is an empty list '()'")
	}
	return d, nil
}

func correctedIndex(k *kdb.K, i int) interface{} {
	if k.Type < kdb.K0 || k.Type > kdb.XT {
		return nil
	}
	if k.Len() == 0 {
		// need to return null of that type
		if k.Type == kdb.K0 {
			return &kdb.K{kdb.K0, kdb.NONE, make([]*kdb.K, 0)}
		}
		return nil

	}
	if k.Type == kdb.K0 {
		return k.Data.([]*kdb.K)[i]
	}
	if k.Type > kdb.K0 && k.Type <= kdb.KT {
		return indexKdbArray(k, i)
	}
	// case for table
	// need to return dict with header
	if k.Type != kdb.XT {
		return nil
	}
	var t = k.Data.(kdb.Table)
	return &kdb.K{kdb.XD, kdb.NONE, correctedTableIndex(t, i)}
}

func indexKdbArray(k *kdb.K, i int) interface{} {
	switch {
	case k.Type == kdb.KB:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]bool)[i]}
	case k.Type == kdb.UU:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]uuid.UUID)[i]}
	case k.Type == kdb.KG:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]byte)[i]}
	case k.Type == kdb.KH:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]int16)[i]}
	case k.Type == kdb.KI:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]int32)[i]}
	case k.Type == kdb.KJ:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]int64)[i]}
	case k.Type == kdb.KE:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]float32)[i]}
	case k.Type == kdb.KF:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]float64)[i]}
	case k.Type == kdb.KC:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.(string)[i]}
	case k.Type == kdb.KS:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]string)[i]}
	case k.Type == kdb.KP:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]time.Time)[i]}
	case k.Type == kdb.KM:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]kdb.Month)[i]}
	case k.Type == kdb.KD:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]time.Time)[i]}
	case k.Type == kdb.KZ:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]time.Time)[i]}
	case k.Type == kdb.KN:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]time.Duration)[i]}
	case k.Type == kdb.KU:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]kdb.Minute)[i]}
	case k.Type == kdb.KV:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]kdb.Second)[i]}
	case k.Type == kdb.KT:
		return &kdb.K{-k.Type, kdb.NONE, k.Data.([]kdb.Time)[i]}
	}
	return nil
}

func correctedTableIndex(tbl kdb.Table, i int) kdb.Dict {
	var d = kdb.Dict{}
	d.Key = &kdb.K{kdb.KS, kdb.NONE, tbl.Columns}
	vslice := make([]*kdb.K, len(tbl.Columns))
	d.Value = &kdb.K{kdb.K0, kdb.NONE, vslice}
	for ci := range tbl.Columns {
		kd := correctedIndex(tbl.Data[ci], i)
		vslice[ci] = kd.(*kdb.K)
	}
	return d
}

func projectAtom(a interface{}, d int) interface{} {
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
		panic(fmt.Errorf("field '%s' specified with unsupported type %T", a, v))
	}
	return o
}
