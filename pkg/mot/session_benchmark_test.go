package mot

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkCollectorSessionReadOnlyMatrix(b *testing.B) {
	// 基准模拟 6 个 replica set、18 个节点和多数据库访问，验证请求级发现与连接复用成本。
	client := newDisconnectedTestClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		session, err := client.NewCollectorSession(CollectorSessionOptions{MaxConcurrency: 4})
		if err != nil {
			b.Fatal(err)
		}
		session.discoverySource = &recordingDiscoverySource{}
		session.catalogSource = &recordingCatalogSource{}
		session.replicaSetSource = &recordingReplicaSetSource{}
		session.connectionFactory = &recordingDerivedConnectionFactory{}

		if _, err := session.client.detectCluster(ctx); err != nil {
			b.Fatal(err)
		}
		if _, err := session.client.databaseNames(ctx); err != nil {
			b.Fatal(err)
		}
		for replicaSet := 0; replicaSet < 6; replicaSet++ {
			key := fmt.Sprintf("rs-%d", replicaSet)
			if _, err := session.client.replicaSetInventory(ctx, session.client.conn, key); err != nil {
				b.Fatal(err)
			}
			if _, err := session.client.replicaSetDatabaseNames(ctx, session.client.conn, key); err != nil {
				b.Fatal(err)
			}
		}
		for node := 0; node < 18; node++ {
			address := fmt.Sprintf("node-%d:27017", node)
			for database := 0; database < 4; database++ {
				if _, err := session.derivedConnection(ctx, address, derivedConnectionOptions{
					Database: fmt.Sprintf("db-%d", database),
					Direct:   boolPointer(true),
				}); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := session.Close(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
