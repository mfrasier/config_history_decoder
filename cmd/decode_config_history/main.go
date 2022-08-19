package main

import (
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"github.com/mfrasier/decode_json_stream/config_decoder"
	"go.uber.org/zap"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const defaultFile = "./config_decoder/testdata/123456789012_Config_us-east-1_ConfigSnapshot_20220809T134016Z_0f1d63cc-aee4-48b8-82ab-4f38087be14e.json.gz"

// config variables
var (
	inputFile  string
	poolSize   int
	timeout    time.Duration
	writerKind string
)

//signalHandler handles OS termination signals
func signalHandler() chan bool {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan bool, 1)

	go func() {
		sig := <-sigs
		fmt.Printf("\nreceived signal %s\n", sig)
		done <- true
	}()

	return done
}

func parseCmdLine() {
	flag.StringVar(&inputFile, "file", defaultFile, "name of input file")
	flag.DurationVar(&timeout, "timeout", 1*time.Hour, "maximum time for program to run (a duration)")
	flag.StringVar(&writerKind, "writer", "null", "item writer type [null|file]")
	flag.IntVar(&poolSize, "pool-size", runtime.GOMAXPROCS(0), "writer pool size")

	flag.Parse()
}

//createLogger builds a zap loqger
func createLogger() (*zap.SugaredLogger, error) {
	zapLogger, err := zap.NewDevelopment()
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate zap logger: %s", err)
	}
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			log.Printf("zap logger sync failed: %s\n", err)
		}
	}(zapLogger)

	return zapLogger.Sugar(), nil
}

// formats number as human readable
// copy/pasted
func byteCountSI(b int) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}

func main() {
	logger, err := createLogger()
	if err != nil {
		log.Fatal(err)
	}

	start := time.Now()
	chSignalHandler := signalHandler()

	// get any config values from command line
	parseCmdLine()

	in, err := os.Open(inputFile)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer in.Close()

	// handle gzipped or uncompressed files
	var r io.Reader
	if strings.HasSuffix(inputFile, ".gz") {
		r, err = gzip.NewReader(in)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stdout, "gzip error reading input file: %s\n", err)
		}
	} else {
		r = in
	}

	_, _ = fmt.Fprintf(os.Stderr, "opened file %s\n", inputFile)

	// create context for downstream
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	spec := config_decoder.ItemTransformSpec{
		Fields: map[string]string{
			"configSnapshotId": "",
			"fileVersion":      "",
		},
		ItemsField: "configurationItems",
	}

	// create writer factory for pool
	var wFactory func() config_decoder.ItemWriter
	switch writerKind {
	case "null":
		wFactory = config_decoder.NullWriterFactory()
	case "file":
		wFactory = config_decoder.FileWriterFactory(os.Stdout, []byte{'\n'})
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown writer type %q specified\n", writerKind)
		_, _ = fmt.Fprintf(os.Stderr, "for help, run %s -h \n", os.Args[0])
		os.Exit(1)
	}

	_, _ = fmt.Fprintln(os.Stderr, "decoding json as stream ...")
	chStatus, chErrors := config_decoder.DecodeAndSplitItems(ctx, r, wFactory, poolSize, spec)

ForSelectLoop:
	for {
		select {
		case err := <-chErrors:
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "error decoding web log object stream: %s\n", err)
				logger.Errorw("error decoding web log object stream",
					"message", "error decoding web log object stream",
					"cause", err.Error())
			}
			break ForSelectLoop
		case <-ctx.Done():
			_, _ = fmt.Fprintf(os.Stderr, "\ndecoder cancelled: %s", ctx.Err())
			break ForSelectLoop
		case <-chSignalHandler:
			_, _ = fmt.Fprintln(os.Stderr, "received shutdown signal")
			break ForSelectLoop
		}
	}

	itemCount, itemBytes := 0, 0
	for i := 0; i < poolSize; i++ {
		s := <-chStatus
		itemCount += s.ItemCount
		itemBytes += s.ByteCount
		_, _ = fmt.Fprintf(os.Stderr, "worker status message: %+v\n", s)
	}

	_, _ = fmt.Fprintf(os.Stderr, "read %d config items (%s) in %s\n",
		itemCount, byteCountSI(itemBytes), time.Since(start))
	//logger.Infow("done",
	//	"message", "application is done",
	//	"timestamp", time.Now().UTC().Format(time.RFC3339Nano),
	//	"itemCount", itemCount,
	//	"duration", time.Since(start),
	//	"tags", []string{"tag1", "tag2"})
}
