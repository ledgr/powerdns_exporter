package main

import (
	"fmt"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

// Used to programmatically create prometheus.Gauge metrics
type gaugeDefinition struct {
	id   int
	name string
	desc string
	key  string
}

// Used to programmatically create prometheus.Counter metrics
type counterDefinition struct {
	id    int
	name  string
	desc  string
	label string
	// Maps PowerDNS stats names to Prometheus label value
	labelMap map[string]string
}

var (
	rTimeBucketMap = map[string]float64{
		"answers0-1":      .001,
		"answers1-10":     .01,
		"answers10-100":   .1,
		"answers100-1000": 1,
		"answers-slow":    0,
	}

	rTimeLabelMap = map[string]string{
		"answers0-1":      "0_1ms",
		"answers1-10":     "1_10ms",
		"answers10-100":   "10_100ms",
		"answers100-1000": "100_1000ms",
		"answers-slow":    "over_1000ms",
	}

	rCodeLabelMap = map[string]string{
		"servfail-answers": "servfail",
		"nxdomain-answers": "nxdomain",
		"noerror-answers":  "noerror",
	}

	exceptionsLabelMap = map[string]string{
		"resource-limits":     "resource_limit",
		"over-capacity-drops": "over_capacity_drop",
		"unreachables":        "ns_unreachable",
		"outgoing-timeouts":   "outgoing_timeout",
	}
)

// PowerDNS recursor metrics definitions
var (
	recursorGaugeDefs = []gaugeDefinition{
		gaugeDefinition{1, "latency_average_seconds", "Exponential moving average of question-to-answer latency.", "qa-latency"},
		gaugeDefinition{2, "concurrent_queries", "Number of concurrent queries.", "concurrent-queries"},
		gaugeDefinition{3, "cache_size", "Number of entries in the cache.", "cache-entries"},
	}

	recursorCounterDefs = []counterDefinition{
		counterDefinition{
			1, "incoming_queries_total", "Total number of incoming queries by network.", "net",
			map[string]string{"questions": "udp", "tcp-questions": "tcp"},
		},
		counterDefinition{
			2, "outgoing_queries_total", "Total number of outgoing queries by network.", "net",
			map[string]string{"all-outqueries": "udp", "tcp-outqueries": "tcp"},
		},
		counterDefinition{
			3, "cache_lookups_total", "Total number of cache lookups by result.", "result",
			map[string]string{"cache-hits": "hit", "cache-misses": "miss"},
		},
		counterDefinition{4, "answers_rcodes_total", "Total number of answers by response code.", "rcode", rCodeLabelMap},
		counterDefinition{5, "answers_rtime_total", "Total number of answers grouped by response time slots.", "timeslot", rTimeLabelMap},
		counterDefinition{6, "exceptions_total", "Total number of exceptions by error.", "error", exceptionsLabelMap},
	}
)

// PowerDNS authoritative server metrics definitions
var (
	authoritativeGaugeDefs = []gaugeDefinition{
		gaugeDefinition{1, "security_status", "PDNS Server Security status based on security-status.secpoll.powerdns.com", "security-status"},
		gaugeDefinition{2, "latency_average_seconds", "Average number of microseconds a packet spends within PowerDNS", "latency"},
		gaugeDefinition{3, "packet_cache_size", "Number of entries in the packet cache.", "packetcache-size"},
		gaugeDefinition{4, "signature_cache_size", "Number of entries in the signature cache.", "signature-cache-size"},
		gaugeDefinition{5, "key_cache_size", "Number of entries in the key cache.", "key-cache-size"},
		gaugeDefinition{6, "metadata_cache_size", "Number of entries in the metadata cache.", "meta-cache-size"},
		gaugeDefinition{7, "qsize", "Number of packets waiting for database attention.", "qsize-q"},
	}
	authoritativeCounterDefs = []counterDefinition{
		counterDefinition{
			1, "incoming_notifications", "Number of NOTIFY packets that were received", "type",
			map[string]string{},
		},
		counterDefinition{
			2, "uptime", "Uptime in seconds of the daemon", "type",
			map[string]string{"uptime": "seconds"},
		},
		counterDefinition{
			3, "dnssec", "DNSSEC counters", "type",
			map[string]string{"signatures": "signatures_created", "udp-do-queries": "ok_queries_recv"},
		},
		counterDefinition{
			4, "packet_cache_lookup", "Packet cache lookups by result", "result",
			map[string]string{"packetcache-hit": "hit", "packetcache-miss": "miss"},
		},
		counterDefinition{
			5, "query_cache_lookup", "Query cache lookups by result", "result",
			map[string]string{"query-cache-hit": "hit", "query-cache-miss": "miss"},
		},
		counterDefinition{
			6, "deferred_cache_actions", "Deferred cache actions because of maintenance by type", "type",
			map[string]string{"deferred-cache-inserts": "inserts", "deferred-cache-lookup": "lookups"},
		},
		counterDefinition{
			7, "dnsupdate_queries_total", "Total number of DNS update queries by status.", "status",
			map[string]string{"dnsupdate-answers": "answered", "dnsupdate-changes": "applied", "dnsupdate-queries": "requested", "dnsupdate-refused": "refused"},
		},
		counterDefinition{
			8, "recursive_queries_total", "Total number of recursive queries by status.", "status",
			map[string]string{"rd-queries": "requested", "recursing-questions": "processed", "recursing-answers": "answered", "recursion-unanswered": "unanswered"},
		},
		counterDefinition{
			9, "queries_total", "Total number of queries by protocol.", "proto",
			map[string]string{"tcp-queries": "tcp",
				"tcp4-queries": "tcp4",
				"tcp6-queries": "tcp6",
				"udp-queries":  "udp",
				"udp4-queries": "udp4",
				"udp6-queries": "udp6"},
		},
		counterDefinition{
			10, "answers_total", "Total number of answers by protocol.", "proto",
			map[string]string{"tcp-answers": "tcp",
				"tcp4-answers": "tcp4",
				"tcp6-answers": "tcp6",
				"udp-answers":  "udp",
				"udp4-answers": "udp4",
				"udp6-answers": "udp6"},
		},
		counterDefinition{
			11, "answers_bytes_total", "Total number of answer bytes sent over by protocol.", "proto",
			map[string]string{"tcp-answers-bytes": "tcp",
				"tcp4-answers-bytes": "tcp4",
				"tcp6-answers-bytes": "tcp6",
				"udp-answers-bytes":  "udp",
				"udp4-answers-bytes": "udp4",
				"udp6-answers-bytes": "udp6"},
		},
		counterDefinition{
			12, "exceptions_total", "Total number of exceptions by error.", "error",
			map[string]string{"servfail-packets": "servfail",
				"timedout-packets":   "timeout",
				"corrupt-packets":    "corrupt_packets",
				"overload-drops":     "backend_overload",
				"udp-recvbuf-errors": "recvbuf_errors",
				"udp-sndbuf-errors":  "sndbuf_errors",
				"udp-in-errors":      "udo_in_errors",
				"udp-noport-errors":  "udp_noport_errors"},
		},
		counterDefinition{
			13, "cpu_utilisation", "Number of CPU milliseconds spent in user, and kernel space", "type",
			map[string]string{"sys-msec": "sys", "user-msec": "user"},
		},
	}
)

// PowerDNS Dnsdist metrics definitions
var (
	dnsdistGaugeDefs   = []gaugeDefinition{}
	dnsdistCounterDefs = []counterDefinition{}
)

// Creates a fixed-value response time histogram from the following stats counters:
// answers0-1, answers1-10, answers10-100, answers100-1000, answers-slow
func makeRecursorRTimeHistogram(statsMap map[string]float64) (prometheus.Metric, error) {
	buckets := make(map[float64]uint64)
	var count uint64
	for k, v := range rTimeBucketMap {
		if _, ok := statsMap[k]; !ok {
			return nil, fmt.Errorf("Required PowerDNS stats key not found: %s", k)
		}
		value := statsMap[k]
		if v != 0 {
			buckets[v] = uint64(value)
		}
		count += uint64(value)
	}

	// Convert linear buckets to cumulative buckets
	var keys []float64
	for k, _ := range buckets {
		keys = append(keys, k)
	}
	sort.Float64s(keys)
	var cumsum uint64
	for _, k := range keys {
		cumsum = cumsum + buckets[k]
		buckets[k] = cumsum
	}

	desc := prometheus.NewDesc(
		namespace+"_recursor_response_time_seconds",
		"Histogram of PowerDNS recursor response times in seconds.",
		[]string{},
		prometheus.Labels{},
	)

	h := prometheus.MustNewConstHistogram(desc, count, 0, buckets)
	return h, nil
}
