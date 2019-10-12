package main

import (
	"io/ioutil"
	"log"
	"os"
	"sync"
	"time"

	"github.com/JoshuaDoes/ffgoconv"
)

func main() {
	//Make sure audio files are specified in program args
	if len(os.Args) < 2 {
		panic("You must specify one or more audio files to transmux")
	}

	//Get list of specified files
	files := os.Args[1:]

	//Remove test file if exists
	os.Remove("./test.mp3")

	//Create slice of streamers
	streamers := make([]*ffgoconv.Streamer, 0)

	//Create new transmuxing session, tell it to output encoded data to final stream's stdout, use MP3 params at 320Kbps
	transmuxer, err := ffgoconv.NewTransmuxer(streamers, "pipe:1", "libmp3lame", "mp3", "320k", 1)
	if err != nil {
		log.Println(err)
		return
	}

	//Add all files to transmuxer
	if len(files) > 0 {
		for i, file := range files {
			log.Println("Adding stream [:", i+1, "]:", file)
			_, err = transmuxer.AddStreamer(file, nil, 1.0)
			if err != nil {
				panic(err)
			}
		}
	}

	//Create and start waitgroup in case you wanna wait for transmuxer to finish
	var wg sync.WaitGroup
	run(transmuxer, &wg)

	log.Println("Sleeping for 5 seconds...")
	time.Sleep(5 * time.Second)

	log.Println("Reading as much as possible...")
	test := make([]byte, 150000)
	transmuxer.FinalStream.Read(test)

	log.Println("Writing", len(test), "bytes to test.mp3...")
	ioutil.WriteFile("test.mp3", test, 0644)
}

func run(transmuxer *ffgoconv.Transmuxer, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		transmuxer.Run()
	}()
}
