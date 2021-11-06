#!/bin/bash

nohup bcli start &

while true
do
  numCIDS=`wc -l < ./test-files/cids`
  cidx=$((1 + $RANDOM % $numCIDS))
  cid=`sed "${cidx}q;d" ./test-files/cids`

  numProviders=`wc -l < ./test-files/providers`
  pidx=$((1 + $RANDOM % $numProviders))
  IFS=" " read dns pid <<<  `sed "${pidx}q;d" ./test-files/providers`

  bcli get -peer="/dns4/$dns/tcp/41504/p2p/$pid" "$cid"

  sleep 5

done
