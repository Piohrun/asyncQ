package plugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	kdb "github.com/sv/kdbgo"
)

var panopticonFormattedMacroPattern = regexp.MustCompile(`\{(TimeWindowStart|TimeWindowEnd|Snapshot|FocusTime|Start|End|From|To):([^{}]+)\}`)

func prepareQueryForExecution(pCtx backend.PluginContext, query backend.DataQuery, model *QueryModel) error {
	if model.OriginalQueryText == "" {
		model.OriginalQueryText = model.QueryText
	}
	if model.CompatibilityMode != CompatibilityModePanopticon {
		return nil
	}

	compiledQuery := expandPanopticonMacros(model.QueryText, pCtx, query)
	if wrapper := strings.TrimSpace(model.PanopticonQueryWrapper); wrapper != "" {
		if strings.Count(wrapper, "{Query}") != 1 {
			return fmt.Errorf("Panopticon query wrapper must contain exactly one {Query} placeholder")
		}
		expandedWrapper := expandPanopticonMacros(wrapper, pCtx, query)
		compiledQuery = strings.Replace(expandedWrapper, "{Query}", compiledQuery, 1)
	}
	model.QueryText = compiledQuery
	return nil
}

func expandPanopticonMacros(input string, pCtx backend.PluginContext, query backend.DataQuery) string {
	if input == "" {
		return ""
	}
	input = expandPanopticonFormattedMacros(input, query)
	replacer := strings.NewReplacer(panopticonMacroPairs(pCtx, query)...)
	return replacer.Replace(input)
}

func panopticonMacroPairs(pCtx backend.PluginContext, query backend.DataQuery) []string {
	from := query.TimeRange.From
	to := query.TimeRange.To
	focus := to
	intervalNs := int64(query.Interval)
	intervalMs := int64(query.Interval / time.Millisecond)

	userName, userLogin, userEmail := "", "", ""
	if pCtx.User != nil {
		userName = pCtx.User.Name
		userLogin = pCtx.User.Login
		userEmail = pCtx.User.Email
	}

	datasourceName, datasourceUID := "", ""
	if pCtx.DataSourceInstanceSettings != nil {
		datasourceName = pCtx.DataSourceInstanceSettings.Name
		datasourceUID = pCtx.DataSourceInstanceSettings.UID
	}

	return []string{
		"{TimeWindowStart}", qTimestampLiteral(from),
		"{TimeWindowEnd}", qTimestampLiteral(to),
		"{Snapshot}", qTimestampLiteral(to),
		"{FocusTime}", qTimestampLiteral(focus),
		"{Start}", qTimestampLiteral(from),
		"{End}", qTimestampLiteral(to),
		"{From}", qTimestampLiteral(from),
		"{To}", qTimestampLiteral(to),
		"{TimeWindowStartText}", qStringLiteral(timeText(from)),
		"{TimeWindowEndText}", qStringLiteral(timeText(to)),
		"{SnapshotText}", qStringLiteral(timeText(to)),
		"{FocusTimeText}", qStringLiteral(timeText(focus)),
		"{Interval}", qLongLiteral(intervalNs),
		"{IntervalNs}", qLongLiteral(intervalNs),
		"{IntervalMs}", qLongLiteral(intervalMs),
		"{MaxDataPoints}", qLongLiteral(query.MaxDataPoints),
		"{RefID}", qStringLiteral(query.RefID),
		"{OrgID}", qLongLiteral(pCtx.OrgID),
		"{UserName}", qStringLiteral(userName),
		"{UserLogin}", qStringLiteral(userLogin),
		"{UserEmail}", qStringLiteral(userEmail),
		"{DatasourceName}", qStringLiteral(datasourceName),
		"{DatasourceUID}", qStringLiteral(datasourceUID),
		"$TimeWindowStart", qTimestampLiteral(from),
		"$TimeWindowEnd", qTimestampLiteral(to),
		"$Snapshot", qTimestampLiteral(to),
		"$FocusTime", qTimestampLiteral(focus),
	}
}

func expandPanopticonFormattedMacros(input string, query backend.DataQuery) string {
	return panopticonFormattedMacroPattern.ReplaceAllStringFunc(input, func(token string) string {
		matches := panopticonFormattedMacroPattern.FindStringSubmatch(token)
		if len(matches) != 3 {
			return token
		}
		t, ok := panopticonTimeMacroValue(matches[1], query)
		if !ok {
			return token
		}
		if t.IsZero() {
			return qStringLiteral("")
		}
		return qStringLiteral(t.UTC().Format(panopticonDateLayout(matches[2])))
	})
}

func panopticonTimeMacroValue(name string, query backend.DataQuery) (time.Time, bool) {
	switch name {
	case "TimeWindowStart", "Start", "From":
		return query.TimeRange.From, true
	case "TimeWindowEnd", "Snapshot", "FocusTime", "End", "To":
		return query.TimeRange.To, true
	default:
		return time.Time{}, false
	}
}

func panopticonDateLayout(format string) string {
	replacer := strings.NewReplacer(
		"yyyy", "2006",
		"YYYY", "2006",
		"SSSSSSSSS", "000000000",
		"SSSSSS", "000000",
		"SSS", "000",
		"MM", "01",
		"dd", "02",
		"DD", "02",
		"HH", "15",
		"mm", "04",
		"ss", "05",
		"XXX", "Z07:00",
	)
	return replacer.Replace(format)
}

func qTimestampLiteral(t time.Time) string {
	if t.IsZero() {
		return "0Np"
	}
	return t.UTC().Format("2006.01.02D15:04:05.000000000")
}

func qLongLiteral(value int64) string {
	return strconv.FormatInt(value, 10) + "j"
}

func qStringLiteral(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + replacer.Replace(value) + "\""
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func describeKdbObject(k *kdb.K) string {
	if k == nil {
		return "nil"
	}
	parts := []string{fmt.Sprintf("type=%d", k.Type)}
	if l := k.Len(); l >= 0 {
		parts = append(parts, fmt.Sprintf("len=%d", l))
	}
	switch k.Type {
	case kdb.XT:
		t := k.Data.(kdb.Table)
		parts = append(parts, fmt.Sprintf("columns=%v", t.Columns))
	case kdb.XD:
		d := k.Data.(kdb.Dict)
		parts = append(parts, fmt.Sprintf("keyType=%d", d.Key.Type), fmt.Sprintf("valueType=%d", d.Value.Type))
	case kdb.K0:
		items, ok := k.Data.([]*kdb.K)
		if ok {
			limit := len(items)
			if limit > 5 {
				limit = 5
			}
			types := make([]int8, limit)
			for i := 0; i < limit; i++ {
				if items[i] != nil {
					types[i] = items[i].Type
				}
			}
			parts = append(parts, fmt.Sprintf("itemTypes=%v", types))
		}
	}
	return strings.Join(parts, " ")
}
