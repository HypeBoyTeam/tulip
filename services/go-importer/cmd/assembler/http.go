package main

import (
	"bufio"
	"compress/gzip"
	"go-importer/internal/pkg/db"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/andybalholm/brotli"
)

const DecompressionSizeLimit = int64(streamdoc_limit)

func AddFingerprints(cookies []*http.Cookie, fingerPrints map[uint32]bool) {
	for _, cookie := range cookies {

		// Prevent exploitation by encoding :pray:, who cares about collisions
		checksum := crc32.Checksum([]byte(url.QueryEscape(cookie.Name)), crc32.IEEETable)
		checksum = crc32.Update(checksum, crc32.IEEETable, []byte("="))
		checksum = crc32.Update(checksum, crc32.IEEETable, []byte(url.QueryEscape(cookie.Value)))
		fingerPrints[checksum] = true
	}
}

// Parse and simplify every item in the flow. Items that were not successfuly
// parsed are left as-is.
//
// If we manage to simplify a flow, the new data is placed in flowEntry.data
func ParseHttpFlow(flow *db.FlowEntry) {
	// Use a set to get rid of duplicates
	fingerprintsSet := make(map[uint32]bool)

	for idx := 0; idx < len(flow.Flow); idx++ {
		flowItem := &flow.Flow[idx]
		// TODO; rethink the flowItem format to make this less clunky
		reader := bufio.NewReader(strings.NewReader(flowItem.Data))

		if flowItem.From == "c" {
			// HTTP Request
			req, err := http.ReadRequest(reader)
			if err != nil {
				if *experimental {
					// Parse cookie and grab fingerprints
					AddFingerprints(req.Cookies(), fingerprintsSet)
				}
				continue
				//TODO; replace the HTTP data.
				// Remember to use a `LimitReader` when implementing this to prevent
				// decompressions bombs / DOS!
			}

		} else if flowItem.From == "s" {
			// Parse HTTP Response
			res, err := http.ReadResponse(reader, nil)
			if err != nil {
				continue
			}

			if *experimental {
				// Parse cookie and grab fingerprints
				AddFingerprints(res.Cookies(), fingerprintsSet)
			}

			// Substitute body
			encoding := res.Header["Content-Encoding"]
			if encoding == nil || len(encoding) == 0 {
				// If we don't find an encoding header, it is either not valid,
				// or already in plain text. In any case, we don't have to edit anything.
				continue
			}

			var newReader io.Reader
			if err != nil {
				// Failed to fully read the body. Bail out here
				continue
			}
			switch encoding[0] {
			case "gzip":
				newReader, err = handleGzip(res.Body)
				break
			case "br":
				newReader, err = handleBrotili(res.Body)
				break
			case "deflate":
				//TODO; verify this is correct
				newReader, err = handleGzip(res.Body)
				break
			default:
				// Skipped, unknown or identity encoding
				continue
			}

			// Replace the reader to allow for in-place decompression
			if err == nil && newReader != nil {
				// Limit the reader to prevent potential decompression bombs
				res.Body = io.NopCloser(io.LimitReader(newReader, DecompressionSizeLimit))
				// invalidate the content length, since decompressing the body will change its value.
				res.ContentLength = -1
				replacement, err := httputil.DumpResponse(res, true)
				if err != nil {
					// HTTPUtil failed us, continue without replacing anything.
					continue
				}
				// TODO; This can exceed the mongo document limit, so we need to take care to
				// cut the flowitems off at some stage. Same issue as in the reassembler, though
				// the data can now grow more after the fact.
				flowItem.Data = string(replacement)
			}
		}
	}

	if *experimental {
		// Use maps.Keys(fingerprintsSet) in the future
		flow.Fingerprints = make([]uint32, 0, len(fingerprintsSet))
		for k := range fingerprintsSet {
			flow.Fingerprints = append(flow.Fingerprints, k)
		}
	}
}

func handleGzip(r io.Reader) (io.Reader, error) {
	return gzip.NewReader(r)
}

func handleBrotili(r io.Reader) (io.Reader, error) {
	reader := brotli.NewReader(r)
	return reader, nil
}
