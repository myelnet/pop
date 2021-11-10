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
source "$my_dir/install-playbook/influxdb-env.sh"
source "$my_dir/install-playbook/validation.sh"

echo "Required arguments"
echo "------------------"
echo "AWS regions (REGIONS): $REGIONS"
echo "AWS images (IMAGES): $IMAGES"
echo "AWS worker node type (WORKER_NODE_TYPE): $WORKER_NODE_TYPE"
echo "Worker nodes in each zone (WORKER_NODES): $WORKER_NODES"
echo "Docker registry (REGISTRY_URL): $REGISTRY_URL"
echo "Test script (TEST_SCRIPT): $TEST_SCRIPT"

echo

BOOT_SCRIPT=$(mktemp)
envsubst <./install-playbook/boot-script.sh >$BOOT_SCRIPT
echo "Boot script created at ${BOOT_SCRIPT}"

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

  export region=${REGIONS[$index]}
  image=${IMAGES[$index]}
  log_dir=./logs/${region}

  mkdir -p $log_dir

  echo "Deploying $WORKER_NODES nodes in $region, with image $image"

  aws --region $region  ec2 create-key-pair --key-name MyelTest --query 'KeyMaterial' --output text > ~/.ssh/MyelTest-$region.pem

  chmod 400 ~/.ssh/MyelTest-$region.pem

  aws --region $region ec2 create-security-group --group-name test-nodes --description "Security group for testing nodes" --output text > $log_dir/sg.out

  aws --region $region ec2 authorize-security-group-ingress --group-name test-nodes \
    --protocol tcp \
    --port 41504 \
    --cidr 0.0.0.0/0 > $log_dir/ingress.out

  aws --region $region ec2 authorize-security-group-ingress --group-name test-nodes \
    --protocol tcp \
    --port 22 \
    --cidr 0.0.0.0/0 > $log_dir/ingress-ssh.out

  aws --region $region  ec2 run-instances --key-name $KEY_NAME --image-id $image --count ${WORKER_NODES}  --instance-type $WORKER_NODE_TYPE \
          --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=test-node}]" \
          --placement  "AvailabilityZone= ${region}a,Tenancy=default" \
          --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=500,VolumeType=gp2}" \
          --security-groups "test-nodes" > $log_dir/ec2.out

  sleep 80

  ips=`aws --region $region \
        ec2 describe-instances \
        --filters \
        "Name=instance-state-name,Values=running" \
        "Name=instance.group-name,Values=test-nodes" \
        --query 'Reservations[*].Instances[*].[PublicIpAddress]' \
        --output text`
  ipset=(`echo ${ips}  | tr '\n' ' '`);




  for ip in "${ipset[@]}"
  do
     export ip=$ip

     test_script_vm=$(mktemp)
     envsubst '${region} ${ip} ${INFLUXDB_URL} ${INFLUXDB_TOKEN} ${INFLUXDB_ORG} ${INFLUXDB_BUCKET}' <./install-playbook/$TEST_SCRIPT >$test_script_vm

     echo "Test script created at ${test_script_vm}"

     echo "Booting $ip"
        # Skip null items
     if [ -z "$ip" ]; then
       continue
     fi
     scp -o StrictHostKeyChecking=no -r -i ~/.ssh/MyelTest-${region}.pem ./test-files ubuntu@$ip:/home/ubuntu
     scp -o StrictHostKeyChecking=no -i ~/.ssh/MyelTest-${region}.pem $test_script_vm ubuntu@$ip:/home/ubuntu/test-files/$TEST_SCRIPT
     ssh -o StrictHostKeyChecking=no -i ~/.ssh/MyelTest-${region}.pem ubuntu@$ip 'bash -s' < $BOOT_SCRIPT

     sudo rm $test_script_vm

  done

done

sudo rm $BOOT_SCRIPT

echo "Test nodes are Ready"
echo
