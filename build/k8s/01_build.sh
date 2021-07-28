#!/bin/bash

set -o errexit
set -o pipefail
set -e

err_report() {
    echo "Error on line $1"
}

trap 'err_report $LINENO' ERR

START_TIME=`date +%s`

echo "Building base image for Myel pops..."
echo

REGISTRY_URL=$1

if [ -z "$REGISTRY_URL" ]
then
  echo -e "Please provide an aws ECR registry url. For example: \`./01_build.sh aws.com/my-registry\`"
  exit 2
fi

read -p "Enter FIL address you want to export key for: " FIL_ADDRESS

pop wallet export $FIL_ADDRESS $PWD/install-playbook/key

docker build -t pop:0.0.0 .
docker tag pop:0.0.0 pop:latest
docker image tag pop:latest ${REGISTRY_URL}:latest
aws ecr get-login-password --region "$(cut -d'.' -f4 <<<${REGISTRY_URL})" | docker login --username AWS --password-stdin "$(cut -d'/' -f1 <<<${REGISTRY_URL})"
docker push ${REGISTRY_URL}:latest
