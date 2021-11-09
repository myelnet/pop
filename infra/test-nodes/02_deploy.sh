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

BOOT_SCRIPT=$(mktemp)
envsubst <./install-playbook/boot-script.sh >$BOOT_SCRIPT

echo "Boot script created at ${BOOT_SCRIPT}"

my_dir="$(dirname "$0")"
source "$my_dir/install-playbook/cluster-env.sh"
source "$my_dir/install-playbook/influxdb-env.sh"
source "$my_dir/install-playbook/validation.sh"

echo "Required arguments"
echo "------------------"
echo "AWS regions (REGIONS): $REGIONS"
echo "AWS images (IMAGES): $IMAGES"
echo "AWS worker node type (WORKER_NODE_TYPE): $WORKER_NODE_TYPE"
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

declare -a paramset
declare -a ipset

REGIONS=(`echo ${REGIONS}`);
IMAGES=(`echo ${IMAGES}`);



for index in ${!REGIONS[@]};

do

  LOG_DIR=./logs/${REGIONS[$index]}

  mkdir -p $LOG_DIR

  echo "Deploying ${WORKER_NODES[$index]} nodes in ${REGIONS[$index]}, with image ${IMAGES[$index]}"

  aws --region ${REGIONS[$index]}  ec2 create-key-pair --key-name MyelTest --query 'KeyMaterial' --output text > ~/.ssh/MyelTest-${REGIONS[$index]}.pem

  chmod 400 ~/.ssh/MyelTest-${REGIONS[$index]}.pem

  aws --region ${REGIONS[$index]} ec2 create-security-group --group-name test-nodes --description "Security group for testing nodes" --output text > $LOG_DIR/sg.out

  aws --region ${REGIONS[$index]} ec2 authorize-security-group-ingress --group-name test-nodes \
    --protocol tcp \
    --port 41504 \
    --cidr 0.0.0.0/0 > $LOG_DIR/ingress.out

  aws --region ${REGIONS[$index]} ec2 authorize-security-group-ingress --group-name test-nodes \
    --protocol tcp \
    --port 22 \
    --cidr 0.0.0.0/0 > $LOG_DIR/ingress-ssh.out

  aws --region ${REGIONS[$index]}  ec2 run-instances --key-name $KEY_NAME --image-id ${IMAGES[$index]} --count ${WORKER_NODES}  --instance-type $WORKER_NODE_TYPE \
          --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=test-node}]" \
          --placement  "AvailabilityZone= ${REGIONS[$index]}a,Tenancy=default" \
          --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=500,VolumeType=gp2}" \
          --security-groups "test-nodes" > $LOG_DIR/ec2.out

  sleep 60

  ips=`aws --region ${REGIONS[$index]} \
        ec2 describe-instances \
        --filters \
        "Name=instance-state-name,Values=running" \
        "Name=instance.group-name,Values=test-nodes" \
        --query 'Reservations[*].Instances[*].[PublicIpAddress]' \
        --output text`
  ipset=(`echo ${ips}  | tr '\n' ' '`);




  for ip in "${ipset[@]}"
  do
     TEST_SCRIPT=$(mktemp)
     envsubst <./install-playbook/test-node-script.sh >$TEST_SCRIPT

     echo "Test script created at ${TEST_SCRIPT}"

     echo "Booting $ip"
        # Skip null items
     if [ -z "$ip" ]; then
       continue
     fi
     scp -r -i ~/.ssh/MyelTest-${REGIONS[$index]}.pem ./test-files ubuntu@$ip:/home/ubuntu
     scp -i ~/.ssh/MyelTest-${REGIONS[$index]}.pem $TEST_SCRIPT ubuntu@$ip:/home/ubuntu/test-files/test-node-script.sh
     ssh -i ~/.ssh/MyelTest-${REGIONS[$index]}.pem ubuntu@$ip 'bash -s' < $BOOT_SCRIPT

     sudo rm $TEST_SCRIPT

  done

done

sudo rm $BOOT_SCRIPT

echo "Test nodes are Ready"
echo
