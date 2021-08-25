#!/bin/bash

set -o errexit
set -o pipefail
set -e

err_report() {
    echo "Error on line $1"
}

echo "Installing InfluxDB"
pushd influxdb
helm install influxdb bitnami/influxdb -f ./values.yaml
popd
