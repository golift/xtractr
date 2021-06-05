// Package main is a binary used for demonstration purposes. It works, but lacks
// the features you can program into your own application. This is just a quick
// sample provided to show one way to interface this library.
package main

import (
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"golift.io/xtractr"
)

func main() {
	pwd, _ := os.Getwd()
	output := flag.String("output", pwd, "Output directory, default is current directory")

	flag.Parse()
	log.SetFlags(0)

	inputFiles := flag.Args()
	if len(inputFiles) < 1 {
		log.Printf("If you pass a directory, this app will extract every archive in it.")
		log.Fatalf("Usage: %s [-output <path>] <path> [paths...]", os.Args[0])
	}

	processInput(inputFiles, *output)
}

func processInput(paths []string, output string) {
	log.Printf("==> Output Path: %s", output)

	archives := getArchives(paths)
	for i, f := range archives {
		log.Printf("==> Extracting Archive (%d/%d): %s", i, len(archives), f)

		start := time.Now()

		size, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
			FilePath:  f,      // Path to archive being extracted.
			OutputDir: output, // Folder to extract archive into.
			FileMode:  0644,   // nolint:gomnd // Write files with this mode.
			DirMode:   0755,   // nolint:gomnd // Write folders with this mode.
			Password:  "",     // (RAR) Archive password. Blank for none.
		})
		if err != nil {
			log.Printf("[ERROR] Archive: %s: %v", f, err)
			continue
		}

		elapsed := time.Since(start).Round(time.Millisecond)
		log.Printf("==> Extracted Archive %s in %v: bytes: %d, files: %d", f, elapsed, size, len(files))
		log.Printf("==> Files:\n - %s", strings.Join(files, "\n - "))
	}
}

func getArchives(paths []string) []string {
	archives := []string{}

	for _, f := range paths {
		switch fileInfo, err := os.Stat(f); {
		case err != nil:
			log.Fatalf("[ERROR] Reading Path: %s: %s", f, err)
		case fileInfo.IsDir():
			archives = append(archives, xtractr.FindCompressedFiles(f)...)
		default:
			archives = append(archives, f)
		}
	}

	return archives
}
