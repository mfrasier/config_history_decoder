//Package config_decoder is used to decode AWS Config message streams
package config_decoder

// todo
// check context.Done() in leaf funcs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

//ItemTransformSpec specifies which fields to copy from parent to child items and the items field
// Fields maps source key name to dest key name. If dest value is "", use the original name.
//  The Field value must be of type string.
// ItemsField identifies the key holding the array of items to split
// Currently, the Fields must be encountered before ItemsField in the source stream
type ItemTransformSpec struct {
	Fields     map[string]string
	ItemsField string
}

//WorkerStatus are worker status messages
type WorkerStatus struct {
	WorkerNum  int
	ItemCount  int
	ByteCount  int
	StartTime  string
	EndTime    string
	Duration   time.Duration
	ErrorCount int
	Status     string
}

//ItemWriter is the interface for item writers
type ItemWriter interface {
	Write(map[string]interface{}) error
}

//NullWriter is a noop ItemWriter
type NullWriter struct{}

// WriteItem implements ItemWriter for NullWriter
func (nw NullWriter) Write(item map[string]interface{}) error {
	// do nothing
	return nil
}

// NullWriterFactory creates NullWriter objects
func NullWriterFactory() func() ItemWriter {
	return func() ItemWriter {
		return NullWriter{}
	}
}

//FileWriter is an ItemWriter that writes to an io.Writer
//todo add things like line terminator, if needed
type FileWriter struct {
	writer      io.Writer
	termination []byte
}

// WriteItem implements ItemWriter for FileWriter
func (fw FileWriter) Write(item map[string]interface{}) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}

	if fw.termination != nil {
		b = append(b, fw.termination...)
	}

	_, err = fw.writer.Write(b)
	if err != nil {
		return err
	}

	return nil
}

// FileWriterFactory creates FileWriter objects that write to io.Writer w
func FileWriterFactory(w io.Writer, termination []byte) func() ItemWriter {
	return func() ItemWriter {
		return FileWriter{w, termination}
	}
}

//WriterPool is a pool of <size> ItemWriters, created by the <writerFactory>
type WriterPool struct {
	size          int
	writerFactory func() ItemWriter
	chItem        chan map[string]interface{}
	chStatus      chan WorkerStatus
}

//NewWriterPool creates and returns a WriterPool
// Creates <size> ItemWriters, which read data items from <chData>
// todo report errors up
func NewWriterPool(ctx context.Context, f func() ItemWriter, size int, chData chan map[string]any) WriterPool {
	wp := WriterPool{writerFactory: f, size: size}
	wp.chItem = chData
	wp.chStatus = make(chan WorkerStatus, 8)

	// init pool of <size> goroutines receiving from chData
	for c := 0; c < size; c++ {
		go func(ctx context.Context, worker int) {
			w := wp.writerFactory()

			startTime := time.Now().UTC()
			status := WorkerStatus{
				WorkerNum: worker,
				StartTime: startTime.Format(time.RFC3339Nano),
				Status:    "starting",
			}

			for i := range wp.chItem {
				status.ItemCount++

				// todo should benchmark this to see if it's costly
				status.ByteCount += len(fmt.Sprintf("%s", i))

				err := w.Write(i)
				if err != nil {
					status.ErrorCount++
					_, _ = fmt.Fprintf(os.Stderr, "writer (%d) write error: %s", worker, err)
				}
			}

			// populate status and signal with data
			endTime := time.Now().UTC()
			status.EndTime = endTime.Format(time.RFC3339Nano)
			status.Duration = endTime.Sub(startTime)
			status.Status = "ended normally"
			wp.chStatus <- status
		}(ctx, c)
	}

	return wp
}

//addMetadata adds data from original message to metadata for new message
func addMetadata(metadata map[string]any, key string, val json.Token) error {
	//snapshotKey is the new field where snapshot-specific data is added to metadata
	//i.e. metadata["config_snapshot"]
	const snapshotKey = "config_snapshot"

	skMap, exists := metadata[snapshotKey]
	if !exists {
		metadata[snapshotKey] = make(map[string]string)
	}
	skMap = metadata[snapshotKey]

	if val, ok := val.(string); ok {
		skMap.(map[string]string)[key] = val
	} else {
		return fmt.Errorf("addMetadata: value %[1]v is not a string type, but a %[1]T", val)
	}

	return nil
}

