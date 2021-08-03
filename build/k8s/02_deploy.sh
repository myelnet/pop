#!/bin/bash

set -o errexit
set -o pipefail
set -e

err_report() {
    echo "Error on line $1"
}

trap 'err_report $LINENO' ERR

START_TIME=`date +%s`

echo "Creating cluster for Myel pops..."
echo

CLUSTER_SPEC_TEMPLATE=$1
export REGISTRY_URL=$2

my_dir="$(dirname "$0")"
source "$my_dir/install-playbook/cluster-env.sh"
source "$my_dir/install-playbook/validation.sh"

echo "Required arguments"
echo "------------------"
echo "Cluster name (CLUSTER_NAME): $CLUSTER_NAME"
echo "Kops state store (KOPS_STATE_STORE): $KOPS_STATE_STORE"
echo "AWS availability zone A (ZONE_A): $ZONE_A"
echo "AWS availability zone B (ZONE_B): $ZONE_B"
echo "AWS region (AWS_REGION): $AWS_REGION"
echo "AWS worker node type (WORKER_NODE_TYPE): $WORKER_NODE_TYPE"
echo "AWS master node type (MASTER_NODE_TYPE): $MASTER_NODE_TYPE"
echo "Worker nodes (WORKER_NODES): $WORKER_NODES"
echo "Master nodes (MASTER_NODES): $MASTER_NODES"
echo "Public key (PUBKEY): $PUBKEY"
echo

CLUSTER_SPEC=$(mktemp)
envsubst <$CLUSTER_SPEC_TEMPLATE >$CLUSTER_SPEC

# Verify with the user before continuing.
echo
echo "The cluster will be built based on the params above."
echo -n "Do they look right to you? [y/n]: "
read response

if [ "$response" != "y" ]
then
  echo "Canceling ."
  exit 2
fi

# The remainder of this script creates the cluster using the generated template

kops create -f $CLUSTER_SPEC

kops create secret --name=$CLUSTER_NAME  sshpublickey admin -i $PUBKEY
kops update cluster --name=$CLUSTER_NAME  --yes

# Wait for worker nodes and master to be ready
kops validate cluster --name=$CLUSTER_NAME --wait 20m

echo "Cluster nodes are Ready"
echo

echo "Installing dashboard"
echo

kubectl apply -f https://raw.githubusercontent.com/kubernetes/dashboard/v2.0.0/aio/deploy/recommended.yaml

kubectl apply -f k8s-dashboard/dashboard-admin.yaml

kubectl --namespace kubernetes-dashboard describe secret $(kubectl -n kubernetes-dashboard get secret | grep admin-user | awk '{print $1}')

vpcId=`aws ec2 describe-vpcs --region=$AWS_REGION --filters Name=tag:Name,Values=$CLUSTER_NAME --output text | awk '/VPCS/ { print $8 }'`

if [[ -z ${vpcId} ]]; then
  echo "Couldn't detect AWS VPC created by `kops`"
  exit 1
fi

echo "Detected VPC: $vpcId"

securityGroupId=`aws ec2 describe-security-groups --region=$AWS_REGION --output text | awk '/nodes.'$CLUSTER_NAME'/ && /SECURITYGROUPS/ { print $6 };'`

if [[ -z ${securityGroupId} ]]; then
  echo "Couldn't detect AWS Security Group created by `kops`"
  exit 1
fi

echo "Detected Security Group ID: $securityGroupId"

echo "Allowing ingress for: $securityGroupId"

aws ec2 authorize-security-group-ingress --group-id $securityGroupId \
   --protocol tcp \
   --port 2001-42000 \
   --cidr 0.0.0.0/0
