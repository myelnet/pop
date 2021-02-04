# Testground plans

Hop Exchange development is made against testground plans to immediately test network behaviors at different scales.

## Getting started

Install testground from the source

```sh
$ git clone https://github.com/testground/testground.git

$ cd testground

$ make install
```

Testground will use `~/testground` as directory to look for plans
you can either import the plans in this repo:

```sh
$ testground plan import --from ~/go-hop-exchange/testground/plans/supply
```

or I like to set the repo as testground home directly i.e.

```sh
$ set -x TESTGROUND_HOME ~/go-hop-exchange/testground
```

Make sure you have docker installed and running then in a different terminal run:

```sh
$ testground daemon
```

Testground plans can be found in /testground/plans. Each plan is go module with a main.go
entry point. A plan can have multiple cases set in a map like:

```go
var testcases = map[string]interface{}{
	"caseA": run.InitializedTestCaseFn(runCaseA),
}
```

A test case is a function to which we pass a runenv object to access the runtime environment
and do things like logging messages and a context which helps us sync up with other instances.

In this function we can run our node like we usually would however we can affect its behavior by passing
specific variables to assign a role or give it info about what other peers may be in this test case.

Once the testcases are written we can declare them in a `manifest.toml` file as well as
all the environment variables we may need such as what is the peer's role etc. See the plans 
maniftests for some examples.

We can now write compositions which are the different conditions under which to run those 
test plans. Our compositions can be found under `/_compositions/` directory. Each composition
must declare one or many groups of peers with different behaviors assigned in the parameters.
Compositions also declare what runners and builders to use, these are the different ways 
our code can be run such as in local:docker or with remote AWS instances.

To run a composition: (Make sure you are in your test plan's directory)

```sh
$ testground run composition -f _compositions/mycomposition.toml
```

We will be updating this readme as we learn more about testground tools.


## Troubleshooting

Sometimes the daemon might hang, I find shutting it down and restarting Docker
fixes it most of the time.
