package plugin

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type cacheResourceRequest struct {
	Scope string `json:"scope,omitempty"`
	Key   string `json:"key,omitempty"`
}

type cacheResourceResponse struct {
	OK            bool                  `json:"ok"`
	Scope         string                `json:"scope,omitempty"`
	Key           string                `json:"key,omitempty"`
	MemoryRemoved int                   `json:"memoryRemoved,omitempty"`
	DiskRemoved   int                   `json:"diskRemoved,omitempty"`
	Status        *syncQueryCacheStatus `json:"status,omitempty"`
	Error         string                `json:"error,omitempty"`
}

type resourceErrorResponse struct {
	OK    bool   `json:"ok"`
	Role  string `json:"role,omitempty"`
	Error string `json:"error"`
}

type resourceEndpointAdmission struct {
	method       string
	maxBodyBytes int
	mediaType    string
	allowForm    bool
}

const (
	maxResourceJSONDepth           = 32
	maxResourceJSONCollectionItems = 1000000
	maxResourceJSONNodes           = maxResourceJSONCollectionItems + 2
	maxResourceJSONMapEntries      = 65536
	maxResourceJSONCacheKeys       = 65536
	maxResourceJSONFrames          = 4096
	maxResourceJSONFields          = 65536
	maxResourceJSONCells           = 1000000

	resourceJSONIntegerCellBytes = 24
	resourceJSONFloatCellBytes   = 64
	resourceJSONBooleanCellBytes = 6
	resourceJSONTimeCellBytes    = 48
	resourceJSONEnumCellBytes    = 24
	resourceJSONStringCellBytes  = 5
	resourceJSONRawCellBytes     = 5

	resourceJSONMediaType = "application/json"
	resourceFormMediaType = "application/x-www-form-urlencoded"

	maxResourceContentTypeBytes = 256
)

type resourceJSONVisit struct {
	typ reflect.Type
	ptr uintptr
}

type resourceJSONBudget struct {
	limit           int64
	used            int64
	nodes           int64
	collectionItems int64
	mapEntries      int64
	frames          int64
	fields          int64
	cells           int64
	active          map[resourceJSONVisit]struct{}
}

var resourceEndpoints = map[string]resourceEndpointAdmission{
	"cache/status":         {method: http.MethodGet},
	"cache/entries":        {method: http.MethodGet},
	"cache/clear":          {method: http.MethodPost, maxBodyBytes: maxQueryJSONBytes},
	"cache/clear-entry":    {method: http.MethodPost, maxBodyBytes: maxQueryJSONBytes},
	"cache/clear-expired":  {method: http.MethodPost, maxBodyBytes: maxQueryJSONBytes},
	"async/run-and-wait":   {method: http.MethodPost, maxBodyBytes: maxQueryJSONBytes},
	"report/catalog":       {method: http.MethodGet},
	"report/validate":      {method: http.MethodGet},
	"report/download":      {method: http.MethodGet},
	"report/generate":      {method: http.MethodPost, maxBodyBytes: maxResourceJSONBytes, allowForm: true},
	"report/generate-link": {method: http.MethodPost, maxBodyBytes: maxResourceJSONBytes, allowForm: true},
}

func (d *KdbDatasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) (err error) {
	if d == nil {
		return backend.PluginErrorf("resource call failed: datasource is nil")
	}
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()
	if req == nil {
		return backend.PluginErrorf("resource call failed: request is nil")
	}
	if resourceSenderIsNil(sender) {
		return backend.PluginErrorf("resource call failed: sender is nil")
	}
	endpoint, status, err := admitResourceRequest(req)
	if err != nil {
		if status == http.StatusMethodNotAllowed {
			return sendResourceJSONWithHeaders(sender, status, resourceErrorResponse{OK: false, Error: err.Error()}, map[string][]string{
				"allow": {endpoint.method},
			})
		}
		return sendResourceJSON(sender, status, resourceErrorResponse{OK: false, Error: err.Error()})
	}
	role, err := validatedResourceRole(req.PluginContext)
	if err != nil {
		return sendResourceJSON(sender, http.StatusBadRequest, resourceErrorResponse{OK: false, Error: "plugin context failed validation"})
	}
	d.normalizeDatasourceDefaults()

	switch req.Path {
	case "cache/status":
		return d.sendSyncQueryCacheResourceStatus(sender, false)
	case "cache/entries":
		if !resourceRoleCanEdit(role) {
			return sendResourceJSON(sender, http.StatusForbidden, resourceErrorResponse{OK: false, Error: "cache entries require an Admin or Editor role"})
		}
		return d.sendSyncQueryCacheResourceStatus(sender, true)
	case "cache/clear":
		if !d.QueryCacheControlEnabled || !resourceRoleCanEdit(role) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, "cache/clear", &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, resourceErrorResponse{OK: false, Error: err.Error()})
		}
		resp := d.clearSyncQueryCache(body.Scope)
		return sendResourceJSON(sender, http.StatusOK, resp)
	case "cache/clear-entry":
		if !d.QueryCacheControlEnabled || !resourceRoleCanEdit(role) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, "cache/clear-entry", &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, resourceErrorResponse{OK: false, Error: err.Error()})
		}
		resp, status := d.clearSyncQueryCacheEntry(body.Scope, body.Key)
		return sendResourceJSON(sender, status, resp)
	case "cache/clear-expired":
		if !d.QueryCacheControlEnabled || !resourceRoleCanEdit(role) {
			return sendCacheControlDenied(sender, req.PluginContext)
		}
		var body cacheResourceRequest
		if err := decodeCacheResourceRequest(req.Body, "cache/clear-expired", &body); err != nil {
			return sendResourceJSON(sender, http.StatusBadRequest, resourceErrorResponse{OK: false, Error: err.Error()})
		}
		resp := d.clearExpiredSyncQueryCache(body.Scope)
		return sendResourceJSON(sender, http.StatusOK, resp)
	case "async/run-and-wait":
		return d.handleAsyncRunAndWaitResource(ctx, req, sender)
	case "report/validate":
		if !resourceRoleCanEdit(role) {
			return sendResourceJSON(sender, http.StatusForbidden, resourceErrorResponse{OK: false, Error: "report validation requires an Admin or Editor role"})
		}
		return d.handleExcelReportResource(ctx, req, sender, req.Path, endpoint.mediaType)
	default:
		if strings.HasPrefix(req.Path, "report/") {
			return d.handleExcelReportResource(ctx, req, sender, req.Path, endpoint.mediaType)
		}
		return sendResourceJSON(sender, http.StatusNotFound, resourceErrorResponse{OK: false, Error: "unknown resource path"})
	}
}

