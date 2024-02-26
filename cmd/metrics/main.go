package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	ddlambda "github.com/DataDog/datadog-lambda-go"
	"github.com/aws/aws-lambda-go/lambda"
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

	metricsLocation string
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
	if strings.HasPrefix(metricsLocation, "http") {
		lambda.Start(ddlambda.WrapFunction(HandleRequest, nil))
	} else {
		HandleRequest(context.Background(), nil)
	}
}

func HandleRequest(ctx context.Context, _ interface{}) {
	var r io.Reader
	if strings.HasPrefix(metricsLocation, "http") {
		r = FetchMetrics(metricsLocation)
	} else if strings.HasPrefix(metricsLocation, "file") {
		r = ProcessFile(metricsLocation)
	} else {
		Error.Panicf("Invalid metrics location %s", metricsLocation)
	}

	metrics := parseMetrics(r)
	if !strings.HasPrefix(metricsLocation, "file") {
		PushMetrics(metrics)
	} else {
		b, _ := json.Marshal(metrics)
		fmt.Printf("%s\n", b)
	}
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
	loc, _ = strings.CutPrefix(loc, "file://")
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

func PushMetrics(metrics []Metric) {
	for _, metric := range metrics {

		labels := serializeLabels(metric.Labels)
		name := metric.Name
		if !strings.HasPrefix(name, "vllm") {
			name = "vll." + name
		}
		name = strings.ReplaceAll(name, ":", ".")
		ddlambda.Metric(name, metric.Value, labels...)
	}
}
