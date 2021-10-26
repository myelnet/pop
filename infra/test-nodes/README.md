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
- `01_build_pop.sh DOCKER_REPO` eg. `DOCKER_REPO=docker.io/myel/test-nodes`
- `02_deploy.sh DOCKER_REPO DOCKER_USERNAME DOCKER_PASSWORD`  eg. `DOCKER_REPO=docker.io/myel/test-nodes, DOCKER_USERNAME=username, DOCKER_PASSWORD=password`
- `03_delete.sh`

`01_build_pop.sh` builds the test pop image and pushes it to a docker registry.

`02_deploy.sh` deploys the docker image to a series of aws EC2 instances with specifications detailed in `install-playbook/cluster-env.sh`.

`delete_kops.sh` destroys all aws resources associated with the cluster.

## Notes

- The deployment requires an existing pop to be running locally (`pop start`) as it extracts the FIL private key from this node to share it with all deployed containers in the cluster.


## Contribute

Our work is never finished. If you see anything we can do better, file an issue on [github.com/myelnet/pop](https://github.com/myelnet/pop/) repo or open a PR!

## License

[MIT](./LICENSE-MIT)
