package mot

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/sync/singleflight"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

type collectorCatalogSource interface {
	ListDatabaseNames(ctx context.Context, conn *pkgmongo.Conn) ([]string, error)
	ListCollections(ctx context.Context, conn *pkgmongo.Conn, database string) ([]indexCollectionMetadata, error)
}

type mongoCollectorCatalogSource struct{}

func (mongoCollectorCatalogSource) ListDatabaseNames(ctx context.Context, conn *pkgmongo.Conn) ([]string, error) {
	return conn.Client.ListDatabaseNames(ctx, bson.D{})
}

func (mongoCollectorCatalogSource) ListCollections(ctx context.Context, conn *pkgmongo.Conn, database string) ([]indexCollectionMetadata, error) {
	cursor, err := conn.Client.Database(database).ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = cursor.Close(closeCtx)
	}()
	var result []indexCollectionMetadata
	if err := cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) databaseNames(ctx context.Context) ([]string, error) {
	if c != nil && c.session != nil {
		return c.session.databaseNames(ctx)
	}
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	return c.conn.Client.ListDatabaseNames(ctx, bson.D{})
}

func (c *Client) collectionMetadata(ctx context.Context, database string) ([]indexCollectionMetadata, error) {
	if c != nil && c.session != nil {
		return c.session.collectionMetadata(ctx, database)
	}
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	return mongoCollectorCatalogSource{}.ListCollections(ctx, c.conn, database)
}

func (c *Client) replicaSetDatabaseNames(ctx context.Context, conn *pkgmongo.Conn, replicaSet string) ([]string, error) {
	if c != nil && c.session != nil {
		return c.session.replicaSetDatabases(ctx, conn, replicaSet)
	}
	if conn == nil || conn.Client == nil {
		return nil, invalidOptions("replica set connection is required")
	}
	return mongoCollectorCatalogSource{}.ListDatabaseNames(ctx, conn)
}

func (s *CollectorSession) databaseNames(ctx context.Context) ([]string, error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	if s.databaseInventory != nil {
		cached := append([]string(nil), s.databaseInventory...)
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	resultChannel := s.catalogGroup.DoChan("databases", func() (any, error) {
		s.mu.RLock()
		if s.databaseInventory != nil {
			cached := append([]string(nil), s.databaseInventory...)
			s.mu.RUnlock()
			return cached, nil
		}
		s.mu.RUnlock()
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		loaded, err := s.catalogSource.ListDatabaseNames(ctx, s.client.conn)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded = append([]string(nil), loaded...)
		s.mu.Lock()
		s.databaseInventory = loaded
		s.stats.DatabaseInventoryLoads++
		s.mu.Unlock()
		return append([]string(nil), loaded...), nil
	})
	result, err := waitForCatalogResult[[]string](ctx, resultChannel)
	return append([]string(nil), result...), err
}

func (s *CollectorSession) collectionMetadata(ctx context.Context, database string) ([]indexCollectionMetadata, error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	if cached, ok := s.collectionInventory[database]; ok {
		result := append([]indexCollectionMetadata(nil), cached...)
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()

	resultChannel := s.catalogGroup.DoChan("collections\x00"+database, func() (any, error) {
		s.mu.RLock()
		if cached, ok := s.collectionInventory[database]; ok {
			result := append([]indexCollectionMetadata(nil), cached...)
			s.mu.RUnlock()
			return result, nil
		}
		s.mu.RUnlock()
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		loaded, err := s.catalogSource.ListCollections(ctx, s.client.conn, database)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded = append([]indexCollectionMetadata(nil), loaded...)
		s.mu.Lock()
		s.collectionInventory[database] = loaded
		s.stats.CollectionInventoryLoads++
		s.mu.Unlock()
		return append([]indexCollectionMetadata(nil), loaded...), nil
	})
	result, err := waitForCatalogResult[[]indexCollectionMetadata](ctx, resultChannel)
	return append([]indexCollectionMetadata(nil), result...), err
}

func (s *CollectorSession) replicaSetDatabases(ctx context.Context, conn *pkgmongo.Conn, replicaSet string) ([]string, error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	if conn == nil || conn.Client == nil {
		return nil, invalidOptions("replica set connection is required")
	}
	s.mu.RLock()
	if cached, ok := s.replicaSetDatabaseNames[replicaSet]; ok {
		result := append([]string(nil), cached...)
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()

	resultChannel := s.catalogGroup.DoChan("replica-databases\x00"+replicaSet, func() (any, error) {
		s.mu.RLock()
		if cached, ok := s.replicaSetDatabaseNames[replicaSet]; ok {
			result := append([]string(nil), cached...)
			s.mu.RUnlock()
			return result, nil
		}
		s.mu.RUnlock()
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		loaded, err := s.catalogSource.ListDatabaseNames(ctx, conn)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded = append([]string(nil), loaded...)
		s.mu.Lock()
		s.replicaSetDatabaseNames[replicaSet] = loaded
		s.stats.DatabaseInventoryLoads++
		s.mu.Unlock()
		return append([]string(nil), loaded...), nil
	})
	result, err := waitForCatalogResult[[]string](ctx, resultChannel)
	return append([]string(nil), result...), err
}

func waitForCatalogResult[T any](ctx context.Context, resultChannel <-chan singleflight.Result) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return zero, mapContextError(ctx.Err())
	case result := <-resultChannel:
		if result.Err != nil {
			return zero, result.Err
		}
		value, ok := result.Val.(T)
		if !ok {
			return zero, errors.New("collector catalog cache returned an invalid value")
		}
		return value, nil
	}
}
