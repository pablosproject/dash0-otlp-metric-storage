package metrics

import (
	"fmt"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// resourceServiceName extracts the service.name from resource attributes, returning "" if not found.
func resourceServiceName(resource *resourcepb.Resource) string {
	if resource == nil {
		return ""
	}
	for _, attribute := range resource.GetAttributes() {
		if attribute.GetKey() == "service.name" {
			return attribute.GetValue().GetStringValue()
		}
	}
	return ""
}

// kvToMap converts a slice of OTLP KeyValue pairs to a Go map.
func kvToMap(attributes []*commonpb.KeyValue) map[string]string {
	result := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		result[attribute.GetKey()] = anyValueToString(attribute.GetValue())
	}
	return result
}

// anyValueToString converts an OTLP AnyValue to its string representation.
func anyValueToString(value *commonpb.AnyValue) string {
	if value == nil {
		return ""
	}
	switch value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return value.GetStringValue()
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", value.GetIntValue())
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", value.GetDoubleValue())
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", value.GetBoolValue())
	default:
		return fmt.Sprintf("%v", value)
	}
}

// nanosToTime converts a uint64 nanoseconds-since-epoch to time.Time.
func nanosToTime(nanos uint64) time.Time {
	return time.Unix(0, int64(nanos))
}

// numberDataPointValue extracts the float64 value from a NumberDataPoint.
func numberDataPointValue(datapoint *metricspb.NumberDataPoint) float64 {
	switch value := datapoint.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return value.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(value.AsInt)
	default:
		return 0
	}
}

// metricRows holds the rows produced from one Export request.
type metricRows struct {
	catalog []CatalogRow
	gauges  []DatapointRow
	sums    []DatapointRow
}

type catalogKey struct {
	dayUnix    int64
	metricHash uint64
}

func (row CatalogRow) catalogKey() catalogKey {
	return catalogKey{dayUnix: row.Day.Unix(), metricHash: row.MetricHash}
}

type rowMapper struct {
	rows         metricRows
	catalogIndex map[catalogKey]int
}

func mapExportRequest(request *colmetricspb.ExportMetricsServiceRequest) metricRows {
	m := rowMapper{catalogIndex: make(map[catalogKey]int)}

	for _, resourceMetrics := range request.GetResourceMetrics() {
		serviceName := resourceServiceName(resourceMetrics.GetResource())
		resourceAttributes := kvToMap(resourceMetrics.GetResource().GetAttributes())

		for _, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			scope := scopeMetrics.GetScope()
			scopeAttributes := kvToMap(scope.GetAttributes())

			for _, metric := range scopeMetrics.GetMetrics() {
				base := CatalogRow{
					ServiceName:           serviceName,
					MetricName:            metric.GetName(),
					MetricUnit:            metric.GetUnit(),
					MetricDescription:     metric.GetDescription(),
					ResourceAttributes:    resourceAttributes,
					ResourceSchemaUrl:     resourceMetrics.GetSchemaUrl(),
					ScopeName:             scope.GetName(),
					ScopeVersion:          scope.GetVersion(),
					ScopeAttributes:       scopeAttributes,
					ScopeSchemaUrl:        scopeMetrics.GetSchemaUrl(),
					ScopeDroppedAttrCount: scope.GetDroppedAttributesCount(),
				}
				datapoints, ok := numberDataPoints(metric, &base)
				if !ok {
					continue
				}
				for _, datapoint := range datapoints {
					m.addDatapoint(base, datapoint)
				}
			}
		}
	}
	return m.rows
}

func numberDataPoints(metric *metricspb.Metric, base *CatalogRow) (datapoints []*metricspb.NumberDataPoint, ok bool) {
	switch data := metric.GetData().(type) {
	case *metricspb.Metric_Gauge:
		base.MetricType = MetricTypeGauge
		return data.Gauge.GetDataPoints(), true
	case *metricspb.Metric_Sum:
		base.MetricType = MetricTypeSum
		base.AggregationTemporality = int32(data.Sum.GetAggregationTemporality())
		base.IsMonotonic = data.Sum.GetIsMonotonic()
		return data.Sum.GetDataPoints(), true
	default:
		return nil, false
	}
}

// addDatapoint adds the datapoint row and the catalog row of its series. The
// catalog keeps one row per (Day, MetricHash): the last one seen wins.
func (m *rowMapper) addDatapoint(catalogRow CatalogRow, datapoint *metricspb.NumberDataPoint) {
	catalogRow.DataPointAttributes = kvToMap(datapoint.GetAttributes())
	catalogRow.MetricHash = metricHash(&catalogRow)
	timeUnix := nanosToTime(datapoint.GetTimeUnixNano())
	catalogRow.Day = utcDay(timeUnix)

	key := catalogRow.catalogKey()
	if i, ok := m.catalogIndex[key]; ok {
		m.rows.catalog[i] = catalogRow
	} else {
		m.catalogIndex[key] = len(m.rows.catalog)
		m.rows.catalog = append(m.rows.catalog, catalogRow)
	}

	row := DatapointRow{
		TimeUnix:      timeUnix,
		MetricHash:    catalogRow.MetricHash,
		StartTimeUnix: nanosToTime(datapoint.GetStartTimeUnixNano()),
		Value:         numberDataPointValue(datapoint),
		Flags:         datapoint.GetFlags(),
	}
	if catalogRow.MetricType == MetricTypeGauge {
		m.rows.gauges = append(m.rows.gauges, row)
	} else {
		m.rows.sums = append(m.rows.sums, row)
	}
}

// utcDay returns the UTC calendar day of t, as stored in the catalog Day column.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
