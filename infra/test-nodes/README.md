<h1 align="center">
	<br>
	  	🌎
	<br>
	<br>
	Myel testing
	<br>
	<br>
	<br>
</h1>

This folders contains scripts for setting up a set of test nodes for [Myel](https://myel.network). These nodes query the Myel network for content at regular intervals.

Assumes you have the `aws cli`, `docker`, and the `pop` library set up appropriately.

## Using these scripts

The scripts for deploying the cluster are in order:
- `01_build_pop.sh`
- `02_deploy.sh`
- `03_delete.sh`

`01_build_pop.sh` builds the test pop image and pushes it to a docker registry.

`02_deploy.sh` deploys a docker image to a series of aws EC2 instances with specifications detailed in `install-playbook/cluster-env.sh`.

`delete_kops.sh` destroys all aws resources associated with the cluster.

## Notes


- Requires the creation of a `test-files` folder containing newline delimited files `cids` and `providers`. The first contains a list of test CIDs to retrieve from providers, the second contains a provider DNS and peer ID on each line -- these are the tested providers.

``` bash
QmNM8jASu723xuVSNPmBf8Yyaq4tkzaDWBCE3sVuQoaDjR
QmNNZ5Br6yAEDeAhHUrzNL8wJ68ubsWQi6W34YQxQDkbhC
QmNQuVJTQ22RAPFDfoMbkmyJ1WaUBANY4g6TDbfQDL2dLi
QmNR5t2eS2ejJkqEaZifsPD8PhJNpCCpqWzjSmfe7bd8Uq
QmNR6FRZKLjYxBRZoL9YG4URsTzpqFsiW4G3tNKyjr5rtx
QmNRP7XiRwux1XbYMRCzk7tt5a29BvwZ7UPex8mkMfiEJy
```

``` bash
mynode1.com 12D3KooWRP3W5Tj5ZbJrN7dkNcmFFjm5sJNeWE1aHZnwkR6HJXCt
mynode2.com 12D3KooWQrFmYVFZPctyJ8kobjJg5AGgZHXB8CiuKCiWeseLcdbm
```

## Contribute

Our work is never finished. If you see anything we can do better, file an issue on [github.com/myelnet/pop](https://github.com/myelnet/pop/) repo or open a PR!

## License

[MIT](./LICENSE-MIT)
