#!/bin/bash

nohup bcli start &

sed -i '/^$/d' ./test-files/providers
sed -i '/^$/d' ./test-files/cids

while true
do
  numCIDS=`wc -l < ./test-files/cids`
  cidx=$((1 + $RANDOM % $numCIDS))
  cid=`sed "${cidx}q;d" ./test-files/cids`

  numProviders=`wc -l < ./test-files/providers`
  pidx=$((1 + $RANDOM % $numProviders))
  IFS=" " read dns pid <<<  `sed "${pidx}q;d" ./test-files/providers`

  echo "/dns4/$dns/tcp/443/wss/p2p/$pid"
  echo $cid

  bcli get -peer="/dns4/$dns/tcp/443/wss/p2p/$pid" -provider-addr="f1fhv76uecv5i3rrlp4wysqtpl7jaqy3lncupc3zi" "$cid"

  sleep 5

done
