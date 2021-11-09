#!/bin/bash

export REGIONS="eu-west-3 us-west-1 us-east-1"
export IMAGES="ami-0f7cd40eac2214b37 ami-0d382e80be7ffdae5 ami-09e67e426f25ce0d7"
export WORKER_NODES=1
export WORKER_NODE_TYPE=t2.medium
export KEY_NAME=MyelTest
export REGISTRY_URL=myel/test-pop
export TEST_SCRIPT=test-node-script.sh