//DecodeAndSplitItems decodes json containing an array of items
//persisting specified parent field values to the emitted item
// todo make use of ctx
func DecodeAndSplitItems(ctx context.Context, r io.Reader, writerFactory func() ItemWriter, poolSize int, spec ItemTransformSpec) (chan WorkerStatus, chan error) {

	cItems := make(chan map[string]any, 0)
	cErrors := make(chan error, 0)
	pool := NewWriterPool(ctx, writerFactory, poolSize, cItems)

	//metadata is map of field additions from source to new item
	metadata := make(map[string]any)
	metadata["event_type"] = "config_snapshot"
	metadata["event_source"] = "something_useful"
	metadata["ingest_time"] = time.Now().UTC().Format(time.RFC3339Nano)

	go func() {
		defer close(cItems)
		defer close(cErrors)
		dec := json.NewDecoder(r)

		// we expect the json document is an object
		if err := expect(dec, json.Delim('{')); err != nil {
			cErrors <- fmt.Errorf("DecodeAndSplitItems: %w", err)
			return
		}

		for dec.More() {
			// get field name
			t, err := dec.Token()
			if err != nil {
				cErrors <- fmt.Errorf("DecodeAndSplitItems: %w", err)
				return
			}

			// handle fields
			if f, ok := t.(string); ok {
				if f == spec.ItemsField {
					// items array
					_, _ = fmt.Fprintf(os.Stderr, "handling %s array...\n", t)
					err := decodeItems(dec, metadata, cItems, cErrors)
					if err != nil {
						// presume we can't continue. e.g. didn't find starting '['
						cErrors <- fmt.Errorf("DecodeAndSplitItems: %w", err)
						return
					}
				} else if tfv, ok := spec.Fields[f]; ok {
					// store field to transfer to new item
					v, err := dec.Token()
					if err != nil {
						cErrors <- fmt.Errorf(
							"DecodeAndSplitItems: error getting token for field %q: %w", f, err,
						)
						return
					}

					// ensure field value is not a json.Delim type
					if _, isDelim := v.(json.Delim); isDelim {
						cErrors <- fmt.Errorf(
							"DecodeAndSplitItems: %s value %s is of unexpected type json.Delim", f, v,
						)
						return
					} else {
						// populate metadata
						// use original field name if destination name is ""
						if tfv == "" {
							tfv = f
						}

						err = addMetadata(metadata, tfv, v)
						if err != nil {
							cErrors <- fmt.Errorf("DecodeAndSplitItems: %w", err)
							return
						}
					}
				} else {
					// skip value if not a field we want
					_, _ = fmt.Fprintf(os.Stderr, "skipping field %q\n", t)
					if err := skip(dec); err != nil {
						cErrors <- fmt.Errorf("DecodeAndSplitItems: %w", err)
						return
					}
				}
			} else {
				fmt.Printf("token %v is not of type string\n", t)
			}
		}

		fmt.Println("\ndecoder goroutine ended normally")
	}()

	return pool.chStatus, cErrors
}

//decodeItems decodes and emits new items, enriched with fields from transforms
func decodeItems(dec *json.Decoder, metadata map[string]any, cItems chan map[string]any, cErrors chan error) error {
	// we expect a json array of items
	if err := expect(dec, json.Delim('[')); err != nil {
		return fmt.Errorf("decodeItems: begin bracket not found: %w", err)
	}

	// while there are more json array elements ...
	for dec.More() {
		var v map[string]any

		if err := dec.Decode(&v); err != nil {
			cErrors <- fmt.Errorf("decodeItems: %w", err)
		}

		// assign any parent values to item and signal the channel with data
		v["metadata"] = metadata
		for key, val := range metadata {
			v[key] = val
		}

		cItems <- v
	}
	return nil
}

// skip skips the next value in the JSON document.
func skip(d *json.Decoder) error {
	n := 0
	for {
		t, err := d.Token()
		if err != nil {
			return fmt.Errorf("skip: %w", err)
		}

		switch t {
		case json.Delim('['), json.Delim('{'):
			n++
		case json.Delim(']'), json.Delim('}'):
			n--
		}
		if n == 0 {
			return nil
		}
	}
}

// expect returns an error if the next token in the document is not expectedT.
func expect(d *json.Decoder, expectedT interface{}) error {
	t, err := d.Token()
	if err != nil {
		return fmt.Errorf("expect: %w", err)
	}
	if t != expectedT {
		return fmt.Errorf("got token %v, want token %v", t, expectedT)
	}
	return nil
}
