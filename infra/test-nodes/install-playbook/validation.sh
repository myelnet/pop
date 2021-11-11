#!/bin/bash

set -o errexit
set -o pipefail
set -e

# Validate required arguments
if [ -z "$WORKER_NODE_TYPE" ]
then
  echo -e "Environment variable WORKER_NODE_TYPE must be set."
  exit 2
fi
if [ -z "$REGIONS" ]
then
  echo -e "Environment variable REGIONS must be set."
  exit 2
fi
if [ -z "$IMAGES" ]
then
  echo -e "Environment variable IMAGES must be set."
  exit 2
fi
if [ -z "$WORKER_NODES" ]
then
  echo -e "Environment variable WORKER_NODES must be set."
  exit 2
fi

if [ -z "$INFLUXDB_URL" ]
then
  echo -e "Please provide an influxdb url."
  exit 2
fi

if [ -z "$INFLUXDB_ORG" ]
then
  echo -e "Please provide an influxdb org."
  exit 2
fi

if [ -z "$INFLUXDB_BUCKET" ]
then
  echo -e "Please provide an influxdb bucket."
  exit 2
fi


if [ -z "$INFLUXDB_TOKEN" ]
then
  echo -e "Please provide an influxdb token."
  exit 2
fi


if [ -z "$REGISTRY_URL" ]
then
  echo -e "Please provide a docker registry url. For example: \`./01_build.sh aws.com/my-registry\`"
  exit 2
fi

if [ -z "$TEST_SCRIPT" ]
then
  echo -e "Please provide a test script to run."
  exit 2
fi

if [ ! -f ./install-playbook/$TEST_SCRIPT ];
then
  echo -e "Please provide a test script that is in the install-playbook folder."
  exit 2
fi