func admitResourceRequest(req *backend.CallResourceRequest) (resourceEndpointAdmission, int, error) {
	if req == nil {
		return resourceEndpointAdmission{}, http.StatusBadRequest, fmt.Errorf("resource request is nil")
	}
	if len(req.Path) == 0 || len(req.Path) > maxResourcePathBytes || !utf8.ValidString(req.Path) {
		return resourceEndpointAdmission{}, http.StatusNotFound, fmt.Errorf("resource path is invalid")
	}
	for _, r := range req.Path {
		if unicode.IsControl(r) {
			return resourceEndpointAdmission{}, http.StatusNotFound, fmt.Errorf("resource path is invalid")
		}
	}
	endpoint, ok := resourceEndpoints[req.Path]
	if !ok {
		return resourceEndpointAdmission{}, http.StatusNotFound, fmt.Errorf("unknown resource path")
	}
	if len(req.URL) > maxResourceURLBytes || !utf8.ValidString(req.URL) {
		return endpoint, http.StatusBadRequest, fmt.Errorf("resource URL is invalid or too long")
	}
	for _, r := range req.URL {
		if unicode.IsControl(r) {
			return endpoint, http.StatusBadRequest, fmt.Errorf("resource URL is invalid")
		}
	}
	if req.Method != endpoint.method {
		return endpoint, http.StatusMethodNotAllowed, fmt.Errorf("resource method is not allowed")
	}
	if endpoint.method == http.MethodGet {
		if len(req.Body) != 0 {
			return endpoint, http.StatusBadRequest, fmt.Errorf("GET resource requests must have an empty body")
		}
		return endpoint, http.StatusOK, nil
	}
	if len(req.Body) > endpoint.maxBodyBytes {
		return endpoint, http.StatusRequestEntityTooLarge, fmt.Errorf("resource request body exceeds the endpoint limit")
	}
	mediaType, err := admitResourceContentType(req, endpoint.allowForm)
	if err != nil {
		return endpoint, http.StatusUnsupportedMediaType, err
	}
	endpoint.mediaType = mediaType
	return endpoint, http.StatusOK, nil
}

func admitResourceContentType(req *backend.CallResourceRequest, allowForm bool) (string, error) {
	if req == nil {
		return "", fmt.Errorf("resource Content-Type is unsupported")
	}
	values := req.GetHTTPHeaders().Values("Content-Type")
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > maxResourceContentTypeBytes {
		return "", fmt.Errorf("resource Content-Type is unsupported")
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil {
		return "", fmt.Errorf("resource Content-Type is unsupported")
	}
	if mediaType != resourceJSONMediaType && (!allowForm || mediaType != resourceFormMediaType) {
		return "", fmt.Errorf("resource Content-Type is unsupported")
	}
	for key, value := range parameters {
		if !strings.EqualFold(key, "charset") || !strings.EqualFold(value, "utf-8") {
			return "", fmt.Errorf("resource Content-Type is unsupported")
		}
	}
	if len(parameters) > 1 {
		return "", fmt.Errorf("resource Content-Type is unsupported")
	}
	return mediaType, nil
}

func validatedResourceRole(pCtx backend.PluginContext) (string, error) {
	if err := validatePluginContextExpansionInputs(pCtx); err != nil {
		return "", err
	}
	if pCtx.User == nil || pCtx.User.Role == "" {
		return "", nil
	}
	role := pCtx.User.Role
	if len(role) > 32 || !utf8.ValidString(role) || strings.TrimSpace(role) != role {
		return "", fmt.Errorf("invalid user role")
	}
	for _, r := range role {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("invalid user role")
		}
	}
	return strings.ToLower(role), nil
}

func resourceRoleCanEdit(role string) bool {
	return role == "admin" || role == "editor"
}

func sendCacheControlDenied(sender backend.CallResourceResponseSender, pCtx backend.PluginContext) error {
	role := ""
	if pCtx.User != nil {
		role = pCtx.User.Role
	}
	return sendResourceJSON(sender, http.StatusForbidden, resourceErrorResponse{
		OK:    false,
		Error: "cache controls require datasource cache-control enablement and an Admin or Editor role",
		Role:  role,
	})
}

var cacheResourceJSONFieldSpecs = map[string]jsonFieldSpec{
	"scope": {kind: jsonStringValue, maxStringBytes: 16},
	"key":   {kind: jsonStringValue, maxStringBytes: 64},
}

func decodeCacheResourceRequest(raw []byte, path string, target *cacheResourceRequest) error {
	if target == nil {
		return fmt.Errorf("cache request target is nil")
	}
	fields, err := scanTopLevelJSONObject(raw, maxQueryJSONBytes, cacheResourceJSONFieldSpecs, false, "cache request JSON")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("cache request JSON could not be decoded")
	}
	if _, ok := fields["scope"]; !ok {
		return fmt.Errorf("cache request scope is required")
	}
	switch target.Scope {
	case "memory", "disk", "both":
	default:
		return fmt.Errorf("cache request scope must be memory, disk, or both")
	}
	switch path {
	case "cache/clear", "cache/clear-expired":
		if _, ok := fields["key"]; ok {
			return fmt.Errorf("cache request key is not valid for this endpoint")
		}
	case "cache/clear-entry":
		if _, ok := fields["key"]; !ok {
			return fmt.Errorf("cache request key is required")
		}
		if len(target.Key) != 64 {
			return fmt.Errorf("cache request key must be a canonical lowercase cache key")
		}
		for index := 0; index < len(target.Key); index++ {
			c := target.Key[index]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return fmt.Errorf("cache request key must be a canonical lowercase cache key")
			}
		}
		if _, err := syncQueryDiskCacheFileName(target.Key); err != nil {
			return fmt.Errorf("cache request key must be a canonical lowercase cache key")
		}
	default:
		return fmt.Errorf("unsupported cache request endpoint")
	}
	return nil
}

func sendResourceJSON(sender backend.CallResourceResponseSender, status int, obj interface{}) error {
	return sendResourceJSONWithHeaders(sender, status, obj, nil)
}

