package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"time"

	warc "github.com/saveweb/gowarc"
)

var flagOutput string

func init() {
	flag.StringVar(&flagOutput, "o", "archive", "output directory")
}

var client = &warc.CustomHTTPClient{}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <vid> [vid2 ...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Archive Sina videos by VID.\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s 44423596\n", os.Args[0])
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	// init warc client
	// Configure WARC settings
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	userAgent := "Mozilla/5.0 (compatible; saveweb) sinavideo_archive/020260620"
	rotatorSettings := &warc.RotatorSettings{
		WarcinfoContent: warc.Header{
			"software":               []string{"saveweb_sinavideo_archive/020260620", "saveweb_gowarc/020260620"},
			"operator":               []string{"saveweb saveweb@saveweb.org"},
			"hostname":               []string{hostname},
			"http-header-user-agent": []string{userAgent},
		},
		Prefix:             "SINA_VIDEO",
		Compression:        warc.CompressionZstd,
		WARCWriterPoolSize: 1,
		OutputDirectory:    path.Join("./", "warcs"),
	}

	// Configure HTTP client settings
	clientSettings := warc.HTTPClientSettings{
		RotatorSettings: rotatorSettings,
		TempDir:         path.Join("./", "temp"),
		DNSServers:      []string{"223.5.5.5", "1.1.1.1"},
		DedupeOptions: warc.DedupeOptions{
			LocalDedupe:   true,
			CDXDedupe:     false,
			SizeThreshold: 1024, // Only payloads above that threshold will be deduped
		},
		DialTimeout:             10 * time.Second,
		ResponseHeaderTimeout:   30 * time.Second,
		DNSResolutionTimeout:    5 * time.Second,
		DNSRecordsTTL:           30 * time.Minute,
		DNSCacheSize:            10000,
		MaxReadBeforeTruncate:   1000000000,
		DecompressBody:          true,
		FollowRedirects:         true,
		InsecureSkipVerifyCerts: false,
		RandomLocalIP:           true,

		EnableHTTP2:     false,
		EnableHTTP3:     false,
		EnableKeepAlive: true,

		DigestAlgorithm:  warc.BLAKE3,
		DefaultUserAgent: userAgent,
	}

	// Create HTTP client
	_client, err := warc.NewWARCWritingHTTPClient(clientSettings)
	if err != nil {
		panic(err)
	}
	client = _client
	defer func() {
		if err := _client.Close(); err != nil {
			log.Printf("[ERROR] failed to close client: %v", err)
		}
	}()

	sem := make(chan struct{}, 114514)
	for _, vid := range flag.Args() {
		sem <- struct{}{}
		go func(vid string) {
			defer func() {
				<-sem
			}()
			if err := archive(vid); err != nil {
				log.Printf("[ERROR] %s: %v", vid, err)
			}
		}(vid)
	}
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	close(sem)
}
