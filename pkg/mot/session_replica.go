package mot

import (
	"context"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

type replicaSetInventory struct {
	Name    string
	Members []pkgmongo.RsMember
}

type collectorReplicaSetSource interface {
	ReplicaSetStatus(ctx context.Context, conn *pkgmongo.Conn) (pkgmongo.RsStatus, error)
}

type mongoCollectorReplicaSetSource struct{}

func (mongoCollectorReplicaSetSource) ReplicaSetStatus(ctx context.Context, conn *pkgmongo.Conn) (pkgmongo.RsStatus, error) {
	return conn.RsStatus(ctx)
}

func (c *Client) replicaSetInventory(ctx context.Context, conn *pkgmongo.Conn, key string) (replicaSetInventory, error) {
	if c != nil && c.session != nil {
		return c.session.replicaSetInventory(ctx, conn, key)
	}
	status, err := conn.RsStatus(ctx)
	if err != nil {
		return replicaSetInventory{}, err
	}
	return replicaSetInventoryFromStatus(status), nil
}

func (s *CollectorSession) replicaSetInventory(ctx context.Context, conn *pkgmongo.Conn, key string) (replicaSetInventory, error) {
	if err := s.requireOpen(); err != nil {
		return replicaSetInventory{}, err
	}
	s.mu.RLock()
	if cached, ok := s.replicaSetInventories[key]; ok {
		result := cloneReplicaSetInventory(cached)
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()

	resultChannel := s.replicaSetGroup.DoChan(key, func() (any, error) {
		s.mu.RLock()
		if cached, ok := s.replicaSetInventories[key]; ok {
			result := cloneReplicaSetInventory(cached)
			s.mu.RUnlock()
			return result, nil
		}
		s.mu.RUnlock()
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		status, err := s.replicaSetSource.ReplicaSetStatus(ctx, conn)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded := replicaSetInventoryFromStatus(status)
		s.mu.Lock()
		s.replicaSetInventories[key] = cloneReplicaSetInventory(loaded)
		s.stats.ReplicaSetInventoryLoads++
		s.mu.Unlock()
		return cloneReplicaSetInventory(loaded), nil
	})
	result, err := waitForDiscoveryResult[replicaSetInventory](ctx, resultChannel)
	return cloneReplicaSetInventory(result), err
}

func (s *CollectorSession) rememberReplicaSetInventory(key string, status pkgmongo.RsStatus) {
	if s == nil {
		return
	}
	if key == "" {
		key = status.Set
	}
	loaded := replicaSetInventoryFromStatus(status)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if _, exists := s.replicaSetInventories[key]; exists {
		return
	}
	s.replicaSetInventories[key] = cloneReplicaSetInventory(loaded)
	s.stats.ReplicaSetInventoryLoads++
}

func (c *Client) rememberReplicaSetInventory(key string, status pkgmongo.RsStatus) {
	if c != nil && c.session != nil {
		c.session.rememberReplicaSetInventory(key, status)
	}
}

func replicaSetInventoryFromStatus(status pkgmongo.RsStatus) replicaSetInventory {
	return replicaSetInventory{Name: status.Set, Members: append([]pkgmongo.RsMember(nil), status.Members...)}
}

func cloneReplicaSetInventory(source replicaSetInventory) replicaSetInventory {
	return replicaSetInventory{Name: source.Name, Members: append([]pkgmongo.RsMember(nil), source.Members...)}
}
