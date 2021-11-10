#!/bin/bash

sed -i '/^$/d' ./test-files/providers
sed -i '/^$/d' ./test-files/cids

declare -i i=0

while true
do

  ((i % 100 == 0)) && pkill -15 bcli && sleep 5 && \
                  nohup bcli start -server-id=${region}-${ip} \
                                   -influxdb-url=${INFLUXDB_URL} \
                                   -influxdb-token=${INFLUXDB_TOKEN} \
                                   -influxdb-org=${INFLUXDB_ORG} \
                                   -influxdb-bucket=${INFLUXDB_BUCKET} &

  sleep 5

  numCIDS=`wc -l < ./test-files/cids`
  cidx=$((1 + $RANDOM % $numCIDS))
  cid=`sed "${cidx}q;d" ./test-files/cids`

  numProviders=`wc -l < ./test-files/providers`
  pidx=$((1 + $RANDOM % $numProviders))
  IFS=" " read dns pid <<<  `sed "${pidx}q;d" ./test-files/providers`

  echo "/dns4/$dns/tcp/443/wss/p2p/$pid"
  echo $cid

  bcli get -peer="/dns4/$dns/tcp/443/wss/p2p/$pid" -provider-addr="f1fhv76uecv5i3rrlp4wysqtpl7jaqy3lncupc3zi" "$cid"

done