func sendResourceJSONWithHeaders(sender backend.CallResourceResponseSender, status int, obj interface{}, extra map[string][]string) error {
	if resourceSenderIsNil(sender) {
		return backend.PluginErrorf("resource response sender is nil")
	}
	if _, err := estimateResourceJSONSize(obj, maxResourceResponseJSON); err != nil {
		return backend.PluginErrorf("resource JSON response failed preflight: %v", err)
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if len(body) > maxResourceResponseJSON {
		return backend.PluginErrorf("resource JSON response exceeds the %d-byte limit", maxResourceResponseJSON)
	}
	headers := map[string][]string{
		"content-type":           {"application/json"},
		"cache-control":          {"no-store"},
		"x-content-type-options": {"nosniff"},
		"referrer-policy":        {"no-referrer"},
	}
	for key, values := range extra {
		headers[key] = append([]string(nil), values...)
	}
	return sender.Send(&backend.CallResourceResponse{
		Status:  status,
		Headers: headers,
		Body:    body,
	})
}

func resourceSenderIsNil(sender backend.CallResourceResponseSender) bool {
	if sender == nil {
		return true
	}
	value := reflect.ValueOf(sender)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var (
	resourceJSONMarshalerType     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	resourceTextMarshalerType     = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	resourceTimeType              = reflect.TypeOf(time.Time{})
	resourceRawMessageType        = reflect.TypeOf(json.RawMessage{})
	resourceFramesType            = reflect.TypeOf(data.Frames{})
	resourceFrameType             = reflect.TypeOf(data.Frame{})
	resourceLabelsType            = reflect.TypeOf(data.Labels{})
	resourceValueMappingsType     = reflect.TypeOf(data.ValueMappings{})
	resourceConfFloat64Type       = reflect.TypeOf(data.ConfFloat64(0))
	resourceNoticeSeverityType    = reflect.TypeOf(data.NoticeSeverity(0))
	resourceFieldTypeType         = reflect.TypeOf(data.FieldType(0))
	resourceAsyncResponseType     = reflect.TypeOf(asyncRunAndWaitResponse{})
	resourceCacheStatusType       = reflect.TypeOf(syncQueryCacheStatus{})
	resourceCacheResponseType     = reflect.TypeOf(cacheResourceResponse{})
	resourceErrorResponseType     = reflect.TypeOf(resourceErrorResponse{})
	resourceJSONRawMessagePtrType = reflect.PointerTo(resourceRawMessageType)
)

func estimateResourceJSONSize(obj interface{}, limit int) (size int64, err error) {
	if obj == nil {
		return 0, fmt.Errorf("response value is nil")
	}
	if limit <= 0 {
		return 0, fmt.Errorf("response JSON limit is invalid")
	}
	budget := &resourceJSONBudget{
		limit:  int64(limit),
		active: make(map[resourceJSONVisit]struct{}),
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			size = 0
			err = fmt.Errorf("response contains an invalid or unsupported value")
		}
	}()
	if err := budget.addValue(reflect.ValueOf(obj), 1); err != nil {
		return 0, err
	}
	return budget.used, nil
}

func (b *resourceJSONBudget) addValue(value reflect.Value, depth int) error {
	if depth > maxResourceJSONDepth {
		return fmt.Errorf("response exceeds the maximum JSON depth of %d", maxResourceJSONDepth)
	}
	if err := b.addCount(&b.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
		return err
	}
	if !value.IsValid() {
		return b.addSize(4)
	}

	typ := value.Type()
	switch typ {
	case resourceTimeType:
		return b.addSize(64)
	case resourceRawMessageType:
		return b.addRawJSON(value.Interface().(json.RawMessage), depth)
	case resourceFramesType:
		return b.addFrames(value, value.Interface().(data.Frames), depth)
	case reflect.PointerTo(resourceFramesType):
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addFrames(value, value.Elem().Interface().(data.Frames), depth)
	case resourceFrameType:
		return fmt.Errorf("response contains an address-sensitive data.Frame value; use *data.Frame or data.Frames")
	case reflect.PointerTo(resourceFrameType):
		if value.IsNil() {
			return fmt.Errorf("response contains a nil data frame")
		}
		return b.addFrame(value, value.Interface().(*data.Frame), depth)
	case resourceLabelsType:
		return b.addLabels(value, value.Interface().(data.Labels))
	case reflect.PointerTo(resourceLabelsType):
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addLabels(value, value.Elem().Interface().(data.Labels))
	case resourceValueMappingsType:
		return b.addValueMappings(value, value.Interface().(data.ValueMappings), depth)
	case reflect.PointerTo(resourceValueMappingsType):
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addValueMappings(value, value.Elem().Interface().(data.ValueMappings), depth)
	case resourceConfFloat64Type:
		return b.addSize(32)
	case resourceNoticeSeverityType, resourceFieldTypeType:
		return b.addSize(64)
	case reflect.PointerTo(resourceConfFloat64Type):
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addSize(32)
	case reflect.PointerTo(resourceNoticeSeverityType), reflect.PointerTo(resourceFieldTypeType):
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addSize(64)
	case resourceAsyncResponseType:
		response := value.Interface().(asyncRunAndWaitResponse)
		if len(response.Statuses) > maxAsyncStatusEvents {
			return fmt.Errorf("async response status count exceeds %d", maxAsyncStatusEvents)
		}
		return b.addStruct(value, depth)
	case reflect.PointerTo(resourceAsyncResponseType):
		if value.IsNil() {
			return fmt.Errorf("async response is nil")
		}
		response := value.Elem().Interface().(asyncRunAndWaitResponse)
		if len(response.Statuses) > maxAsyncStatusEvents {
			return fmt.Errorf("async response status count exceeds %d", maxAsyncStatusEvents)
		}
		release, err := b.enter(value)
		if err != nil {
			return err
		}
		defer release()
		return b.addStruct(value.Elem(), depth)
	case resourceCacheStatusType:
		status := value.Interface().(syncQueryCacheStatus)
		if err := validateResourceCacheStatusCollections(status); err != nil {
			return err
		}
		return b.addStruct(value, depth)
	case reflect.PointerTo(resourceCacheStatusType):
		if value.IsNil() {
			return fmt.Errorf("cache status is nil")
		}
		status := value.Elem().Interface().(syncQueryCacheStatus)
		if err := validateResourceCacheStatusCollections(status); err != nil {
			return err
		}
		release, err := b.enter(value)
		if err != nil {
			return err
		}
		defer release()
		return b.addStruct(value.Elem(), depth)
	case resourceCacheResponseType, resourceErrorResponseType:
		return b.addStruct(value, depth)
	}
	if typ == resourceJSONRawMessagePtrType {
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addRawJSON(value.Elem().Interface().(json.RawMessage), depth)
	}
	if typ.Kind() == reflect.Pointer && typ.Elem() == resourceTimeType {
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addSize(64)
	}
	if resourceJSONTypeHasCustomMarshaler(typ) {
		return fmt.Errorf("response contains unsupported custom JSON encoding for %s", typ.String())
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return b.addSize(4)
		}
		return b.addValue(value.Elem(), depth+1)
	case reflect.Pointer:
		if value.IsNil() {
			return b.addSize(4)
		}
		release, err := b.enter(value)
		if err != nil {
			return err
		}
		defer release()
		return b.addValue(value.Elem(), depth+1)
	case reflect.Bool:
		return b.addSize(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return b.addSize(24)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return b.addSize(24)
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("response contains a non-finite number")
		}
		return b.addSize(32)
	case reflect.String:
		return b.addJSONString(value.String())
	case reflect.Struct:
		return b.addStruct(value, depth)
	case reflect.Map:
		return b.addMap(value, depth)
	case reflect.Slice:
		return b.addSlice(value, depth)
	case reflect.Array:
		return b.addArray(value, depth)
	default:
		return fmt.Errorf("response contains unsupported JSON value kind %s", value.Kind())
	}
}

