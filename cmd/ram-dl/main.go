package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pojntfx/go-nbd/pkg/backend"
	"github.com/pojntfx/go-nbd/pkg/server"
	v1frpc "github.com/pojntfx/r3map/pkg/api/frpc/mount/v1"
	lbackend "github.com/pojntfx/r3map/pkg/backend"
	"github.com/pojntfx/r3map/pkg/chunks"
	"github.com/pojntfx/r3map/pkg/device"
	"github.com/pojntfx/r3map/pkg/services"
	"github.com/pojntfx/r3map/pkg/utils"
)

func main() {
	raddr := flag.String("raddr", "localhost:1337", "Remote address for the fRPC r3map backend server")

	chunkSize := flag.Int64("chunk-size", 4096, "Chunk size to use")
	chunking := flag.Bool("chunking", true, "Whether the backend requires to be interfaced with in fixed chunks")

	verbose := flag.Bool("verbose", false, "Whether to enable verbose logging")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := v1frpc.NewClient(nil, nil)
	if err != nil {
		panic(err)
	}

	if err := client.Connect(*raddr); err != nil {
		panic(err)
	}
	defer client.Close()

	devPath, err := utils.FindUnusedNBDDevice()
	if err != nil {
		panic(err)
	}

	devFile, err := os.Open(devPath)
	if err != nil {
		panic(err)
	}
	defer devFile.Close()

	var b backend.Backend
	b = lbackend.NewRPCBackend(
		ctx,
		&services.BackendRemote{
			ReadAt: func(ctx context.Context, length int, off int64) (r services.ReadAtResponse, err error) {
				res, err := client.Backend.ReadAt(ctx, &v1frpc.ComPojtingerFelicitasR3MapMountV1ReadAtArgs{
					Length: int32(length),
					Off:    off,
				})
				if err != nil {
					return services.ReadAtResponse{}, err
				}

				return services.ReadAtResponse{
					N: int(res.N),
					P: res.P,
				}, err
			},
			WriteAt: func(context context.Context, p []byte, off int64) (n int, err error) {
				res, err := client.Backend.WriteAt(ctx, &v1frpc.ComPojtingerFelicitasR3MapMountV1WriteAtArgs{
					Off: off,
					P:   p,
				})
				if err != nil {
					return 0, err
				}

				return int(res.Length), nil
			},
			Size: func(context context.Context) (int64, error) {
				res, err := client.Backend.Size(ctx, &v1frpc.ComPojtingerFelicitasR3MapMountV1SizeArgs{})
				if err != nil {
					return 0, err
				}

				return res.Size, nil
			},
			Sync: func(context context.Context) error {
				if _, err := client.Backend.Sync(ctx, &v1frpc.ComPojtingerFelicitasR3MapMountV1SyncArgs{}); err != nil {
					return err
				}

				return nil
			},
		},
		*verbose,
	)

	size, err := b.Size()
	if err != nil {
		panic(err)
	}

	if *chunking {
		b = lbackend.NewReaderAtBackend(
			chunks.NewArbitraryReadWriterAt(
				chunks.NewChunkedReadWriterAt(
					b, *chunkSize, size / *chunkSize),
				*chunkSize,
			),
			(b).Size,
			(b).Sync,
			false,
		)
	}

	dev := device.NewDevice(
		b,
		devFile,

		&server.Options{
			MinimumBlockSize:   uint32(*chunkSize),
			PreferredBlockSize: uint32(*chunkSize),
			MaximumBlockSize:   uint32(*chunkSize),
		},
		nil,
	)

	var (
		errs = make(chan error)
		wg   sync.WaitGroup
	)
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		for err := range errs {
			if err != nil {
				panic(err)
			}
		}
	}()

	go func() {
		if err := dev.Wait(); err != nil {
			errs <- err

			return
		}

		close(errs)
	}()

	defer dev.Close()
	if err := dev.Open(); err != nil {
		panic(err)
	}

	if output, err := exec.Command("mkswap", devPath).CombinedOutput(); err != nil {
		log.Printf("Could not create swap partition: %s", output)

		panic(err)
	}

	if output, err := exec.Command("swapon", devPath).CombinedOutput(); err != nil {
		log.Printf("Could not enable partition for swap: %s", output)

		panic(err)
	}

	defer func() {
		if output, err := exec.Command("swapoff", devPath).CombinedOutput(); err != nil {
			log.Printf("Could not enable partition for swap: %s", output)

			panic(err)
		}
	}()

	log.Println("Ready on", devPath)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	<-ch

	if *verbose {
		log.Println("Gracefully shutting down")
	}

	go func() {
		<-ch

		log.Println("Forcefully exiting")

		os.Exit(1)
	}()
}
