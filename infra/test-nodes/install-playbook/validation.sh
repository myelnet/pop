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
