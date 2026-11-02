package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"dash0.com/otlp-log-processor-backend/internal/metrics"
)

var (
	listenAddr            = flag.String("listenAddr", "localhost:4317", "The listen address")
	maxReceiveMessageSize = flag.Int("maxReceiveMessageSize", 16777216, "The max message size in bytes the server can receive")
	clickhouseAddr        = flag.String("clickhouseAddr", "localhost:9000", "The ClickHouse native protocol address")
	clickhouseDatabase    = flag.String("clickhouseDatabase", "default", "The ClickHouse database")
	clickhouseUsername    = flag.String("clickhouseUsername", "default", "The ClickHouse username; the password is read from CLICKHOUSE_PASSWORD")
)

const name = "dash0.com/otlp-log-processor-backend"

const clickhouseStartupTimeout = 30 * time.Second

var logger = otelslog.NewLogger(name)

func main() {
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

func run() (err error) {
	slog.SetDefault(logger)
	logger.Info("Starting application")

	// Set up OpenTelemetry.
	otelShutdown, err := setupOTelSDK(context.Background())
	if err != nil {
		return
	}

	// Handle shutdown properly so nothing leaks.
	defer func() {
		err = errors.Join(err, otelShutdown(context.Background()))
	}()

	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openClickHouseStore(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, store.Close())
	}()

	slog.Debug("Starting listener", slog.String("listenAddr", *listenAddr))
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(*maxReceiveMessageSize),
		grpc.Creds(insecure.NewCredentials()),
	)
	colmetricspb.RegisterMetricsServiceServer(grpcServer, metrics.NewServer(*listenAddr, store))

	go func() {
		<-ctx.Done()
		slog.Info("Shutting down gRPC server")
		grpcServer.GracefulStop()
	}()

	slog.Debug("Starting gRPC server")

	return grpcServer.Serve(listener)
}

func openClickHouseStore(ctx context.Context) (*metrics.ClickHouseMetricsStore, error) {
	ctx, cancel := context.WithTimeout(ctx, clickhouseStartupTimeout)
	defer cancel()

	store, err := metrics.NewClickHouseMetricsStore(ctx, *clickhouseAddr, *clickhouseDatabase, *clickhouseUsername, os.Getenv("CLICKHOUSE_PASSWORD"))
	if err != nil {
		return nil, err
	}
	if err := store.CreateTables(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("creating clickhouse tables: %w", err), store.Close())
	}
	slog.Info("Connected to ClickHouse", slog.String("clickhouseAddr", *clickhouseAddr), slog.String("clickhouseDatabase", *clickhouseDatabase))
	return store, nil
}
