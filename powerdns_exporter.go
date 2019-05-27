package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
)

const (
	namespace        = "powerdns"
	apiInfoEndpoint  = "servers/localhost"
	apiStatsEndpoint = "servers/localhost/statistics"
)

var (
	client = &http.Client{
		Transport: &http.Transport{
			Dial: func(netw, addr string) (net.Conn, error) {
				c, err := net.DialTimeout(netw, addr, 5*time.Second)
				if err != nil {
					return nil, err
				}
				if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					return nil, err
				}
				return c, nil
			},
		},
	}
)

// ServerInfo is used to parse JSON data from 'server/localhost' endpoint
type ServerInfo struct {
	Kind       string `json:"type"`
	ID         string `json:"id"`
	URL        string `json:"url"`
	DaemonType string `json:"daemon_type"`
	Version    string `json:"version"`
	ConfigUrl  string `json:"config_url"`
	ZonesUrl   string `json:"zones_url"`
}

// StatsEntry is used to parse JSON data from 'server/localhost/statistics' endpoint
type StatsEntry struct {
	Name  string  `json:"name"`
	Kind  string  `json:"type"`
	Value float64 `json:"value,string"`
}

// Exporter collects PowerDNS stats from the given HostURL and exports them using
// the prometheus metrics package.
type Exporter struct {
	HostURL    *url.URL
	ServerType string
	ApiKey     string
	mutex      sync.RWMutex

	up                prometheus.Gauge
	totalScrapes      prometheus.Counter
	jsonParseFailures prometheus.Counter
	gaugeMetrics      map[int]prometheus.Gauge
	counterMetrics    map[int]*prometheus.Desc
	gaugeDefs         []gaugeDefinition
	counterDefs       []counterDefinition
	client            *http.Client
}

func newGaugeMetric(serverType, metricName, docString string) prometheus.Gauge {
	return prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: serverType,
			Name:      metricName,
			Help:      docString,
		},
	)
}

// NewExporter returns an initialized Exporter.
func NewExporter(apiKey, serverType string, hostURL *url.URL) *Exporter {
	var gaugeDefs []gaugeDefinition
	var counterDefs []counterDefinition

	gaugeMetrics := make(map[int]prometheus.Gauge)
	counterMetrics := make(map[int]*prometheus.Desc)

	switch serverType {
	case "recursor":
		gaugeDefs = recursorGaugeDefs
		counterDefs = recursorCounterDefs
	case "authoritative":
		gaugeDefs = authoritativeGaugeDefs
		counterDefs = authoritativeCounterDefs
	case "dnsdist":
		gaugeDefs = dnsdistGaugeDefs
		counterDefs = dnsdistCounterDefs
	}

	for _, def := range gaugeDefs {
		gaugeMetrics[def.id] = newGaugeMetric(serverType, def.name, def.desc)
	}

	for _, def := range counterDefs {
		counterMetrics[def.id] = prometheus.NewDesc(
			prometheus.BuildFQName(
				namespace,
				serverType,
				def.name,
			),
			def.desc,
			[]string{def.label},
			nil,
		)
	}

	return &Exporter{
		HostURL:    hostURL,
		ServerType: serverType,
		ApiKey:     apiKey,
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: serverType,
			Name:      "up",
			Help:      "Was the last scrape of PowerDNS successful.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: serverType,
			Name:      "exporter_total_scrapes",
			Help:      "Current total PowerDNS scrapes.",
		}),
		jsonParseFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: serverType,
			Name:      "exporter_json_parse_failures",
			Help:      "Number of errors while parsing PowerDNS JSON stats.",
		}),
		gaugeMetrics:   gaugeMetrics,
		counterMetrics: counterMetrics,
		gaugeDefs:      gaugeDefs,
		counterDefs:    counterDefs,
	}
}

// Describe describes all the metrics ever exported by the PowerDNS exporter. It
// implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range e.counterMetrics {
		ch <- m
	}
	for _, m := range e.gaugeMetrics {
		ch <- m.Desc()
	}
	ch <- e.up.Desc()
	ch <- e.totalScrapes.Desc()
	ch <- e.jsonParseFailures.Desc()
}

