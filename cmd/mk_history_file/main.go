// makes bigger history files
// start with config_snapshot.json.part1
// insert some number of config_snapshot.json.items contents (~648 items)
// end with `]}` to make it valid json
// the partial files are embedded into the binary
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	part1File = "config_snapshot.json.part1"
	itemsFile = "config_snapshot.json.items"
	ending    = "]}"
)

//go:embed config_snapshot.json.part1
//go:embed config_snapshot.json.items
var res embed.FS

// cmd-line option
var requestedItemCount int

func itemCount(items []byte) int {
	numItems := 0

	for _, i := range items {
		if i == '{' {
			numItems++
		}
	}
	return numItems
}

func main() {
	var bytesWritten int64 = 0
	itemChunks := 0

	flag.IntVar(&requestedItemCount, "count", 500, "approximate desired config item count")
	flag.Parse()

	// read
	part1, err := res.ReadFile(part1File)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening file %s: %s", part1File, err)
		os.Exit(1)
	}

	items, err := res.ReadFile(itemsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening file %s: %s", itemsFile, err)
		os.Exit(1)
	}

	itemsSize := itemCount(items)
	// how many item chunks to write?
	if requestedItemCount > 0 {
		itemChunks = (requestedItemCount / itemsSize) + 1
	}

	// write
	dest := os.Stdout

	w, err := bytes.NewBuffer(part1).WriteTo(dest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error copying part1 to stdout: %s", err)
		os.Exit(1)
	}
	bytesWritten += w

	// write itemChunks blocks
	for c := 0; c < itemChunks; c++ {
		w, err = bytes.NewBuffer(items).WriteTo(dest)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error copying items to stdout: %s", err)
			os.Exit(1)
		}

		bytesWritten += w

		// todo append a comma after each item block, except last one
		if c < itemChunks-1 {
			w, err := dest.Write([]byte(","))
			if err != nil {
				fmt.Fprintf(os.Stderr, "error writing trailing comma after item block %d", c)
				os.Exit(1)
			}
			bytesWritten += int64(w)
		}

	}

	r := strings.NewReader(ending)
	w, err = io.Copy(dest, r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error copying ending to stdout: %s", err)
		os.Exit(1)
	}
	fmt.Println()
	bytesWritten += w + 1

	fmt.Fprintf(os.Stderr, "wrote chunks: %d, items: %d, bytes: %d\n",
		itemChunks, itemChunks*itemsSize, bytesWritten)
}
