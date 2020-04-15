package main

import (
  "log"
  "fmt"
  "net/http"
  "context"
  "io"
  "io/ioutil"

	"errors"
	"strconv"
	"time"

  "golang.org/x/sync/errgroup"
)

var myClient *http.Client
func setupClient() {
  // Customize the Transport to have larger connection pool
  defaultRoundTripper := http.DefaultTransport
  defaultTransportPointer, ok := defaultRoundTripper.(*http.Transport)
  if !ok {
      panic(fmt.Sprintf("defaultRoundTripper not an *http.Transport"))
  }
  defaultTransport := *defaultTransportPointer // dereference it to get a copy of the struct that the pointer points to
  defaultTransport.MaxIdleConns = 100
  defaultTransport.MaxIdleConnsPerHost = 100

  myClient = &http.Client{Transport: &defaultTransport}
}

// Unconditionally processes the request through the multiplexing tunnel
type ParallelTransport struct {}
func (pt *ParallelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
  log.Println("Working on", req.URL.Path)

  download := ParallelDownload{
    OrigRequest: req,
    Count: 1,
  }

	// Get header from the url
  if err := download.getSizeAndCheckRangeSupport(); err != nil {
    return nil, err
  }

  if download.TotalSize > 1000000 {
    download.Count = 12
  } else if download.TotalSize > 250000 {
    download.Count = 8
  } else if download.TotalSize > 50000 {
    download.Count = 4
  } else if download.TotalSize > 10000 {
    download.Count = 2
  }

	// log.Printf("File size: %d bytes\n", download.TotalSize)

  return download.PerformForFakeResponse()
}

type ParallelDownload struct {
	OrigRequest *http.Request
	Count     int64
  HeadResponse *http.Response

	TotalSize int64
}

type DownloadChunk struct {
  Number int64
  StartPos int64
  EndPos int64
  Data []byte
}

// Unconditionally processes the request through the multiplexing tunnel
func (pt *ParallelDownload) PerformForFakeResponse() (*http.Response, error) {
  g, ctx := errgroup.WithContext(context.TODO())

	var start, end int64
	var partial_size = int64(pt.TotalSize / pt.Count)
	now := time.Now().UTC()
  var chunks []*DownloadChunk
	for num := int64(0); num < pt.Count; num++ {
		if num+1 == pt.Count {
			end = pt.TotalSize // last part
		} else {
			end = start + partial_size
		}
    // log.Printf("Part %d range %d - %d", num, start, end)
    chunk := &DownloadChunk{
      Number: num,
      StartPos: start,
      EndPos: end,
    }
    g.Go(func() error {
      return pt.GrabChunkData(chunk, ctx)
    })
    chunks = append(chunks, chunk)
		start = end
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("failed to download data: %w", err)
	}
  log.Println("Received", pt.TotalSize, "bytes in", time.Since(now), "for", pt.OrigRequest.URL.Path)
  // log.Println(pt)
  // log.Printf("Chunks: %+v", chunks)

	r, w := io.Pipe()
	go func() {
    for _, chunk := range chunks {
      w.Write(chunk.Data)
    }
		w.Close()
	}()

  resp := pt.HeadResponse
  resp.Body = r
  return resp, nil
}

func (pt *ParallelDownload) GrabChunkData(chunk *DownloadChunk, ctx context.Context) (error) {
  clone := pt.OrigRequest.Clone(ctx)
  clone.RequestURI = ""
  // Set range header
	clone.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", chunk.StartPos, chunk.EndPos-1))

	resp, err := myClient.Do(clone)
	if err != nil {
		return err
	}

	size, err := strconv.ParseInt(resp.Header["Content-Length"][0], 10, 64)
  if err != nil {
		return err
	}
  if size != (chunk.EndPos-chunk.StartPos) {
    log.Println("confusing sizes:", size, chunk.EndPos, chunk.StartPos, chunk.EndPos-chunk.StartPos)
  }

  bytes, err := ioutil.ReadAll(resp.Body) //Reads the entire response to memory
  if err != nil {
    return err
  }
  log.Println("part", chunk.Number, "of", pt.Count, "got", len(bytes), "bytes, from", chunk.StartPos)
  chunk.Data = bytes

	return nil
}

func (pt *ParallelDownload) getSizeAndCheckRangeSupport() (error) {
  clone := pt.OrigRequest.Clone(context.TODO())
  clone.Method = "HEAD"
  clone.RequestURI = ""

  // log.Printf("%+v", clone.Header)

	res, err := myClient.Do(clone)
	if err != nil {
		return err
	}
	// log.Printf("Response header: %v\n", res.Header)
	header := res.Header
	accept_ranges, supported := header["Accept-Ranges"]
	if !supported {
		return errors.New("Doesn't support header `Accept-Ranges`.")
	} else if supported && accept_ranges[0] != "bytes" {
		return errors.New("Support `Accept-Ranges`, but value is not `bytes`.")
	}

	size, err := strconv.ParseInt(header["Content-Length"][0], 10, 64)
  if err != nil {
    return err
  }

  pt.HeadResponse = res
  pt.TotalSize = size
  return nil
}
