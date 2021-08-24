package metrics

import (
	"fmt"
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


type InfluxParams struct {
  URL string
  Token string
  Org string
  Bucket string
}

func GetInfluxParams() (*InfluxParams, error) {
  addr := os.Getenv(EnvInfluxDBURL)
	if addr == "" {
		return nil, fmt.Errorf("no InfluxDB URL in $%s env var", EnvInfluxDBURL)
	}

  token := os.Getenv(EnvInfluxDBToken)
	if token == "" {
		return nil, fmt.Errorf("no InfluxDB Token in $%s env var", EnvInfluxDBToken)
	}

  org := os.Getenv(EnvInfluxDBOrg)
  if org == "" {
    return nil, fmt.Errorf("no InfluxDB Organization in $%s env var", EnvInfluxDBOrg)
  }

  bucket := os.Getenv(EnvInfluxDBBucket)
  if bucket == "" {
    return nil, fmt.Errorf("no InfluxDB Bucket in $%s env var", EnvInfluxDBBucket)
  }

  return &InfluxParams{addr, token, org, bucket}, nil

}

func SendMessage(re InfluxMessage, params InfluxParams) error {

  // Create a new client using an InfluxDB server base URL and an authentication token
  client := influxdb.NewClient(params.URL, params.Token)

  p := influxdb.NewPoint("check-in",
  map[string]string{"peer": re.Peer},
  map[string]interface{}{"msg": re.Msg},
  time.Now())

  // get non-blocking write client
  writeAPI := client.WriteAPI(params.Org, params.Bucket)

  // write point asynchronously
  writeAPI.WritePoint(p)
  // Flush writes
  writeAPI.Flush()

  client.Close()

  return nil
}

func NewMeasurement(re RetrievalMeasurement, params InfluxParams) error {

  // Create a new client using an InfluxDB server base URL and an authentication token
  client := influxdb.NewClient(params.URL, params.Token)

  p := influxdb.NewPoint("retrieval",
  map[string]string{"peer": re.Peer},
  map[string]interface{}{"fileSize": re.FileSize, "timeSpent": re.TimeSpent},
  time.Now())

  // get non-blocking write client
  writeAPI := client.WriteAPI(params.Org, params.Bucket)

  // write point asynchronously
  writeAPI.WritePoint(p)
  // Flush writes
  writeAPI.Flush()

  client.Close()

  return nil
}
