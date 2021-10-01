<h1 align="center">
	<br>
	  	🪝
	<br>
	<br>
	pop
	<br>
	<br>
	<br>
</h1>

> Run a github webhook to update your [Myel](https://www.myel.network/) pop automatically


## Technical Highlights

- Runs a server on port 4567 and awaits new releases from the [pop repo](https://github.com/myelnet/pop) to then auto update.


## Usage

Make sure `start-cmd.sh` matches the command you want to run when starting your pop.

Make sure the environment variables `GITHUB_WEBHOOK_SECRET` matches the [Github webhook secret](https://docs.github.com/en/developers/webhooks-and-events/webhooks/securing-your-webhooks) set for your server.

Run `go hook.go` with `-h` flag for more usage details.

```
Usage of hook.go:
  -arch string
        architecture used (default "amd64")
  -cmd string
        cmd to run when starting pop (default "./start-cmd.sh")
  -os string
        OS used (default "linux")
  -pop-path string
        path to pop install (default "/usr/local/bin/pop")
```