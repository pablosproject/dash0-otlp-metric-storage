package metrics

import (
	"encoding/binary"
	"maps"
	"slices"

	"github.com/cespare/xxhash/v2"
)

const fieldSeparator byte = 0xff

// metricHash computes the MetricHash of the series described by a catalog row.
// Only identity fields are hashed (see ADR §4.1): Scope, schema URLs,
// MetricDescription and ServiceName are ignored. Changing the encoding changes
// every MetricHash and splits the history of all series.
func metricHash(r *CatalogRow) uint64 {
	b := make([]byte, 0, 256)
	b = appendString(b, r.MetricName)
	b = append(b, fieldSeparator)
	b = appendString(b, r.MetricUnit)
	b = append(b, fieldSeparator, byte(r.MetricType), fieldSeparator)
	b = binary.BigEndian.AppendUint32(b, uint32(r.AggregationTemporality))
	b = append(b, fieldSeparator, boolByte(r.IsMonotonic), fieldSeparator)
	b = appendAttributes(b, r.ResourceAttributes)
	b = append(b, fieldSeparator)
	b = appendAttributes(b, r.DataPointAttributes)
	return xxhash.Sum64(b)
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendAttributes(b []byte, attrs map[string]string) []byte {
	b = binary.AppendUvarint(b, uint64(len(attrs)))
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		b = appendString(b, k)
		b = appendString(b, attrs[k])
	}
	return b
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}
