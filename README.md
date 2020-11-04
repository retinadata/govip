# Govip

Govip is an application to ensure an IP address is set on one of the servers at
any given time, to keep a service available during a failure. It's similar to
the popular keepalived but instead of using VRRP it needs an etcd cluster to
decide which node should have the address.

It's a small application useful in scenarios where an etcd is already deployed.
We deploy etcd, apiserver, haproxy and govip instances together on kubernetes
master nodes in bare-metal deployments. One of the govips place the VIP on one
of the nodes, so the API traffic is received on the haproxy running on that
node which proxies to all apiservers.

Synchronization between govip instances is accomplished using etcd concurrency
API. Instances will try to become the election leader and claim the VIP address
only when they succeed. This means the entire process relies on etcd. If the
etcd cluster is down you won't have a VIP on any of the servers. If it's up and
functioning correctly, you won't have a split brain issue. That's the objective
anyway.

## Building

```
go build -o govip -ldflags "-X main.Version=1.0.0"
```

## Running

```
Usage of ./govip:
  -cacert string
        etcd CA cert (default "ca.crt")
  -cert string
        etcd cert file (default "server.crt")
  -etcd string
        etcd address(es) (default "https://127.0.0.1:2379")
  -key string
        etcd key file (default "server.key")
  -member string
        Unique name for this govip (default "hostname")
  -name string
        Position to synchronize multiple govips (default "/govip/")
  -version
        Print version and exit
  -vif string
        Interface to announce the VIP from (default "eth0")
  -vip string
        VIP to announce from the selected govip (default "192.168.0.254/32")
```

govip will stop if it can't reach the configured etcd cluster. So you should
run it using a supervisor capable of restarting it, like systemd. An example
file is in the repository.

In practice you would run more than one instance of govip on different nodes
using the same `-name`, `-vip` and etcd details but with different `-member`
parameters.
