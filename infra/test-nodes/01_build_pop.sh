#!/bin/bash
# Make sure you are pushing to a private repo the built image contains a shared private key

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
  echo -e "Please provide a docker registry url. For example: \`./01_build.sh aws.com/my-registry\`"
  exit 2
fi

docker build --platform linux/x86_64 -t ${REGISTRY_URL}:latest .
# uncomment if using aws registry
# aws ecr get-login-password --region "$(cut -d'.' -f4 <<<${REGISTRY_URL})" | docker login --username AWS --password-stdin "$(cut -d'/' -f1 <<<${REGISTRY_URL})"
docker push ${REGISTRY_URL}:latest
