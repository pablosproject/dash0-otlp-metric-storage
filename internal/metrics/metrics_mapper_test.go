package metrics

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func strVal(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func intVal(i int64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}}
}

func kv(k string, v *commonpb.AnyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: v}
}

func TestAnyValueToString(t *testing.T) {
	tests := map[string]struct {
		in   *commonpb.AnyValue
		want string
	}{
		"nil value": {in: nil, want: ""},
		"string":    {in: strVal("abc"), want: "abc"},
		"int":       {in: intVal(123), want: "123"},
		"double":    {in: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}, want: "1.5"},
		"bool":      {in: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}, want: "true"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := anyValueToString(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKvToMap_SameTextDifferentTypeIsSameSeries(t *testing.T) {
	a, b := baseSeriesRow(), baseSeriesRow()
	a.DataPointAttributes = kvToMap([]*commonpb.KeyValue{kv("cpu", intVal(1))})
	b.DataPointAttributes = kvToMap([]*commonpb.KeyValue{kv("cpu", strVal("1"))})
	if metricHash(&a) != metricHash(&b) {
		t.Errorf("expected int 1 and string \"1\" to give the same MetricHash")
	}
}
