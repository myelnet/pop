package metrics

import (
	"os"
	"time"

	influxdb "github.com/influxdata/influxdb-client-go/v2"
)

const EnvInfluxDBURL = "INFLUXDB_URL"
const EnvInfluxDBToken = "INFLUXDB_TOKEN"
const EnvInfluxDBOrg = "INFLUXDB_ORG"
const EnvInfluxDBBucket = "INFLUXDB_BUCKET"


type Config struct {
  InfluxURL string
  InfluxToken string
  Org string
  Bucket string
}

type MetricsRecorder interface {
  Record(string, map[string]string, map[string]interface{})
  URL() string
}

// implements MetricsRecorder
type InfluxDBClient struct {
  client influxdb.Client
  InfluxURL string
  Org    string
  Bucket string
}

func (idbc *InfluxDBClient) Record(name string, tag map[string]string, value map[string]interface{}) {
  p := influxdb.NewPoint(name, tag, value, time.Now())

  // get non-blocking write client
  writeAPI := idbc.client.WriteAPI(idbc.Org, idbc.Bucket)

  // write point asynchronously
  writeAPI.WritePoint(p)
  // Flush writes
  writeAPI.Flush()
}

func (idbc *InfluxDBClient) URL() string {
  return idbc.InfluxURL
}

type NullMetrics struct {}

func (*NullMetrics) Record(name string, tag map[string]string, value map[string]interface{}) {}

func (*NullMetrics) URL() string { return "" }

func New(params *Config) MetricsRecorder {

  if params == nil {
    return &NullMetrics{}
  }
  // Create a new client using an InfluxDB server base URL and an authentication token
  client := influxdb.NewClient(params.InfluxURL, params.InfluxToken)

  return &InfluxDBClient{client, params.InfluxURL, params.Org, params.Bucket}

}

func GetInfluxParams() *Config {
  addr := os.Getenv(EnvInfluxDBURL)
	if addr == "" {
		return nil
	}

  token := os.Getenv(EnvInfluxDBToken)
	if token == "" {
		return nil
	}

  org := os.Getenv(EnvInfluxDBOrg)
  if org == "" {
    return nil
  }

  bucket := os.Getenv(EnvInfluxDBBucket)
  if bucket == "" {
    return nil
  }

  return &Config{addr, token, org, bucket}

}
