package mot

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const cleanupTimeout = 5 * time.Second

// Client 是 mongo-overview-tool 的公开 SDK 入口。
type Client struct {
	conn            *pkgmongo.Conn
	bulk            bulkOperations
	ownsMongoClient bool
	opts            Options
	uri             string
	logger          Logger
}

type derivedConnectionOptions struct {
	Database       string
	ReplicaSet     string
	FallbackAuthDB string
	Direct         *bool
}

// NewClient 创建并 ping 一个由 SDK 管理生命周期的 MongoDB 连接。
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = applyOptionsDefaults(opts)
	uri, err := BuildURI(opts)
	if err != nil {
		return nil, err
	}

	conn, err := pkgmongo.NewMongoConnWithContext(ctx, uri, pkgmongo.ConnOptions{
		ConnectTimeout: opts.ConnectTimeout,
		Direct:         opts.Direct,
	})
	if err != nil {
		return nil, mapContextError(err)
	}

	return &Client{
		conn:            conn,
		bulk:            connBulkOperations{conn: conn},
		ownsMongoClient: true,
		opts:            opts,
		uri:             uri,
		logger:          normalizeLogger(opts.Logger),
	}, nil
}

// NewClientFromMongoClient 使用调用方已有的 MongoDB client 构造 SDK facade。
func NewClientFromMongoClient(ctx context.Context, client *drivermongo.Client, opts ClientOptions) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return nil, invalidOptions("mongo client is required")
	}
	if strings.TrimSpace(opts.URI) != "" {
		if err := validateURI(opts.URI); err != nil {
			return nil, err
		}
	}
	if opts.Ping {
		if err := client.Ping(ctx, readpref.Primary()); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, mapContextError(err)
			}
			return nil, fmt.Errorf("%w: ping injected mongo client: %w", ErrInvalidOptions, err)
		}
	}

	motOpts := Options{
		URI:        opts.URI,
		AuthSource: opts.AuthSource,
		Logger:     opts.Logger,
	}
	if motOpts.AuthSource == "" {
		motOpts.AuthSource = defaultAuthSource
	}
	conn := &pkgmongo.Conn{
		URI:    opts.URI,
		Client: client,
	}
	return &Client{
		conn:            conn,
		bulk:            connBulkOperations{conn: conn},
		ownsMongoClient: opts.OwnsMongoClient,
		opts:            motOpts,
		uri:             opts.URI,
		logger:          normalizeLogger(opts.Logger),
	}, nil
}

// Close 关闭由 SDK 拥有的 MongoDB 连接。注入且不归 SDK 拥有的连接不会被关闭。
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.conn == nil || !c.ownsMongoClient {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return mapContextError(c.conn.CloseWithContext(ctx))
}

func (c *Client) requireConn() error {
	if c == nil || c.conn == nil || c.conn.Client == nil {
		return invalidOptions("client is not initialized")
	}
	return nil
}

func (c *Client) requireMemberConnectionURI() error {
	if err := c.requireConn(); err != nil {
		return err
	}
	if strings.TrimSpace(c.uri) == "" {
		return invalidOptions("base uri is required for member connections")
	}
	return nil
}

func (c *Client) connectAddress(ctx context.Context, addr string, target derivedConnectionOptions) (*pkgmongo.Conn, error) {
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	if target.FallbackAuthDB == "" {
		target.FallbackAuthDB = c.opts.AuthSource
	}
	uri, err := deriveConnectionURI(c.uri, addr, target)
	if err != nil {
		return nil, err
	}
	return pkgmongo.NewMongoConnWithContext(ctx, uri, pkgmongo.ConnOptions{
		ConnectTimeout: c.opts.ConnectTimeout,
		Direct:         target.Direct,
	})
}

func (c *Client) closeDerivedConnection(ctx context.Context, conn *pkgmongo.Conn) {
	if conn == nil {
		return
	}
	closeCtx, cancel := cleanupContext(ctx)
	defer cancel()
	if err := conn.CloseWithContext(closeCtx); err != nil {
		c.logger.Warnf("failed to close derived MongoDB connection; detail suppressed")
	}
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(baseCtx, cleanupTimeout)
}

func deriveConnectionURI(baseURI, addr string, target derivedConnectionOptions) (string, error) {
	if addr == "" {
		return "", invalidOptions("address is required")
	}
	if strings.TrimSpace(baseURI) == "" {
		return "", invalidOptions("base uri is required for member connections")
	}
	if err := validateURI(baseURI); err != nil {
		return "", err
	}

	parsed, err := url.Parse(baseURI)
	if err != nil {
		return "", fmt.Errorf("%w: parse base uri: %w", ErrInvalidOptions, err)
	}
	if parsed.Host == "" {
		return "", invalidOptions("base uri host is required")
	}

	originalDatabase := strings.TrimPrefix(parsed.Path, "/")
	query := parsed.Query()
	originalAuthSource := queryValueFold(query, "authSource")
	if originalAuthSource == "" {
		originalAuthSource = originalDatabase
		if originalAuthSource == "" {
			originalAuthSource = target.FallbackAuthDB
			if originalAuthSource == "" {
				originalAuthSource = defaultAuthSource
			}
		}
	}
	deleteQueryValuesFold(query, []string{
		"connect",
		"directConnection",
		"loadBalanced",
		"maxStalenessSeconds",
		"readPreference",
		"readPreferenceTags",
		"replicaSet",
		"srvMaxHosts",
		"srvServiceName",
	})

	if parsed.Scheme == "mongodb+srv" {
		parsed.Scheme = "mongodb"
		if queryValueFold(query, "tls") == "" && queryValueFold(query, "ssl") == "" {
			query.Set("tls", "true")
		}
	}
	if target.ReplicaSet != "" {
		query.Set("replicaSet", target.ReplicaSet)
	}
	if queryValueFold(query, "authSource") == "" && originalDatabase == "" {
		query.Set("authSource", originalAuthSource)
	}
	if target.Database != "" {
		if queryValueFold(query, "authSource") == "" {
			query.Set("authSource", originalAuthSource)
		}
		parsed.Path = "/" + target.Database
		parsed.RawPath = ""
	}

	parsed.Host = addr
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func queryValueFold(query url.Values, key string) string {
	for queryKey, values := range query {
		if strings.EqualFold(queryKey, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func deleteQueryValuesFold(query url.Values, keys []string) {
	for queryKey := range query {
		for _, key := range keys {
			if strings.EqualFold(queryKey, key) {
				query.Del(queryKey)
				break
			}
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func parseShardHost(value string) (replicaSet, addresses string, err error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: invalid shard host %s", ErrUnsupportedTopology, value)
	}
	return parts[0], parts[1], nil
}

func convertClusterType(clusterType pkgmongo.ClusterType) ClusterType {
	switch clusterType {
	case pkgmongo.ClusterShard:
		return ClusterSharded
	default:
		return ClusterReplicaSet
	}
}
