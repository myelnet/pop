# Architecture

The Exchange module coordinates multiple independent services to allow reading and writing to a 
decentralized network of POPs.

## Index

The exchange maintains an index of metadata about its locally stored content. When importing new content to 
the node a new isolated blockstore is created and the index allows finding the reference to the store used
for a given content ID.

## Discovery

The discovery service allows an exchange to both find providers for a given content reference as well as 
participate as a provider and responding to queries made by other discovery peers. It exposes 2 interfaces
abstracting away the implementation:

```go

// ReceiveResponse is a callback triggered every time we receive a new response from the network
type ReceiveResponse func(peer.AddrInfo, deal.QueryResponse)

// QueryResolver sends a query and assigns a callback function to receive responses
type QueryResolver interface {
	// ReceiveResponse may be triggered until the given context is cancelled
	Query(context.Context, cid.Cid, ReceiveResponse) error
}

// ResponseFunc processes a Query and returns a Response or an error if query is declined
type ResponseFunc func(context.Context, peer.ID, Region, deal.Query) (deal.QueryResponse, error)

// ContentProvider starts a worker that listens for queries and calls
type ContentProvider interface {
	StartProviding(context.Context, ResponseFunc) error
}

// Discovery implements both QueryResolver and ContentProvider interfaces
type Discovery interface {
	QueryResolver
	ContentProvider
}

```

## Replication

The replication service abstracts away mechanisms for replicating content across multiple nodes. 
It is built upon the premise that our node is exposed to network queries and can thus asses the demand
for given content blocks. To that end, it piggybacks on the Discovery service to record every time the
node receives a query for content.

```go

// ContentDispatcher exposes a method to dispatch a content tree of the given size
// it registers a callback to be called every time a peer receives the content
type ContentDispatcher interface {
	Dispatch(content cid.Cid, size uint64, fn func(peer.ID, cid.Cid)) error
}

// DemandRecorder exposes a method to register each time a node receives a Query for content
// based on the frequency of calls
type DemandRecorder interface {
	Record(peer.ID, deal.Query) error
}

// Replication handles the propagation of content to adjacent providers and the role
// of the local node in replication schemes
type Replication interface {
	ContentDispatcher
	DemandRecorder
}

```
