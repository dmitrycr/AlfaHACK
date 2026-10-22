// testbroker is a loopback-only Kafka protocol mock for development tests.
package main

import (
	"alfa.local/shared/platform"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/twmb/franz-go/pkg/kfake"
	"net"
	"os"
)

func main() {
	port := flag.Int("port", 0, "loopback port (0 selects a free port)")
	flag.Parse()
	cluster, e := kfake.NewCluster(kfake.NumBrokers(1), kfake.BrokerConfigs(map[string]string{"message.max.bytes": "3145728"}), kfake.Ports(*port), kfake.ListenFn(func(network, address string) (net.Listener, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return net.Listen(network, net.JoinHostPort("127.0.0.1", port))
	}), kfake.SeedTopics(3, "news.articles.v1", "news.ingestion.v1", "events.lifecycle.v1", "processing.dlq.v1"))
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	defer cluster.Close()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"brokers": cluster.ListenAddrs(), "mode": "kfake"})
	<-platform.Context().Done()
}
