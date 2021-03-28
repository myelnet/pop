# Content Dicovery

This plan evaluates different strategies for content discovery.
For more information on how to run it please follow instructions on the testplans [readme](/testplans).

## Global parameters

### Groups
In this plan we work with different node groups to replicate real world scenarios we expect to encounter
in the Myel network. These groups are defined in regards to the session we run in our tesplan.
- Nodes in the `clients` group make queries for content during the lifecycle of our session.
- Nodes in the `providers` group store the content requested by clients and reply to client queries.
- Nodes in the `bystanders` group may be client or provider nodes though aren't actively querying for content
or storing the content clients in our session are looking for. Since all nodes in the network won't be storing
or requesting the same content we assume a majority of node are bystanders to given session.

### Connections
The way peers are connected in the network heavily influences the performance of discovery sessions.
To simulate real world network topologies we use a combination of bootstrapping by connecting some nodes
directly and letting the DHT randomly distribute connections.
- `bootstrap` defines the number of nodes we directly connect with when setting up our node. A higher number
of bootstrap nodes means our network will be more densely connected in a shorter time frame.
- `min_conns` is the minimum of connection our node routing should strive to reach with the DHT.
- `max_conns` is the amount after which our node will start pruning connections.
Hence we expect the number of nodes connected to reach some number between min and max conns. 

### Traffic Shaping
We assume peers will have different network conditions and randomly generate parameters between a minimum and maximum.
- `latency` is generated between a given min and max. We use values expected to be encountered in a relatively close geographic area i.e. Europe as our use case optimizes for localized usage.
- `bandwidth` is generated based on average bandwidth for our target user segment, end users of recent and mid to high performance devices. We may try on lower latency connections to get and idea as well.
- `jitter` is generated based on typical values encountered in real world situations in which users maybe using more brittle wireless connections over Ethernet connected servers.

## Gossip
The main solution is currently based on gossip messaging though we may add other
solutions down the line as we believe different solutions may be best fit for different use cases.
Each composition should be run at different scales.

### Low Content Replication

This composition evaluates the performance of the network with low to no content replication over a
large network.

### High Content Replication

This composition evaluates the performance of the network with high content replication over a large 
network.

### Low network density

This composition evaluates the performance of the network in a low density topology. This means peers may be 
limiting connections to a small amount of peers or not have had time to bootstrap and increasing the size of 
the routing table.

### High network density

This composition evaluates the performance of the network in a high density topology. This means users are running
devices that have been online for a while and are able to maintain a very large number of connections.

### Network Segmentation By Region

This composition demonstrates the segmentation of the gossip network into different topics based on geographic
regions. Messages are only published to a topic in which the subscribers have relatively the same low latency.

## Netwotk Segmentation By Content

This composition demonstrates the segmentation of the gossip network by type of content. I.e. an application
has created its own subnetwork in which clients can query the topic to find their content.
