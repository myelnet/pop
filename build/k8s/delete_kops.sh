#!/bin/bash

set -o errexit
set -o pipefail
set -x
set -e

err_report() {
    echo "Error on line $1"
}

trap 'err_report $LINENO' ERR

my_dir="$(dirname "$0")"
source "$my_dir/install-playbook/cluster-env.sh"

kops delete cluster --name=$CLUSTER_NAME  --yes
