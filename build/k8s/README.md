<h1 align="center">
	<br>
	  	🌎
	<br>
	<br>
	Myel infrastructure
	<br>
	<br>
	<br>
</h1>

This folders contains scripts for setting up a Kubernetes cluster for [Myel](https://myel.network), essentially allowing you to deploy a planetary scale CDN with a few scripts !

Assumes you have the `aws cli`, `docker`, and the `pop` library set up appropriately.

## Using these scripts

The scripts for deploying the cluster are in order:
- `01_build.sh REGISTRY_URL` eg. `REGISTRY_URL=aws.com/container/registry`
- `02_deploy.sh CLUSTER_SPEC_TEMPLATE` eg. `CLUSTER_SPEC_TEMPLATE=cluster.yaml`
- `03_monitor.sh DEPLOYMENT_SPEC_TEMPLATE REGISTRY_URL` eg. `DEPLOYMENT_SPEC_TEMPLATE=pop-deployment.yaml`

`01_build.sh`:  builds the point-of-presence (pop) docker container using the specifications in in `install-playbook/pop-env.sh`, and pushes it to a registry. Note that `install-playbook/pop-env.sh` is of the following form, where `<YOUR LOTUS ENDPOINT>` and `<YOUR LOTUS TOKEN>` should be replaced as appropriate: 

```
#!/bin/bash

export BOOTSTRAP="/dns4/bootstrap.myel.cloud/tcp/4001/ipfs/12D3KooWML7NMZudk8H4v1AptitsTZdDqLKgEzoAdLUwuKPqkLyy"
export FIL_ENDPOINT= "<YOUR LOTUS ENDPOINT>"
export FIL_TOKEN="<YOUR LOTUS TOKEN>"
export FIL_TOKEN_TYPE="Bearer"
export CAPACITY="10GB"
export MAXPPB=5

```

`02_deploy.sh` deploys the k8s cluster + monitoring dashboard to aws, with specifications detailed in `install-playbook/cluster-env.sh`.

`03_monitor.sh` runs Myel pops on the k8s cluster with specifications detailed in `install-playbook/pop-env.sh`.

`delete_kops.sh` allows for the destruction of all aws resources associated with the cluster.

You can access the monitoring dashboard by running `kubectl proxy` and going to the associated [url](http://localhost:8001/api/v1/namespaces/kubernetes-dashboard/services/https:kubernetes-dashboard:/proxy/). To get the auth token you need to login, run:

```
 kubectl get secret -n kubernetes-dashboard $(kubectl get serviceaccount admin-user -n kubernetes-dashboard -o jsonpath="{.secrets[0].name}") -o jsonpath="{.data.token}" | base64 --decode
```
The token is created each time the dashboard is deployed and is required to log into the dashboard. Note that the token will change if the dashboard is stopped and redeployed.

## Notes

- The deployment requires an existing pop to be running locally (`pop start`) as it extracts the FIL private key from this node to share it with all deployed containers in the cluster.

- The default is for the cluster to use AWS spot compute to save on costs.

- Autoscaling is enabled by default.

- You probably want to open ingress to the public (`0.0.0.0/0`) for your worker node security group for a broad range of ports (`2001-42000`).


## Contribute

Our work is never finished. If you see anything we can do better, file an issue on [github.com/myelnet/pop](https://github.com/myelnet/pop/) repo or open a PR!

## License

[MIT](./LICENSE-MIT)
