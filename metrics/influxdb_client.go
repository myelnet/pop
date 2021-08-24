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

type RetrievalMeasurement struct {
  TimeSpent int
  FileSize int
  Peer string
}

type InfluxMessage struct {
  Peer string
  Msg string
}


type Config struct {
  InfluxURL string
  InfluxToken string
  Org string
  Bucket string
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

func NewInfluxDBClient(params Config) *InfluxDBClient {
  // Create a new client using an InfluxDB server base URL and an authentication token
  client := influxdb.NewClient(params.InfluxURL, params.InfluxToken)

  return &InfluxDBClient{client, params.InfluxURL, params.Org, params.Bucket}

}

func GetInfluxParams() *Config {
  addr := os.Getenv(EnvInfluxDBURL)
	if addr == "" {
		return &Config{}
	}

  token := os.Getenv(EnvInfluxDBToken)
	if token == "" {
		return &Config{}
	}

  org := os.Getenv(EnvInfluxDBOrg)
  if org == "" {
    return &Config{}
  }

  bucket := os.Getenv(EnvInfluxDBBucket)
  if bucket == "" {
    return &Config{}
  }

  return &Config{addr, token, org, bucket}

}