func (b *resourceJSONBudget) addStruct(value reflect.Value, depth int) error {
	if err := b.addSize(2); err != nil {
		return err
	}
	typ := value.Type()
	firstField := true
	for index := 0; index < value.NumField(); index++ {
		fieldType := typ.Field(index)
		tag := fieldType.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if fieldType.PkgPath != "" {
			embeddedType := fieldType.Type
			if embeddedType.Kind() == reflect.Pointer {
				embeddedType = embeddedType.Elem()
			}
			if fieldType.Anonymous && embeddedType.Kind() == reflect.Struct {
				return fmt.Errorf("response contains an unsupported unexported anonymous struct field %s", fieldType.Name)
			}
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if !resourceJSONValidTag(name) {
			name = fieldType.Name
		}
		if !firstField {
			if err := b.addSize(1); err != nil {
				return err
			}
		}
		firstField = false
		if err := b.addJSONString(name); err != nil {
			return err
		}
		if err := b.addSize(1); err != nil {
			return err
		}
		if err := b.addValue(value.Field(index), depth+1); err != nil {
			return err
		}
		if strings.Contains(","+options+",", ",string,") &&
			resourceJSONStringOptionApplies(fieldType.Type) {
			additional, err := resourceJSONStringOptionAdditionalBytes(value.Field(index))
			if err != nil {
				return err
			}
			if err := b.addSize(additional); err != nil {
				return err
			}
		}
	}
	return nil
}

func resourceJSONStringOptionApplies(typ reflect.Type) bool {
	if typ.Name() == "" && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

func resourceJSONStringOptionAdditionalBytes(value reflect.Value) (int64, error) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, nil
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.String {
		_, additional, err := resourceJSONStringEncodingSizes(value.String())
		return additional, err
	}
	return 2, nil
}

func resourceJSONValidTag(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", character):
		case unicode.IsLetter(character), unicode.IsDigit(character):
		default:
			return false
		}
	}
	return true
}

