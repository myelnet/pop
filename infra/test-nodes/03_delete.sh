#!/bin/bash

set -o errexit
set -o pipefail
set -e

err_report() {
    echo "Error on line $1"
}

trap 'err_report $LINENO' ERR

START_TIME=`date +%s`

echo "Creating Myel test nodes..."
echo


my_dir="$(dirname "$0")"
source "$my_dir/install-playbook/cluster-env.sh"
source "$my_dir/install-playbook/validation.sh"

echo "Required arguments"
echo "------------------"
echo "AWS regions (REGIONS): $REGIONS"
echo "Worker nodes in each zone (WORKER_NODES): $WORKER_NODES"

echo

# Verify with the user before continuing.
echo
echo "The nodes will be deployed based on the params above."
echo -n "Do they look right to you? [y/n]: "
read response

if [ "$response" != "y" ]
then
  echo "Canceling ."
  exit 2
fi

declare -a idset

REGIONS=(`echo ${REGIONS}`);

for index in ${!REGIONS[@]};

do

  LOG_DIR=./logs/${REGIONS[$index]}

  mkdir -p $LOG_DIR

  echo "Deleting ${WORKER_NODES} nodes in ${REGIONS[$index]}"


  ids=`aws --region ${REGIONS[$index]} \
        ec2 describe-instances \
        --filters \
        "Name=instance-state-name,Values=running" \
        "Name=instance.group-name,Values=test-nodes" \
        --query 'Reservations[*].Instances[*].[InstanceId]' \
        --output text`
  idset=(`echo ${ids}  | tr '\n' ' '`);

  if [ -z "$idset" ]; then
    echo "No nodes in ${REGIONS[$index]}"
  else
    aws --region ${REGIONS[$index]} ec2 terminate-instances --instance-ids $idset --output text >> $LOG_DIR/delete-ec2.out
    sleep 60
  fi

  aws --region ${REGIONS[$index]}  ec2 delete-key-pair --key-name MyelTest

  chmod -f 777 ~/.ssh/MyelTest-${REGIONS[$index]}.pem  || true

  sudo rm -f ~/.ssh/MyelTest-${REGIONS[$index]}.pem || true

  aws --region ${REGIONS[$index]} ec2 delete-security-group --group-name test-nodes --output text || true >> $LOG_DIR/delete-sg.out

done
echo "Test nodes are deleted"
echo
