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

First create an S3 bucket:

```bash
aws s3api create-bucket --bucket ${KOPS_STATE_STORE} --region $AWS_REGION --create-bucket-configuration LocationConstraint=$AWS_REGION >> s3.resp
```


The scripts for deploying the cluster are in order:
- `01_deploy.sh CLUSTER_SPEC_TEMPLATE` eg. `CLUSTER_SPEC_TEMPLATE=cluster.yaml`
- `02_deploy_influxdb.sh`
- `03_build_pop.sh REGISTRY_URL` eg. `REGISTRY_URL=aws.com/container/registry`
- `04_push_pop.sh DEPLOYMENT_SPEC_TEMPLATE REGISTRY_URL` eg. `DEPLOYMENT_SPEC_TEMPLATE=pop-deployment.yaml`

`01_deploy.sh` deploys the k8s cluster + monitoring dashboard to aws, with specifications detailed in `install-playbook/cluster-env.sh`.

`02_deploy_influxdb.sh` deploys the influxdb database. Log on to the database dashboard at the specified endpoint by running `kubectl proxy` and going to the associated [url](http://localhost:8001/api/v1/namespaces/influxdb/services/https:influxdb:/proxy/). Create a user token, a bucket, and an organization then create an env file for them: `touch install-playbook` and populate it as such:

```bash
#!/bin/bash

# influxdb is the url given to the service withing the k8s cluster
export INFLUXDB_URL="http://influxdb:8086"
export INFLUXDB_TOKEN=<INSERT TOKEN>
export INFLUXDB_ORG=<INSERT ORG>
export INFLUXDB_BUCKET=<INSERT BUCKET>
```

`03_build_pop.sh`:  then builds the point-of-presence (pop) docker container using the specifications in in `install-playbook/pop-env.sh`, and pushes it to a registry.

`04_push_pop.sh` runs Myel pops on the k8s cluster with specifications detailed in `install-playbook/pop-env.sh`.

`delete_kops.sh` destroys all aws resources associated with the cluster.

You can access the monitoring dashboard by running `kubectl proxy` and going to the associated [url](http://localhost:8001/api/v1/namespaces/kubernetes-dashboard/services/https:kubernetes-dashboard:/proxy/). To get the auth token you need to login, run:

```bash
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