func (b *resourceJSONBudget) addMap(value reflect.Value, depth int) error {
	if value.IsNil() {
		return b.addSize(4)
	}
	keyType := value.Type().Key()
	if keyType.Kind() != reflect.String && keyType.Implements(resourceTextMarshalerType) {
		return fmt.Errorf("response contains unsupported custom map-key encoding for %s", keyType.String())
	}
	switch keyType.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
	default:
		return fmt.Errorf("response contains unsupported JSON map key type %s", keyType.String())
	}
	if err := b.addCount(&b.mapEntries, int64(value.Len()), maxResourceJSONMapEntries, "map entries"); err != nil {
		return err
	}
	if err := b.addCollection(value.Len()); err != nil {
		return err
	}
	release, err := b.enter(value)
	if err != nil {
		return err
	}
	defer release()
	if err := b.addSize(2); err != nil {
		return err
	}
	iterator := value.MapRange()
	for iterator.Next() {
		if err := b.addMapKey(iterator.Key()); err != nil {
			return err
		}
		if err := b.addSize(2); err != nil {
			return err
		}
		if err := b.addValue(iterator.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addMapKey(key reflect.Value) error {
	switch key.Kind() {
	case reflect.String:
		return b.addJSONString(key.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return b.addJSONString(strconv.FormatInt(key.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return b.addJSONString(strconv.FormatUint(key.Uint(), 10))
	default:
		return fmt.Errorf("response contains unsupported JSON map key type %s", key.Type().String())
	}
}

func (b *resourceJSONBudget) addSlice(value reflect.Value, depth int) error {
	if value.IsNil() {
		return b.addSize(4)
	}
	if value.Type().Elem().Kind() == reflect.Uint8 {
		elementType := value.Type().Elem()
		if resourceJSONTypeHasCustomMarshaler(elementType) {
			return fmt.Errorf("response contains unsupported custom byte-element encoding for %s", elementType.String())
		}
		length := int64(value.Len())
		if length > math.MaxInt64-2 {
			return fmt.Errorf("response byte slice is too large")
		}
		groups := (length + 2) / 3
		if groups > (math.MaxInt64-2)/4 {
			return fmt.Errorf("response byte slice is too large")
		}
		return b.addSize(groups*4 + 2)
	}
	if err := b.addCollection(value.Len()); err != nil {
		return err
	}
	release, err := b.enter(value)
	if err != nil {
		return err
	}
	defer release()
	if err := b.addSize(2); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if err := b.addSize(1); err != nil {
			return err
		}
		if err := b.addValue(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addArray(value reflect.Value, depth int) error {
	if err := b.addCollection(value.Len()); err != nil {
		return err
	}
	if err := b.addSize(2); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if err := b.addSize(1); err != nil {
			return err
		}
		if err := b.addValue(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addFrames(value reflect.Value, frames data.Frames, depth int) error {
	if frames == nil {
		return b.addSize(4)
	}
	if err := b.addCount(&b.frames, int64(len(frames)), maxResourceJSONFrames, "data frames"); err != nil {
		return err
	}
	if err := b.addCollection(len(frames)); err != nil {
		return err
	}
	release, err := b.enter(value)
	if err != nil {
		return err
	}
	defer release()
	if err := b.addSize(2); err != nil {
		return err
	}
	for _, frame := range frames {
		if frame == nil {
			return fmt.Errorf("response contains a nil data frame")
		}
		if err := b.addFrame(reflect.ValueOf(frame), frame, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addFrame(value reflect.Value, frame *data.Frame, depth int) error {
	if frame == nil {
		return fmt.Errorf("response contains a nil data frame")
	}
	if depth > maxResourceJSONDepth {
		return fmt.Errorf("response exceeds the maximum JSON depth of %d", maxResourceJSONDepth)
	}
	if err := b.addCount(&b.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
		return err
	}
	if err := b.addCount(&b.fields, int64(len(frame.Fields)), maxResourceJSONFields, "data frame fields"); err != nil {
		return err
	}
	if err := b.addCollection(len(frame.Fields)); err != nil {
		return err
	}
	var release func()
	if value.IsValid() && value.Kind() == reflect.Pointer {
		var err error
		release, err = b.enter(value)
		if err != nil {
			return err
		}
		defer release()
	}
	for _, field := range frame.Fields {
		if field == nil {
			return fmt.Errorf("response contains a nil data frame field")
		}
	}
	rowCount, err := frame.RowLen()
	if err != nil {
		return fmt.Errorf("response data frame is invalid")
	}
	if err := b.addSize(512); err != nil {
		return err
	}
	if err := b.addJSONString(frame.Name); err != nil {
		return err
	}
	if err := b.addJSONString(frame.RefID); err != nil {
		return err
	}
	if frame.Meta != nil {
		if err := b.addValue(reflect.ValueOf(frame.Meta), depth+1); err != nil {
			return err
		}
	}
	for _, field := range frame.Fields {
		if err := b.addSize(256); err != nil {
			return err
		}
		if err := b.addJSONString(field.Name); err != nil {
			return err
		}
		if field.Labels != nil {
			if err := b.addLabels(reflect.ValueOf(field.Labels), field.Labels); err != nil {
				return err
			}
		}
		if field.Config != nil {
			if err := b.addValue(reflect.ValueOf(field.Config), depth+1); err != nil {
				return err
			}
		}
		if err := b.addFrameField(field, rowCount, depth+4); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addFrameField(field *data.Field, rowCount int, valueDepth int) error {
	if err := b.addCount(&b.cells, int64(rowCount), maxResourceJSONCells, "data frame cells"); err != nil {
		return err
	}
	fieldType := field.Type()
	var perCell int64
	var variableKind data.FieldType
	switch fieldType {
	case data.FieldTypeInt8, data.FieldTypeNullableInt8,
		data.FieldTypeInt16, data.FieldTypeNullableInt16,
		data.FieldTypeInt32, data.FieldTypeNullableInt32,
		data.FieldTypeInt64, data.FieldTypeNullableInt64,
		data.FieldTypeUint8, data.FieldTypeNullableUint8,
		data.FieldTypeUint16, data.FieldTypeNullableUint16,
		data.FieldTypeUint32, data.FieldTypeNullableUint32,
		data.FieldTypeUint64, data.FieldTypeNullableUint64:
		perCell = resourceJSONIntegerCellBytes
	case data.FieldTypeFloat32, data.FieldTypeNullableFloat32,
		data.FieldTypeFloat64, data.FieldTypeNullableFloat64:
		perCell = resourceJSONFloatCellBytes
	case data.FieldTypeBool, data.FieldTypeNullableBool:
		perCell = resourceJSONBooleanCellBytes
	case data.FieldTypeTime, data.FieldTypeNullableTime:
		perCell = resourceJSONTimeCellBytes
	case data.FieldTypeEnum, data.FieldTypeNullableEnum:
		perCell = resourceJSONEnumCellBytes
	case data.FieldTypeString, data.FieldTypeNullableString:
		perCell = resourceJSONStringCellBytes
		variableKind = data.FieldTypeString
	case data.FieldTypeJSON, data.FieldTypeNullableJSON:
		perCell = resourceJSONRawCellBytes
		variableKind = data.FieldTypeJSON
	default:
		return fmt.Errorf("response contains an unsupported data frame field type")
	}
	if err := b.addRepeated(int64(rowCount), perCell); err != nil {
		return err
	}
	if variableKind == 0 {
		return nil
	}
	for index := 0; index < rowCount; index++ {
		value, present := field.ConcreteAt(index)
		if !present {
			continue
		}
		switch variableKind {
		case data.FieldTypeString:
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("response contains an invalid string data frame cell")
			}
			if err := b.addJSONString(text); err != nil {
				return err
			}
		case data.FieldTypeJSON:
			raw, ok := value.(json.RawMessage)
			if !ok {
				return fmt.Errorf("response contains an invalid JSON data frame cell")
			}
			if err := b.addRawJSON(raw, valueDepth); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *resourceJSONBudget) addLabels(value reflect.Value, labels data.Labels) error {
	if labels == nil {
		return b.addSize(4)
	}
	if err := b.addCount(&b.mapEntries, int64(len(labels)), maxResourceJSONMapEntries, "map entries"); err != nil {
		return err
	}
	if err := b.addCollection(len(labels)); err != nil {
		return err
	}
	release, err := b.enter(value)
	if err != nil {
		return err
	}
	defer release()
	if err := b.addSize(2); err != nil {
		return err
	}
	for key, label := range labels {
		if err := b.addJSONString(key); err != nil {
			return err
		}
		if err := b.addJSONString(label); err != nil {
			return err
		}
		if err := b.addSize(2); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addValueMappings(value reflect.Value, mappings data.ValueMappings, depth int) error {
	if mappings == nil {
		return b.addSize(4)
	}
	if err := b.addCollection(len(mappings)); err != nil {
		return err
	}
	release, err := b.enter(value)
	if err != nil {
		return err
	}
	defer release()
	if err := b.addSize(2); err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping == nil {
			return fmt.Errorf("response contains a nil value mapping")
		}
		mappingValue := reflect.ValueOf(mapping)
		if mappingValue.Kind() == reflect.Pointer && mappingValue.IsNil() {
			return fmt.Errorf("response contains a typed-nil value mapping")
		}
		if err := b.addSize(64); err != nil {
			return err
		}
		if err := b.addValue(mappingValue, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *resourceJSONBudget) addJSONString(value string) error {
	size, _, err := resourceJSONStringEncodingSizes(value)
	if err != nil {
		return err
	}
	return b.addSize(size)
}

func resourceJSONStringEncodingSizes(value string) (encoded int64, stringOptionAdditional int64, err error) {
	// A ,string field is encoded once as JSON, then that first encoding is
	// quoted again. The second pass must re-escape every backslash and quote
	// introduced by the first pass.
	encoded = 2
	stringOptionAdditional = 4
	add := func(target *int64, amount int64) error {
		if amount < 0 || *target > math.MaxInt64-amount {
			return fmt.Errorf("response JSON size accounting overflow")
		}
		*target += amount
		return nil
	}
	for index := 0; index < len(value); {
		character := value[index]
		if character < utf8.RuneSelf {
			var encodedBytes int64
			var requotedBytes int64
			switch character {
			case '"', '\\':
				encodedBytes = 2
				requotedBytes = 2
			case '\b', '\f', '\n', '\r', '\t':
				encodedBytes = 2
				requotedBytes = 1
			default:
				switch {
				case character < 0x20, character == '<', character == '>', character == '&':
					encodedBytes = 6
					requotedBytes = 1
				default:
					encodedBytes = 1
				}
			}
			if err := add(&encoded, encodedBytes); err != nil {
				return 0, 0, err
			}
			if err := add(&stringOptionAdditional, requotedBytes); err != nil {
				return 0, 0, err
			}
			index++
			continue
		}

		runeValue, runeBytes := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && runeBytes == 1 {
			if err := add(&encoded, 6); err != nil {
				return 0, 0, err
			}
			if err := add(&stringOptionAdditional, 1); err != nil {
				return 0, 0, err
			}
			index++
			continue
		}
		if runeValue == '\u2028' || runeValue == '\u2029' {
			if err := add(&encoded, 6); err != nil {
				return 0, 0, err
			}
			if err := add(&stringOptionAdditional, 1); err != nil {
				return 0, 0, err
			}
		} else if err := add(&encoded, int64(runeBytes)); err != nil {
			return 0, 0, err
		}
		index += runeBytes
	}
	return encoded, stringOptionAdditional, nil
}

func (b *resourceJSONBudget) addRawJSON(raw json.RawMessage, baseDepth int) error {
	if raw == nil {
		return b.addSize(4)
	}
	encodedSize, err := resourceRawJSONEncodedSizeUpperBound(raw)
	if err != nil {
		return err
	}
	if encodedSize < 0 || b.used > b.limit-encodedSize {
		return fmt.Errorf("response JSON exceeds the %d-byte limit", b.limit)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("response raw JSON must contain valid UTF-8")
	}
	if baseDepth > maxResourceJSONDepth {
		return fmt.Errorf("response exceeds the maximum JSON depth of %d", maxResourceJSONDepth)
	}

	parser := resourceRawJSONParser{
		raw:       raw,
		budget:    b,
		baseDepth: baseDepth,
	}
	parser.skipWhitespace()
	if parser.index == len(raw) {
		return fmt.Errorf("response contains invalid raw JSON")
	}
	if err := parser.parseValue(0); err != nil {
		return err
	}
	parser.skipWhitespace()
	if parser.index != len(raw) {
		return fmt.Errorf("response contains trailing raw JSON")
	}
	return b.addSize(encodedSize)
}

type resourceRawJSONParser struct {
	raw       []byte
	index     int
	budget    *resourceJSONBudget
	baseDepth int
}

func (p *resourceRawJSONParser) parseValue(containerDepth int) error {
	if p.index >= len(p.raw) {
		return fmt.Errorf("response contains invalid raw JSON")
	}
	switch p.raw[p.index] {
	case '{':
		if p.baseDepth+containerDepth > maxResourceJSONDepth {
			return fmt.Errorf("response exceeds the maximum JSON depth of %d", maxResourceJSONDepth)
		}
		if err := p.budget.addCount(&p.budget.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
			return err
		}
		return p.parseObject(containerDepth)
	case '[':
		if p.baseDepth+containerDepth > maxResourceJSONDepth {
			return fmt.Errorf("response exceeds the maximum JSON depth of %d", maxResourceJSONDepth)
		}
		if err := p.budget.addCount(&p.budget.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
			return err
		}
		return p.parseArray(containerDepth)
	case '"':
		if err := p.budget.addCount(&p.budget.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
			return err
		}
		return p.parseString()
	case 't':
		return p.parseLiteral("true")
	case 'f':
		return p.parseLiteral("false")
	case 'n':
		return p.parseLiteral("null")
	default:
		if p.raw[p.index] == '-' || (p.raw[p.index] >= '0' && p.raw[p.index] <= '9') {
			if err := p.budget.addCount(&p.budget.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
				return err
			}
			return p.parseNumber()
		}
		return fmt.Errorf("response contains invalid raw JSON")
	}
}

func (p *resourceRawJSONParser) parseObject(containerDepth int) error {
	p.index++
	p.skipWhitespace()
	if p.consume('}') {
		return nil
	}
	for {
		if p.index >= len(p.raw) || p.raw[p.index] != '"' {
			return fmt.Errorf("response contains invalid raw JSON")
		}
		if err := p.parseString(); err != nil {
			return err
		}
		if err := p.budget.addCount(&p.budget.mapEntries, 1, maxResourceJSONMapEntries, "map entries"); err != nil {
			return err
		}
		if err := p.budget.addCollection(1); err != nil {
			return err
		}
		p.skipWhitespace()
		if !p.consume(':') {
			return fmt.Errorf("response contains invalid raw JSON")
		}
		p.skipWhitespace()
		if err := p.parseValue(containerDepth + 1); err != nil {
			return err
		}
		p.skipWhitespace()
		if p.consume('}') {
			return nil
		}
		if !p.consume(',') {
			return fmt.Errorf("response contains invalid raw JSON")
		}
		p.skipWhitespace()
	}
}

func (p *resourceRawJSONParser) parseArray(containerDepth int) error {
	p.index++
	p.skipWhitespace()
	if p.consume(']') {
		return nil
	}
	for {
		if err := p.budget.addCollection(1); err != nil {
			return err
		}
		if err := p.parseValue(containerDepth + 1); err != nil {
			return err
		}
		p.skipWhitespace()
		if p.consume(']') {
			return nil
		}
		if !p.consume(',') {
			return fmt.Errorf("response contains invalid raw JSON")
		}
		p.skipWhitespace()
	}
}

func (p *resourceRawJSONParser) parseString() error {
	if !p.consume('"') {
		return fmt.Errorf("response contains invalid raw JSON")
	}
	for p.index < len(p.raw) {
		character := p.raw[p.index]
		p.index++
		switch character {
		case '"':
			return nil
		case '\\':
			if p.index >= len(p.raw) {
				return fmt.Errorf("response contains invalid raw JSON")
			}
			escaped := p.raw[p.index]
			p.index++
			switch escaped {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if len(p.raw)-p.index < 4 {
					return fmt.Errorf("response contains invalid raw JSON")
				}
				for end := p.index + 4; p.index < end; p.index++ {
					if !isResourceJSONHex(p.raw[p.index]) {
						return fmt.Errorf("response contains invalid raw JSON")
					}
				}
			default:
				return fmt.Errorf("response contains invalid raw JSON")
			}
		default:
			if character < 0x20 {
				return fmt.Errorf("response contains invalid raw JSON")
			}
		}
	}
	return fmt.Errorf("response contains invalid raw JSON")
}

func (p *resourceRawJSONParser) parseLiteral(literal string) error {
	if len(p.raw)-p.index < len(literal) || string(p.raw[p.index:p.index+len(literal)]) != literal {
		return fmt.Errorf("response contains invalid raw JSON")
	}
	if err := p.budget.addCount(&p.budget.nodes, 1, maxResourceJSONNodes, "JSON nodes"); err != nil {
		return err
	}
	p.index += len(literal)
	return nil
}

func (p *resourceRawJSONParser) parseNumber() error {
	if p.consume('-') && p.index >= len(p.raw) {
		return fmt.Errorf("response contains invalid raw JSON")
	}
	if p.consume('0') {
		if p.index < len(p.raw) && p.raw[p.index] >= '0' && p.raw[p.index] <= '9' {
			return fmt.Errorf("response contains invalid raw JSON")
		}
	} else {
		if p.index >= len(p.raw) || p.raw[p.index] < '1' || p.raw[p.index] > '9' {
			return fmt.Errorf("response contains invalid raw JSON")
		}
		for p.index < len(p.raw) && p.raw[p.index] >= '0' && p.raw[p.index] <= '9' {
			p.index++
		}
	}
	if p.consume('.') {
		start := p.index
		for p.index < len(p.raw) && p.raw[p.index] >= '0' && p.raw[p.index] <= '9' {
			p.index++
		}
		if p.index == start {
			return fmt.Errorf("response contains invalid raw JSON")
		}
	}
	if p.index < len(p.raw) && (p.raw[p.index] == 'e' || p.raw[p.index] == 'E') {
		p.index++
		if p.index < len(p.raw) && (p.raw[p.index] == '+' || p.raw[p.index] == '-') {
			p.index++
		}
		start := p.index
		for p.index < len(p.raw) && p.raw[p.index] >= '0' && p.raw[p.index] <= '9' {
			p.index++
		}
		if p.index == start {
			return fmt.Errorf("response contains invalid raw JSON")
		}
	}
	return nil
}

func (p *resourceRawJSONParser) skipWhitespace() {
	for p.index < len(p.raw) {
		switch p.raw[p.index] {
		case ' ', '\t', '\n', '\r':
			p.index++
		default:
			return
		}
	}
}

func (p *resourceRawJSONParser) consume(character byte) bool {
	if p.index >= len(p.raw) || p.raw[p.index] != character {
		return false
	}
	p.index++
	return true
}

func isResourceJSONHex(character byte) bool {
	return (character >= '0' && character <= '9') ||
		(character >= 'a' && character <= 'f') ||
		(character >= 'A' && character <= 'F')
}

func resourceRawJSONEncodedSizeUpperBound(raw json.RawMessage) (int64, error) {
	size := int64(len(raw))
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '<', '>', '&':
			if size > math.MaxInt64-5 {
				return 0, fmt.Errorf("response JSON size accounting overflow")
			}
			size += 5
		case 0xe2:
			if index+2 < len(raw) && raw[index+1] == 0x80 && (raw[index+2] == 0xa8 || raw[index+2] == 0xa9) {
				if size > math.MaxInt64-3 {
					return 0, fmt.Errorf("response JSON size accounting overflow")
				}
				size += 3
			}
		}
	}
	return size, nil
}

func (b *resourceJSONBudget) addCollection(count int) error {
	if count < 0 {
		return fmt.Errorf("response contains an invalid collection")
	}
	return b.addCount(&b.collectionItems, int64(count), maxResourceJSONCollectionItems, "collection items")
}

func (b *resourceJSONBudget) addRepeated(count int64, perItem int64) error {
	if count < 0 || perItem < 0 {
		return fmt.Errorf("response JSON size accounting overflow")
	}
	if count != 0 && perItem > math.MaxInt64/count {
		return fmt.Errorf("response JSON size accounting overflow")
	}
	return b.addSize(count * perItem)
}

func (b *resourceJSONBudget) addSize(delta int64) error {
	if delta < 0 || b.used > b.limit-delta {
		return fmt.Errorf("response JSON exceeds the %d-byte limit", b.limit)
	}
	b.used += delta
	return nil
}

func (b *resourceJSONBudget) addCount(target *int64, delta int64, maximum int64, label string) error {
	if delta < 0 || *target > maximum-delta {
		return fmt.Errorf("response %s exceed %d", label, maximum)
	}
	*target += delta
	return nil
}

func (b *resourceJSONBudget) enter(value reflect.Value) (func(), error) {
	var pointer uintptr
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return func() {}, nil
		}
		pointer = value.Pointer()
	default:
		return func() {}, nil
	}
	if pointer == 0 {
		return func() {}, nil
	}
	visit := resourceJSONVisit{typ: value.Type(), ptr: pointer}
	if _, exists := b.active[visit]; exists {
		return nil, fmt.Errorf("response contains a cyclic value")
	}
	b.active[visit] = struct{}{}
	return func() {
		delete(b.active, visit)
	}, nil
}

func resourceJSONTypeHasCustomMarshaler(typ reflect.Type) bool {
	if typ.Implements(resourceJSONMarshalerType) || typ.Implements(resourceTextMarshalerType) {
		return true
	}
	if typ.Kind() != reflect.Pointer {
		pointer := reflect.PointerTo(typ)
		return pointer.Implements(resourceJSONMarshalerType) || pointer.Implements(resourceTextMarshalerType)
	}
	return false
}

func validateResourceCacheStatusCollections(status syncQueryCacheStatus) error {
	memoryKeys := int64(len(status.Memory.Keys))
	diskKeys := int64(len(status.Disk.Keys))
	if memoryKeys > maxResourceJSONCacheKeys || diskKeys > maxResourceJSONCacheKeys-memoryKeys {
		return fmt.Errorf("cache status key count exceeds %d", maxResourceJSONCacheKeys)
	}
	return nil
}

const cacheResourceStatusUnavailable = "cache status is temporarily unavailable"

func (d *KdbDatasource) buildSyncQueryCacheResourceStatus(includeKeys bool) (syncQueryCacheStatus, error) {
	policy := d.syncQueryCachePolicy(QueryModel{})
	status := syncQueryCacheStatus{
		Enabled:           policy.enabled,
		ControlEnabled:    d.QueryCacheControlEnabled,
		TTLSeconds:        int64(policy.ttl / time.Second),
		StaleTTLSeconds:   int64(policy.staleTTL / time.Second),
		TimeBucketSeconds: int64(policy.timeBucket / time.Second),
		KeyMode:           policy.keyMode,
		Memory: syncQueryMemoryStatus{
			Enabled:    policy.enabled,
			MaxEntries: d.QueryCacheMaxEntries,
		},
		Disk: syncQueryDiskStatus{
			Enabled:    policy.enabled && policy.disk.enabled,
			MaxEntries: policy.disk.maxEntries,
			MaxBytes:   policy.disk.maxBytes,
		},
	}

	remainingKeys := maxResourceJSONCacheKeys
	if cache := d.currentSyncQueryCache(); cache != nil {
		memory, err := cache.resourceStatus(policy, includeKeys, remainingKeys)
		if err != nil {
			return syncQueryCacheStatus{}, err
		}
		status.Memory = memory
		if includeKeys {
			remainingKeys -= len(memory.Keys)
		}
	}
	if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
		disk, err := diskCache.resourceStatus(policy, includeKeys, remainingKeys)
		if err != nil {
			return syncQueryCacheStatus{}, err
		}
		status.Disk = disk
	}
	if err := validateResourceCacheStatusCollections(status); err != nil {
		return syncQueryCacheStatus{}, err
	}
	return status, nil
}

func (d *KdbDatasource) sendSyncQueryCacheResourceStatus(sender backend.CallResourceResponseSender, includeKeys bool) error {
	status, err := d.buildSyncQueryCacheResourceStatus(includeKeys)
	if err != nil {
		d.logDiagnosticError("cache resource status failed", "error", err.Error())
		return sendResourceJSON(sender, http.StatusServiceUnavailable, resourceErrorResponse{
			OK:    false,
			Error: cacheResourceStatusUnavailable,
		})
	}
	return sendResourceJSON(sender, http.StatusOK, status)
}

func (d *KdbDatasource) attachSyncQueryCacheResourceStatus(resp *cacheResourceResponse) {
	if resp == nil {
		return
	}
	status, err := d.buildSyncQueryCacheResourceStatus(false)
	if err != nil {
		d.logDiagnosticError("cache resource status failed after cache control", "error", err.Error())
		resp.OK = false
		if resp.Error == "" {
			resp.Error = cacheResourceStatusUnavailable
		}
		return
	}
	resp.Status = &status
}

func (d *KdbDatasource) currentSyncQueryCache() *syncQueryCache {
	d.queryCacheMu.Lock()
	defer d.queryCacheMu.Unlock()
	return d.queryCache
}

func (d *KdbDatasource) clearSyncQueryCache(scope string) cacheResourceResponse {
	scope = normalizeCacheResourceScope(scope)
	resp := cacheResourceResponse{OK: true, Scope: scope}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil {
			resp.MemoryRemoved = cache.clearCount()
		}
	}
	if scope == "disk" || scope == "both" {
		policy := d.syncQueryCachePolicy(QueryModel{})
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clear()
			resp.DiskRemoved = removed
			if err != nil {
				d.logDiagnosticError("disk cache clear failed", "error", err.Error())
				resp.OK = false
				resp.Error = "disk cache operation failed"
			}
		}
	}
	d.attachSyncQueryCacheResourceStatus(&resp)
	return resp
}

func (d *KdbDatasource) clearSyncQueryCacheEntry(scope string, key string) (cacheResourceResponse, int) {
	scope = normalizeCacheResourceScope(scope)
	key = strings.ToLower(strings.TrimSpace(key))
	resp := cacheResourceResponse{OK: true, Scope: scope, Key: key}
	if _, err := syncQueryDiskCacheFileName(key); err != nil {
		resp.OK = false
		resp.Error = "cache request key is invalid"
		return resp, http.StatusBadRequest
	}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil && cache.clearKey(key) {
			resp.MemoryRemoved = 1
		}
	}
	if scope == "disk" || scope == "both" {
		policy := d.syncQueryCachePolicy(QueryModel{})
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clearKey(key)
			if removed {
				resp.DiskRemoved = 1
			}
			if err != nil {
				d.logDiagnosticError("disk cache entry clear failed", "error", err.Error())
				resp.OK = false
				resp.Error = "disk cache operation failed"
			}
		}
	}
	d.attachSyncQueryCacheResourceStatus(&resp)
	return resp, http.StatusOK
}

func (d *KdbDatasource) clearExpiredSyncQueryCache(scope string) cacheResourceResponse {
	scope = normalizeCacheResourceScope(scope)
	policy := d.syncQueryCachePolicy(QueryModel{})
	resp := cacheResourceResponse{OK: true, Scope: scope}
	if scope == "memory" || scope == "both" {
		if cache := d.currentSyncQueryCache(); cache != nil {
			resp.MemoryRemoved = cache.clearExpired(policy)
		}
	}
	if scope == "disk" || scope == "both" {
		if diskCache := d.syncQueryDiskCache(policy); diskCache != nil {
			removed, err := diskCache.clearExpired(policy)
			resp.DiskRemoved = removed
			if err != nil {
				d.logDiagnosticError("expired disk cache clear failed", "error", err.Error())
				resp.OK = false
				resp.Error = "disk cache operation failed"
			}
		}
	}
	d.attachSyncQueryCacheResourceStatus(&resp)
	return resp
}

func normalizeCacheResourceScope(scope string) string {
	switch scope {
	case "memory":
		return "memory"
	case "disk":
		return "disk"
	case "both":
		return "both"
	default:
		return ""
	}
}