// Collect fetches the stats from configured PowerDNS API URI and delivers them
// as Prometheus metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	stats := e.scrape()
	ch <- e.up
	ch <- e.totalScrapes
	ch <- e.jsonParseFailures

	e.collectMetrics(ch, stats)
}

func (e *Exporter) scrape() []StatsEntry {
	e.totalScrapes.Inc()

	var data []StatsEntry
	url := apiURL(e.HostURL, apiStatsEndpoint)
	err := getJSON(url, e.ApiKey, &data)
	if err != nil {
		e.up.Set(0)
		e.jsonParseFailures.Inc()
		log.Errorf("Error scraping PowerDNS: %v", err)
		return nil
	}

	e.up.Set(1)
	return data
}

func (e *Exporter) collectMetrics(ch chan<- prometheus.Metric, stats []StatsEntry) {
	statsMap := make(map[string]float64)
	for _, s := range stats {
		statsMap[s.Name] = s.Value
	}
	if len(statsMap) == 0 {
		return
	}

	for _, def := range e.gaugeDefs {
		if value, ok := statsMap[def.key]; ok {
			// latency gauges need to be converted from microseconds to seconds
			if strings.HasSuffix(def.key, "latency") {
				value = value / 1000000
			}
			e.gaugeMetrics[def.id].Set(value)
			ch <- e.gaugeMetrics[def.id]
		} else {
			log.Errorf("Expected PowerDNS stats key not found: %s", def.key)
			e.jsonParseFailures.Inc()
		}
	}

	for _, def := range e.counterDefs {
		for key, label := range def.labelMap {
			if value, ok := statsMap[key]; ok {
				ch <- prometheus.MustNewConstMetric(e.counterMetrics[def.id], prometheus.CounterValue, float64(value), label)
			} else {
				log.Errorf("Expected PowerDNS stats key not found: %s", key)
				e.jsonParseFailures.Inc()
			}
		}
	}

	if e.ServerType == "recursor" {
		h, err := makeRecursorRTimeHistogram(statsMap)
		if err != nil {
			log.Errorf("Could not create response time histogram: %v", err)
			return
		}
		ch <- h
	}
}

func getServerInfo(hostURL *url.URL, apiKey string) (*ServerInfo, error) {
	var info ServerInfo
	url := apiURL(hostURL, apiInfoEndpoint)
	err := getJSON(url, apiKey, &info)
	if err != nil {
		return nil, err
	}

	return &info, nil
}

func getJSON(url, apiKey string, data interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Add("X-API-Key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		content, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf(string(content))
	}

	if err := json.NewDecoder(resp.Body).Decode(data); err != nil {
		return err
	}

	return nil
}

func apiURL(hostURL *url.URL, path string) string {
	endpointURI, _ := url.Parse(path)
	u := hostURL.ResolveReference(endpointURI)
	return u.String()
}

func main() {
	var (
		listenAddress = flag.String("listen-address", ":9120", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("metric-path", "/metrics", "Path under which to expose metrics.")
		apiURL        = flag.String("api-url", "http://localhost:8081/api/v1/", "Base-URL of PowerDNS authoritative server/recursor API.")
		apiKey        = flag.String("api-key", "", "PowerDNS API Key")
	)
	flag.Parse()

	hostURL, err := url.Parse(*apiURL)
	if err != nil {
		log.Fatalf("Error parsing api-url: %v", err)
	}

	server, err := getServerInfo(hostURL, *apiKey)
	if err != nil {
		log.Fatalf("Could not fetch PowerDNS server info: %v", err)
	}

	exporter := NewExporter(*apiKey, server.DaemonType, hostURL)
	prometheus.MustRegister(exporter)

	log.Infof("Starting Server: %s", *listenAddress)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>PowerDNS Exporter</title></head>
             <body>
             <h1>PowerDNS Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})

	go func() {
		log.Fatal(http.ListenAndServe(*listenAddress, nil))
	}()

	<-stop
}
