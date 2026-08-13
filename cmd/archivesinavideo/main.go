package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	archiveworker "github.com/saveweb/sinavideo/internal/worker"
	"github.com/saveweb/sinavideo/vl"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var flagDebufOutput string
var flagMaxJobs int

func init() {
	flag.StringVar(&flagDebufOutput, "o", "", "debug output directory (for dev only)")
	flag.IntVar(&flagMaxJobs, "max-jobs", 0, "maximum jobs to claim before stopping (0 means unlimited)")
}

func main() {
	flag.Parse()
	if flagMaxJobs < 0 {
		log.Fatal("max-jobs must not be negative")
	}
	baseLogger, closeLogger := newLogger()
	defer closeLogger()

	logger := baseLogger.With(zap.Dict("_stream", zap.String("project", archiveworker.Project)))
	shutdownCtx, stopShutdown := gracefulShutdownContext(logger)
	defer stopShutdown()

	if err := archiveworker.Run(shutdownCtx, baseLogger, flagMaxJobs); err != nil {
		logger.Fatal("archive worker stopped", zap.Error(err))
	}
}

func newLogger() (*zap.Logger, func()) {
	vlWriter := vl.NewVLWriter(
		"https://victorialogs.saveweb.org/",
		"",
		10_000,
		500,
		2*time.Second,
	)

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.MessageKey = "_msg"
	encoderConfig.TimeKey = "_time"
	encoderConfig.EncodeTime = utcISO8601TimeEncoder

	core := zapcore.NewTee(
		omitFields(
			zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(os.Stdout), zap.InfoLevel),
			"_stream",
		),
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(vlWriter), zap.InfoLevel),
	)

	baseLogger := zap.New(core, zap.AddCaller())
	return baseLogger, func() {
		_ = baseLogger.Sync()
		vlWriter.Close()
	}
}

func gracefulShutdownContext(logger *zap.Logger) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdownSignals
		signal.Stop(shutdownSignals)
		cancel()
		logger.Info("shutdown requested; canceling the current HQ job and stopping", zap.String("force_exit", "press Ctrl-C again"))
	}()
	return ctx, func() {
		signal.Stop(shutdownSignals)
		cancel()
	}
}
