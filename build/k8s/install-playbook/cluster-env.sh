#!/bin/bash

export CLUSTER_NAME=myel.k8s.local
export KOPS_STATE_STORE=k8s-myel-pops
export AWS_REGION=eu-west-1
export ZONE_A=eu-west-1a
export ZONE_B=eu-west-1b
#export AWS_WORKER_REGION=eu-west-1a,eu-west-1b,eu-west-1c
export WORKER_NODE_TYPE=t2.medium
export MASTER_NODE_TYPE=t2.medium
export WORKER_NODES=2
export MASTER_NODES=1
export BASE_ON_DEMAND=0
export PUBKEY=~/.ssh/id_rsa.pub
