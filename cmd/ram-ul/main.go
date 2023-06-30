package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/pojntfx/go-nbd/pkg/backend"
	v1frpc "github.com/pojntfx/r3map/pkg/api/frpc/mount/v1"
	lbackend "github.com/pojntfx/r3map/pkg/backend"
	"github.com/pojntfx/r3map/pkg/chunks"
	"github.com/pojntfx/r3map/pkg/services"
	"github.com/pojntfx/r3map/pkg/utils"
)

const (
	backendTypeFile      = "file"
	backendTypeMemory    = "memory"
	backendTypeDirectory = "directory"
)

var (
	knownBackendTypes = []string{backendTypeFile, backendTypeMemory, backendTypeDirectory}

	errUnknownBackend = errors.New("unknown backend")
)

func main() {
	laddr := flag.String("addr", ":1337", "Listen address")

	size := flag.Int64("size", 4096*1024*1024, "Size of the memory region or file to allocate")
	chunkSize := flag.Int64("chunk-size", 4096, "Chunk size to use")

	bck := flag.String(
		"backend",
		backendTypeFile,
		fmt.Sprintf(
			"Backend to use (one of %v)",
			knownBackendTypes,
		),
	)
	location := flag.String("location", filepath.Join(os.TempDir(), "ram-ul"), "Backend's directory (for directory backend) or file (for file backend)")
	chunking := flag.Bool("chunking", true, "Whether the backend requires to be interfaced with in fixed chunks in tests")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	var b backend.Backend
	switch *bck {
	case backendTypeMemory:
		b = backend.NewMemoryBackend(make([]byte, *size))

	case backendTypeFile:
		if err := os.MkdirAll(filepath.Dir(*location), os.ModePerm); err != nil {
			panic(err)
		}

		if err := os.RemoveAll(*location); err != nil {
			panic(err)
		}

		file, err := os.Create(*location)
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(file.Name())

		if err := file.Truncate(*size); err != nil {
			panic(err)
		}

		b = backend.NewFileBackend(file)

	case backendTypeDirectory:
		if err := os.RemoveAll(*location); err != nil {
			panic(err)
		}

		if err := os.MkdirAll(*location, os.ModePerm); err != nil {
			panic(err)
		}

		b = lbackend.NewDirectoryBackend(*location, *size, *chunkSize, 512, false)
	default:
		panic(errUnknownBackend)
	}

	if *chunking {
		b = lbackend.NewReaderAtBackend(
			chunks.NewArbitraryReadWriterAt(
				chunks.NewChunkedReadWriterAt(
					b, *chunkSize, *size / *chunkSize),
				*chunkSize,
			),
			(b).Size,
			(b).Sync,
			false,
		)
	}

	errs := make(chan error)
	server, err := v1frpc.NewServer(services.NewBackendFrpc(services.NewBackend(b, *verbose, *chunkSize)), nil, nil)
	if err != nil {
		panic(err)
	}

	log.Println("Listening on", *laddr)

	go func() {
		if err := server.Start(*laddr); err != nil {
			if !utils.IsClosedErr(err) {
				errs <- err
			}

			return
		}
	}()

	for err := range errs {
		if err != nil {
			panic(err)
		}
	}
}
