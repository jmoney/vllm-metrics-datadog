package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	dogstatsd "github.com/DataDog/datadog-go/statsd"
	"github.com/robfig/cron"
)

// Metric represents a parsed Prometheus metric line.
type Metric struct {
	Name   string
	Labels map[string]string
	Value  float64
}

var (
	// Info Logger
	Info *log.Logger
	// Warning Logger
	Warning *log.Logger
	// Error Logger
	Error *log.Logger

	metricsLocation   string
	dogstatsdLocation string
)

func init() {

	Info = log.New(os.Stdout,
		"[INFO]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Warning = log.New(os.Stderr,
		"[WARNING]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Error = log.New(os.Stderr,
		"[ERROR]: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	metricsLocation = os.Getenv("METRICS_LOCATION")
	dogstatsdLocation = os.Getenv("DD_DOGSTATSD_SOCKET")
}

func serializeLabels(labels map[string]string) []string {
	var result []string
	for k, v := range labels {
		result = append(result, fmt.Sprintf("%s:%s", k, v))
	}
	return result
}

func parseMetrics(r io.Reader) []Metric {

	// Create a scanner to read the file line by line.
	scanner := bufio.NewScanner(r)

	var metrics []Metric
	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse the metric line.
		metric, err := parseMetricLine(line)
		if err != nil {
			fmt.Println("Error parsing metric line:", err)
			continue
		}

		metrics = append(metrics, metric)
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading file:", err)
	}

	return metrics
}

// parseMetricLine parses a single metric line into a Metric struct.
func parseMetricLine(line string) (Metric, error) {
	// Split the line at spaces to separate the metric name/labels and the value.
	parts := strings.Split(line, " ")
	if len(parts) < 2 {
		return Metric{}, fmt.Errorf("invalid metric line: %s", line)
	}

	// Further split the first part to separate the metric name from its labels.
	nameLabels := strings.Split(parts[0], "{")
	name := nameLabels[0]
	labelsPart := strings.TrimRight(nameLabels[1], "}")

	// Parse labels.
	labels := make(map[string]string)
	if labelsPart != "" {
		labelsPairs := strings.Split(labelsPart, ",")
		for _, pair := range labelsPairs {
			kv := strings.Split(pair, "=")
			if len(kv) == 2 {
				key := kv[0]
				// Remove quotes from the value.
				value := strings.Trim(kv[1], "\"")
				labels[key] = value
			}
		}
	}

	// Parse the value.
	var value float64
	fmt.Sscanf(parts[1], "%f", &value)

	return Metric{
		Name:   name,
		Labels: labels,
		Value:  value,
	}, nil
}

func main() {
	c := cron.New()

	cronErr := c.AddFunc("1 * * * *", Processs)
	if cronErr != nil {
		Error.Printf("cron errored out: %v", cronErr)
		return
	}

	go c.Start()
	sig := make(chan os.Signal, 5)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}

func FetchMetrics(loc string) io.Reader {
	resp, err := http.Get(loc)
	if err != nil {
		Error.Panic("Error fetching metrics:", err)
	}
	defer resp.Body.Close()

	// Read the body into a byte slice
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading response body: %v", err)
	}

	// Create an io.Reader from the byte slice
	return bytes.NewReader(bodyBytes)
}

func ProcessFile(loc string) io.Reader {
	file, err := os.Open(loc)
	if err != nil {
		Error.Panic("Error opening file:", err)
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Error reading response body: %v", err)
	}

	return bytes.NewReader(fileBytes)
}

func Processs() {
	var r io.Reader
	if strings.HasPrefix(metricsLocation, "http") {
		r = FetchMetrics(metricsLocation)
	} else if strings.HasPrefix(metricsLocation, "file") {
		r = ProcessFile(metricsLocation)
	} else {
		Error.Panicf("Invalid metrics location %s", metricsLocation)
	}

	metrics := parseMetrics(r)
	fmt.Printf("%+v\n", metrics)
}

func PushMetrics(metrics []Metric) {
	statsd, err := dogstatsd.New(dogstatsdLocation)
	if err != nil {
		Error.Panic("Error creating statsd client:", err)
	}
	defer statsd.Close()

	for _, metric := range metrics {

		labels := serializeLabels(metric.Labels)
		name := metric.Name
		if !strings.HasPrefix(name, "vllm") {
			name = "vll." + name
		}
		name = strings.ReplaceAll(name, ":", ".")

		statsd.Gauge(name, metric.Value, labels, 1)
	}
}
