package archive

import (
	"context"
	"errors"
	"io"
	"time"

	http "github.com/saveweb/fhttp"
	warc "github.com/saveweb/gowarc"
)

const apiRequestTimeout = 2 * time.Minute

// executeWARCRequest waits for durable archival even when the request is
// canceled. The request context stops network I/O; Wait finishes only capture
// work that the transport already owns.
func (a *Archiver) executeWARCRequest(req *http.Request, consume func(*http.Response) error) (*http.Response, []warc.RecordEvent, error) {
	exchange, err := a.client.Start(req)
	if err != nil {
		if exchange == nil {
			return nil, nil, err
		}
		result, archiveErr := exchange.Wait(context.Background())
		return exchange.Response, result.Records, errors.Join(err, archiveErr)
	}

	response := exchange.Response
	consumeErr := consume(response)
	closeErr := response.Body.Close()
	result, archiveErr := exchange.Wait(context.Background())
	return response, result.Records, errors.Join(consumeErr, closeErr, archiveErr)
}

func (a *Archiver) readWARCURL(ctx context.Context, url string) ([]byte, []warc.RecordEvent, error) {
	return a.readWARCURLWithTimeout(ctx, url, apiRequestTimeout)
}

func (a *Archiver) readWARCURLWithTimeout(ctx context.Context, url string, timeout time.Duration) ([]byte, []warc.RecordEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	var body []byte
	_, events, err := a.executeWARCRequest(req, func(response *http.Response) error {
		var readErr error
		body, readErr = io.ReadAll(response.Body)
		return readErr
	})
	return body, events, err
}
