package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"uuid"

	"github.com/spf13/cobra"

	"github.com/Noah-Wilderom/godb/pkg/server"
	"github.com/Noah-Wilderom/godb/pkg/telemetry"
)

var serverCmd = newServerCommand()

func newServerCommand() *cobra.Command {
	var (
		config        server.Config
		group, listen string
		peers         []string
		traceConfig   telemetry.TraceConfig
	)

	command := &cobra.Command{
		Use: "server", Short: "Run a database member with authenticated Raft peer transport", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := uuid.Parse(group)
			if err != nil {
				return fmt.Errorf("group ID: %w", err)
			}

			config.Identity.GroupID = id

			config.Peers, err = parseServerPeers(peers)
			if err != nil {
				return err
			}

			if config.RaftPath == "" {
				config.RaftPath = config.DatabasePath + ".raft"
			}

			if listen == "" {
				listen = config.Peers[config.Identity.NodeID]
			}

			if listen == "" {
				return fmt.Errorf("local node must appear in --peer")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if traceConfig.EndpointURL != "" {
				traceConfig.InstanceID = fmt.Sprintf("%s/%d", group, config.Identity.NodeID)

				provider, err := telemetry.NewTracerProvider(ctx, traceConfig)
				if err != nil {
					return err
				}

				config.TracerProvider = provider
				defer func() {
					flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()

					if err := provider.Shutdown(flushCtx); err != nil {
						cmd.PrintErrf("Trace export shutdown: %v\n", err)
					}
				}()
			}

			var listenerConfig net.ListenConfig

			listener, err := listenerConfig.Listen(ctx, "tcp", listen)
			if err != nil {
				return err
			}

			member, err := server.Start(ctx, config, listener)
			if err != nil {
				return errors.Join(err, listener.Close())
			}

			cmd.Printf("Database member %d listening on %s (group %s)\n", config.Identity.NodeID, listener.Addr(), group)

			if member.MetricsAddress() != "" {
				cmd.Printf("Prometheus metrics listening on http://%s/metrics\n", member.MetricsAddress())
			}

			if member.MySQLAddress() != "" {
				cmd.Printf("MySQL endpoint listening on %s (TLS required)\n", member.MySQLAddress())
			}

			return member.Wait()
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.DatabasePath, "database", "", "Existing database file")
	flags.StringVar(&config.RaftPath, "raft", "", "Raft journal path (default: database path plus .raft)")
	flags.StringVar(&group, "group", "", "Shared group UUID")
	flags.Uint64Var(&config.Identity.NodeID, "node", 0, "This member's numeric ID")
	flags.StringArrayVar(&peers, "peer", nil, "Member ID=host:port (repeat for every member, including this one)")
	flags.StringVar(&listen, "listen", "", "Bind address (default: this member's peer address)")
	flags.BoolVar(&config.Bootstrap, "bootstrap", false, "Allow initializing a missing or empty Raft journal with the configured peers")
	flags.StringVar(&config.CertFile, "tls-cert", "", "Member certificate PEM")
	flags.StringVar(&config.KeyFile, "tls-key", "", "Member private key PEM")
	flags.StringVar(&config.CAFile, "tls-ca", "", "Trusted peer CA bundle PEM")
	flags.DurationVar(&config.TickInterval, "tick", 0, "Raft tick interval (default: 100ms)")
	flags.DurationVar(&config.RPCTimeout, "peer-timeout", 0, "Peer RPC timeout (default: 1s)")
	flags.StringVar(&config.MetricsAddress, "metrics-listen", "", "Optional HTTP metrics bind address, e.g. 127.0.0.1:9090")
	flags.StringVar(&config.MySQL.Address, "mysql-listen", "", "Optional MySQL client bind address (TLS required)")
	flags.StringVar(&config.MySQL.User, "mysql-user", "", "MySQL login user (full access to this database)")
	flags.StringVar(&config.MySQL.PasswordFile, "mysql-password-file", "", "MySQL password file; one trailing newline is removed")
	flags.StringVar(&config.MySQL.Database, "mysql-database", "godb", "Logical database name exposed to MySQL clients")
	flags.IntVar(&config.MySQL.MaxConnections, "mysql-max-connections", 128, "Maximum MySQL connections including handshakes")
	flags.DurationVar(&config.MySQL.QueryTimeout, "mysql-query-timeout", 5*time.Second, "MySQL query and response deadline")
	flags.DurationVar(&config.MySQL.IdleTimeout, "mysql-idle-timeout", time.Minute, "MySQL idle/read deadline")
	flags.StringVar(&traceConfig.EndpointURL, "otlp-http-endpoint", "", "Optional OTLP HTTP trace endpoint; enables export")
	flags.Float64Var(&traceConfig.SampleRatio, "trace-sample-ratio", 0.1, "Root trace sampling ratio when export is enabled (0..1)")

	for _, name := range []string{"database", "group", "node", "peer", "tls-cert", "tls-key", "tls-ca"} {
		_ = command.MarkFlagRequired(name)
	}

	return command
}

func parseServerPeers(values []string) (map[uint64]string, error) {
	peers := make(map[uint64]string, len(values))
	for _, value := range values {
		idText, address, ok := strings.Cut(value, "=")

		id, err := strconv.ParseUint(idText, 10, 64)
		if !ok || err != nil || id == 0 || address == "" || peers[id] != "" {
			return nil, fmt.Errorf("invalid or duplicate peer %q; expected ID=host:port", value)
		}

		peers[id] = address
	}

	return peers, nil
}

func init() {
	rootCmd.AddCommand(serverCmd)
}
