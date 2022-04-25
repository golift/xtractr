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
	if len(archives) == 0 {
		log.Println("==> No archives found in:", paths)
	}

	total := 0
	count := 0

	for _, files := range archives {
		total += len(files)
	}

	for _, files := range archives {
		for _, fileName := range files {
			count++
			log.Printf("==> Extracting Archive (%d/%d): %s", count, total, fileName)

			start := time.Now()

			size, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
				FilePath:  fileName, // Path to archive being extracted.
				OutputDir: output,   // Folder to extract archive into.
				FileMode:  0o644,    // nolint:gomnd // Write files with this mode.
				DirMode:   0o755,    // nolint:gomnd // Write folders with this mode.
				Password:  "",       // (RAR) Archive password. Blank for none.
			})
			if err != nil {
				log.Printf("[ERROR] Archive: %s: %v", fileName, err)
				continue
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			log.Printf("==> Extracted Archive %s in %v: bytes: %d, files: %d", fileName, elapsed, size, len(files))
			log.Printf("==> Files:\n - %s", strings.Join(files, "\n - "))
		}
	}
}

func getArchives(paths []string) map[string][]string {
	archives := map[string][]string{}

	for _, fileName := range paths {
		switch fileInfo, err := os.Stat(fileName); {
		case err != nil:
			log.Fatalf("[ERROR] Reading Path: %s: %s", fileName, err)
		case fileInfo.IsDir():
			for k, v := range xtractr.FindCompressedFiles(fileName) {
				archives[k] = v
			}
		default:
			archives[fileName] = []string{fileName}
		}
	}

	return archives
}
