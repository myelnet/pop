#!/bin/bash

set -o errexit
set -o pipefail
set -e

err_report() {
    echo "Error on line $1"
}

trap 'err_report $LINENO' ERR

DEPLOYMENT_SPEC_TEMPLATE=$1
export REGISTRY_URL=$2


DEPLOYMENT_SPEC=$(mktemp)
envsubst <$DEPLOYMENT_SPEC_TEMPLATE >$DEPLOYMENT_SPEC

echo $DEPLOYMENT_SPEC

kubectl apply -f $DEPLOYMENT_SPEC
