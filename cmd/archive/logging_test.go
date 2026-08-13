package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestTerminalLogOmitsStream(t *testing.T) {
	var terminal bytes.Buffer
	var structured bytes.Buffer
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.MessageKey = "_msg"

	core := zapcore.NewTee(
		omitFields(
			zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(&terminal), zap.InfoLevel),
			"_stream",
		),
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(&structured), zap.InfoLevel),
	)
	logger := zap.New(core).With(zap.Dict("_stream", zap.String("project", "sinavideo")))
	logger.Info("archived", zap.Int64("job", 916229))

	if strings.Contains(terminal.String(), "_stream") {
		t.Fatalf("terminal log contains _stream: %s", terminal.String())
	}
	if !strings.Contains(terminal.String(), `{"job": 916229}`) {
		t.Fatalf("terminal log lost ordinary fields: %s", terminal.String())
	}

	var entry map[string]any
	if err := json.Unmarshal(structured.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	stream, ok := entry["_stream"].(map[string]any)
	if !ok || stream["project"] != "sinavideo" {
		t.Fatalf("structured log _stream = %#v", entry["_stream"])
	}
	if entry["job"] != float64(916229) {
		t.Fatalf("structured log job = %#v", entry["job"])
	}
}
