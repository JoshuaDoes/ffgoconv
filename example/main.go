package main

import (
	"fmt"
	"io"
	"os"

	"github.com/JoshuaDoes/ffgoconv"
)

func main() {
	muxOptions := &ffgoconv.TranscodeOptions{
		Codec:          "libmp3lame",
		Format:         "mp3",
		Volume:         256,
		Channels:       2,
		SampleRate:     48000,
		FrameDuration:  20,
		Bitrate:        320000,
		BufferedFrames: 100,
	}

	fmt.Println("> Creating muxing session...")
	muxSession := ffgoconv.NewMuxer(muxOptions)

	for _, source := range os.Args[1:] {
		identifier, err := muxSession.AddSource(source, nil)
		if err != nil {
			fmt.Printf("Error adding source: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("> Added \"%s\"\nIdentifier: %v\n", source, identifier)
	}

	fmt.Println("> Starting muxing session...")
	muxSession.Start()
	defer muxSession.Cleanup()

	fmt.Println("> Creating destination file...")
	dest, err := os.Create("example_out.mp3")
	if err != nil {
		fmt.Printf("Error creating destination file: %v\n", err)
		return
	}

	fmt.Println("> Writing to destination file...")
	io.Copy(dest, muxSession)

	fmt.Println("Done!")
}
