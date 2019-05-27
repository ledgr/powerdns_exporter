package main

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type pdns struct {
	*httptest.Server
	config []byte
}

func newPowerDNS(config []byte) *pdns {
	h := &pdns{config: config}
	h.Server = httptest.NewServer(handler(h))
	return h
}

func handler(h *pdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Write(h.config)
	}
}

func handlerStale(exit chan bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		<-exit
	}
}

func readCounter(m prometheus.Counter) float64 {
	// TODO: Revisit this once client_golang offers better testing tools.
	pb := &dto.Metric{}
	m.Write(pb)
	return pb.GetCounter().GetValue()
}

func readGauge(m prometheus.Gauge) float64 {
	// TODO: Revisit this once client_golang offers better testing tools.
	pb := &dto.Metric{}
	m.Write(pb)
	return pb.GetGauge().GetValue()
}

func TestServerWithoutChecks(t *testing.T) {
	config, err := ioutil.ReadFile("test/recursor_stats.json")
	if err != nil {
		t.Fatalf("could not read config file: %v", err.Error())
	}

	h := newPowerDNS(config)
	defer h.Close()

	hostURL, _ := url.Parse(h.URL)

	e := NewExporter("12345", "recursor", hostURL)

	ch := make(chan prometheus.Metric)

	go func() {
		defer close(ch)
		e.Collect(ch)
	}()

	if expect, got := 1., readGauge((<-ch).(prometheus.Gauge)); expect != got {
		// up
		t.Errorf("expected %f up, got %f", expect, got)
	}
	if expect, got := 1., readCounter((<-ch).(prometheus.Counter)); expect != got {
		// totalScrapes
		t.Errorf("expected %f recorded scrape, got %f", expect, got)
	}
	if expect, got := 0., readCounter((<-ch).(prometheus.Counter)); expect != got {
		// jsonParseFailures
		t.Errorf("expected %f csv parse failures, got %f", expect, got)
	}
	// Suck up the remaining metrics.
	for _ = range ch {
	}
}

func TestServerWithoutChecks_Error_BrokenJSONResult(t *testing.T) {
	config, err := ioutil.ReadFile("test/recursor_broken_result.json")
	if err != nil {
		t.Fatalf("could not read config file: %v", err.Error())
	}

	h := newPowerDNS(config)
	defer h.Close()

	hostURL, _ := url.Parse(h.URL)

	e := NewExporter("12345", "recursor", hostURL)

	ch := make(chan prometheus.Metric)

	go func() {
		defer close(ch)
		e.Collect(ch)
	}()

	// Check if exporter reports via the "up" metric that there was an error during the last scrape
	if expect, got := 0., readGauge((<-ch).(prometheus.Gauge)); expect != got {
		// up
		t.Errorf("expected %f up, got %f", expect, got)
	}
	// Suck up the remaining metrics.
	for _ = range ch {
	}
}

func TestParseServerInfo(t *testing.T) {
	config, err := ioutil.ReadFile("test/recursor_info.json")
	if err != nil {
		t.Fatalf("could not read config file: %v", err.Error())
	}

	h := newPowerDNS(config)
	defer h.Close()

	hostURL, _ := url.Parse(h.URL)

	got, err := getServerInfo(hostURL, "12345")
	if err != nil {
		t.Errorf("expected getServerInfo() to return no error, but got %v", err)
	}

	want := &ServerInfo{
		Kind:       "Server",
		ID:         "localhost",
		URL:        "/servers/localhost",
		DaemonType: "recursor",
		Version:    "3.7.3",
		ConfigUrl:  "/servers/localhost/config{/config_setting}",
		ZonesUrl:   "/servers/localhost/zones{/zone}",
	}

	if !reflect.DeepEqual(want, got) {
		t.Errorf("want serverInfo %#v but got %#v",
			want,
			got,
		)
	}
}

func TestCollectAuthoritativeMetrics(t *testing.T) {
	config, err := ioutil.ReadFile("test/authoritative_stats.json")
	if err != nil {
		t.Fatalf("could not read config file: %v", err.Error())
	}

	h := newPowerDNS(config)
	defer h.Close()

	hostURL, _ := url.Parse(h.URL)

	e := NewExporter("12345", "authoritative", hostURL)

	testCases := []struct {
		metricName string
		expected   string
	}{
		{
			metricName: "powerdns_authoritative_cpu_utilisation",
			expected: `
				# HELP powerdns_authoritative_cpu_utilisation Number of CPU milliseconds spent in user, and kernel space
				# TYPE powerdns_authoritative_cpu_utilisation counter
				powerdns_authoritative_cpu_utilisation{type="sys"} 1729
				powerdns_authoritative_cpu_utilisation{type="user"} 1877
			`,
		},
		{
			metricName: "powerdns_authoritative_latency_average_seconds",
			expected: `
				# HELP powerdns_authoritative_latency_average_seconds Average number of microseconds a packet spends within PowerDNS
				# TYPE powerdns_authoritative_latency_average_seconds gauge
				powerdns_authoritative_latency_average_seconds 0.001308
			`,
		},
	}

	for _, tc := range testCases {
		err = testutil.CollectAndCompare(e, strings.NewReader(tc.expected), tc.metricName)
		assert.NoError(t, err)
	}
}
func BenchmarkExtract(b *testing.B) {
	config, err := ioutil.ReadFile("test/recursor_stats.json")
	if err != nil {
		b.Fatalf("could not read config file: %v", err.Error())
	}

	h := newPowerDNS(config)
	defer h.Close()

	hostURL, _ := url.Parse(h.URL)

	e := NewExporter("12345", "recursor", hostURL)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ch := make(chan prometheus.Metric)
		go func(ch chan prometheus.Metric) {
			for _ = range ch {
			}
		}(ch)

		e.Collect(ch)
		close(ch)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	b.Logf("%d bytes used after %d runs", after.Alloc-before.Alloc, b.N)
}
