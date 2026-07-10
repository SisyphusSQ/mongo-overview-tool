package mot

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHost       = "127.0.0.1"
	defaultPort       = 27017
	defaultAuthSource = "admin"
)

// Options 是 SDK 自建 MongoDB 连接时使用的显式配置。
type Options struct {
	URI        string
	Host       string
	Port       int
	Username   string
	Password   string
	AuthSource string

	ConnectTimeout time.Duration
	Direct         *bool
	Logger         Logger
}

// ClientOptions 是 SDK 包装已有 *mongo.Client 时使用的配置。
type ClientOptions struct {
	URI             string
	AuthSource      string
	OwnsMongoClient bool
	Ping            bool
	Logger          Logger
}

// DefaultOptions 返回 SDK 默认连接参数。
func DefaultOptions() Options {
	return Options{
		Host:       defaultHost,
		Port:       defaultPort,
		AuthSource: defaultAuthSource,
	}
}

func applyOptionsDefaults(opts Options) Options {
	defaults := DefaultOptions()
	if opts.Host == "" {
		opts.Host = defaults.Host
	}
	if opts.Port == 0 {
		opts.Port = defaults.Port
	}
	if opts.AuthSource == "" {
		opts.AuthSource = defaults.AuthSource
	}
	return opts
}

// BuildURI 根据 Options 构造 MongoDB URI。该函数不读取环境变量，也不修改入参。
func BuildURI(opts Options) (string, error) {
	opts = applyOptionsDefaults(opts)
	if strings.TrimSpace(opts.URI) != "" {
		if err := validateURI(opts.URI); err != nil {
			return "", err
		}
		return opts.URI, nil
	}
	if opts.Host == "" {
		return "", invalidOptions("host is required")
	}
	if opts.Port <= 0 {
		return "", invalidOptions("port must be greater than 0")
	}
	if (opts.Username == "") != (opts.Password == "") {
		return "", invalidOptions("username and password must be provided together")
	}

	u := url.URL{
		Scheme: "mongodb",
		Host:   net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port)),
		Path:   "/" + opts.AuthSource,
	}
	if opts.Username != "" {
		u.User = url.UserPassword(opts.Username, opts.Password)
	}
	return u.String(), nil
}

// RedactURI 脱敏 MongoDB URI 中的密码。
func RedactURI(uri string) string {
	return redactPassword(uri, "***")
}

func validateURI(uri string) error {
	if !strings.HasPrefix(uri, "mongodb://") && !strings.HasPrefix(uri, "mongodb+srv://") {
		return invalidOptions("uri must start with mongodb:// or mongodb+srv://")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("%w: parse uri: %w", ErrInvalidOptions, err)
	}
	if parsed.Host == "" {
		return invalidOptions("uri host is required")
	}
	return nil
}

func redactPassword(input, replace string) string {
	colon := strings.Index(input, ":")
	if colon == -1 || colon == len(input)-1 {
		return input
	}
	if input[colon+1] == '/' {
		for colon++; colon < len(input); colon++ {
			if input[colon] == ':' {
				break
			}
		}
		if colon == len(input) {
			return input
		}
	}

	at := strings.Index(input, "@")
	if at == -1 || at == len(input)-1 || at <= colon {
		return input
	}

	redacted := make([]byte, 0, len(input))
	for i := 0; i < len(input); i++ {
		if i <= colon || i > at {
			redacted = append(redacted, input[i])
			continue
		}
		if i == at {
			redacted = append(redacted, replace...)
			redacted = append(redacted, input[i])
		}
	}
	return string(redacted)
}
