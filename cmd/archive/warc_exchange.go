package main

import (
	"context"
	"errors"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
)

// startWARCRequest preserves records from attempts that fail before response
// headers arrive. Start still returns an Exchange for those attempts.
func startWARCRequest(req *http.Request) (*warc.Exchange, []warc.RecordEvent, error) {
	exchange, err := client.Start(req)
	if err == nil {
		return exchange, nil, nil
	}
	if exchange == nil {
		return nil, nil, err
	}
	result, archiveErr := exchange.Wait(context.Background())
	return nil, result.Records, errors.Join(err, archiveErr)
}

// finishWARCRequest closes the caller-facing body before waiting for durable
// writer acknowledgement. The Wait context controls only this wait; the
// request context already controls the exchange itself.
func finishWARCRequest(exchange *warc.Exchange) ([]warc.RecordEvent, error) {
	closeErr := exchange.Response.Body.Close()
	result, archiveErr := exchange.Wait(context.Background())
	return result.Records, errors.Join(closeErr, archiveErr)
}
