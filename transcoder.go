package ffgoconv

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/superwhiskers/crunch"
)

// AudioApplication is an application profile for encoding of specific audio formats
type AudioApplication string

var (
	AudioApplicationVoip     AudioApplication = "voip"     // Favor improved speech intelligibility
	AudioApplicationAudio    AudioApplication = "audio"    // Favor faithfulness to the input
	AudioApplicationLowDelay AudioApplication = "lowdelay" // Restrict to only the lowest delay modes
)

var (
	ErrBadFrame = errors.New("bad frame")
)

type TranscodeOptions struct {
	Codec            string           // Audio codec, run "ffmpeg -codecs" to see what codecs are supported by your FFmpeg installation
	Format           string           // Output audio format, run "ffmpeg -formats" to see what formats are supported by your FFmpeg installation
	Volume           int              // Volume of the output audio (0-512, 256 = 100%, 512 = 200%)
	Channels         int              // Audio channel count
	SampleRate       int              // Audio sampling rate (ex: 48000)
	FrameDuration    int              // Audio frame duration (20ms, 40ms, or 60ms)
	Bitrate          int              // Audio encoding bitrate in KB/s (8-128)
	PacketLoss       int              // Expected packet loss percentage
	Application      AudioApplication // Audio application type
	CoverFormat      string           // Format of encoded cover art (ex: "jpeg", "png")
	CompressionLevel int              // Compression level (0-10)
	BufferedFrames   int              // How big the frame buffer should be
	VBR              bool             // Whether or not to use variable bitrate
	Threads          int              // Number of threads to use, 0 for automatic threading

	// The FFmpeg audio filters to use, leave empty to use no filters
	// See more info here: https://ffmpeg.org/ffmpeg-filters.html#Audio-Filters
	AudioFilter string
}

func (opt TranscodeOptions) PCMFrameLen() int {
	return 960 * opt.Channels * (opt.FrameDuration / 20)
}

func (opt *TranscodeOptions) Validate() error {
	if opt.Volume < 0 || opt.Volume > 512 {
		return errors.New("volume out of bounds (0-512)")
	}

	if opt.FrameDuration != 20 && opt.FrameDuration != 40 && opt.FrameDuration != 60 {
		return errors.New("invalid frame duration (20, 40, 60)")
	}

	if opt.PacketLoss < 0 || opt.PacketLoss > 100 {
		return errors.New("invalid packet loss percentage (0-100)")
	}

	if opt.Application != AudioApplicationAudio && opt.Application != AudioApplicationVoip && opt.Application != AudioApplicationLowDelay && opt.Application != "" {
		return errors.New("invalid audio application")
	}

	if opt.CompressionLevel < 0 || opt.CompressionLevel > 10 {
		return errors.New("compression level out of bounds (0-10)")
	}

	if opt.Threads < 0 {
		return errors.New("thread count cannot be less than 0")
	}

	return nil
}

// StdTranscodeOptions is the standard options for transcoding
var StdTranscodeOptions = &TranscodeOptions{
	Codec:            "pcm_s16le",
	Format:           "s16le",
	Volume:           256,
	Channels:         2,
	SampleRate:       48000,
	FrameDuration:    20,
	Bitrate:          128,
	Application:      AudioApplicationAudio,
	CompressionLevel: 10,
	PacketLoss:       1,
	BufferedFrames:   100,
	VBR:              true,
}

// RawTranscodeOptions is the raw PCM Signed 16-bit LE option for transcoding
var RawTranscodeOptions = &TranscodeOptions{
	Codec:          "pcm_s16le",
	Format:         "s16le",
	Volume:         256,
	Channels:       2,
	SampleRate:     48000,
	FrameDuration:  20,
	Bitrate:        128,
	BufferedFrames: 100,
}

type TranscodeStats struct {
	Size     int
	Duration time.Duration
	Bitrate  float32
	Speed    float32
}

type Frame struct {
	data []byte
}

type TranscodeSession struct {
	sync.Mutex

	options    *TranscodeOptions
	pipeReader io.Reader
	filePath   string

	running      bool
	started      time.Time
	frameChannel chan *Frame
	process      *os.Process
	lastStats    *TranscodeStats

	lastFrame int
	err       error

	ffmpegOutput string

	unreadBuffer *CrunchMiniBuffer
}

func TranscodeMem(r io.Reader, options *TranscodeOptions) (session *TranscodeSession, err error) {
	err = options.Validate()
	if err != nil {
		return
	}

	session = &TranscodeSession{
		options:      options,
		pipeReader:   r,
		frameChannel: make(chan *Frame, options.BufferedFrames),
	}

	go session.run()
	return
}

func TranscodeFile(path string, options *TranscodeOptions) (session *TranscodeSession, err error) {
	err = options.Validate()
	if err != nil {
		return
	}

	session = &TranscodeSession{
		options:      options,
		filePath:     path,
		frameChannel: make(chan *Frame, options.BufferedFrames),
	}

	go session.run()
	return
}

func (session *TranscodeSession) run() {
	// Reset the running state
	defer func() {
		session.Lock()
		session.running = false
		session.Unlock()
	}()

	session.Lock()
	session.running = true

	unreadBuffer := &crunch.MiniBuffer{}
	crunch.NewMiniBuffer(&unreadBuffer, nil)
	session.unreadBuffer = &CrunchMiniBuffer{unreadBuffer}

	inFile := "pipe:0"
	if session.filePath != "" {
		inFile = session.filePath
	}

	if session.options == nil {
		session.options = StdTranscodeOptions
	}

	vbrStr := "on"
	if !session.options.VBR {
		vbrStr = "off"
	}

	// Launch FFmpeg with the required arguments
	args := []string{
		"-stats",
		"-i", inFile,
		"-map", "0:a",
		"-acodec", session.options.Codec,
		"-f", session.options.Format,
		"-vbr", vbrStr,
		"-vol", strconv.Itoa(session.options.Volume),
		"-ar", strconv.Itoa(session.options.SampleRate),
		"-ac", strconv.Itoa(session.options.Channels),
		"-b:a", strconv.Itoa(session.options.Bitrate * 1000),
		"-frame_duration", strconv.Itoa(session.options.FrameDuration),
		"-threads", strconv.Itoa(session.options.Threads),
	}

	if session.options.CompressionLevel > 0 {
		args = append(args, "-compression_level", strconv.Itoa(session.options.CompressionLevel))
	}

	if session.options.Application != "" {
		args = append(args, "-application", string(session.options.Application))
	}

	if session.options.PacketLoss > 0 {
		args = append(args, "-packet_loss", strconv.Itoa(session.options.PacketLoss))
	}

	if session.options.AudioFilter != "" {
		args = append(args, "-af", session.options.AudioFilter)
	}

	args = append(args, "pipe:1")

	ffmpeg := exec.Command("ffmpeg", args...)

	if session.pipeReader != nil {
		ffmpeg.Stdin = session.pipeReader
	}

	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		session.Unlock()
		close(session.frameChannel)
		return
	}

	stderr, err := ffmpeg.StderrPipe()
	if err != nil {
		session.Unlock()
		close(session.frameChannel)
		return
	}

	err = ffmpeg.Start()
	if err != nil {
		session.Unlock()
		close(session.frameChannel)
		return
	}

	session.started = time.Now()

	session.process = ffmpeg.Process
	session.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go session.readStderr(stderr, &wg)

	defer close(session.frameChannel)
	session.readStdout(stdout)
	wg.Wait()
	err = ffmpeg.Wait()
	if err != nil {
		if err.Error() != "signal: killed" {
			session.Lock()
			session.err = err
			session.Unlock()
		}
	}
}

func (session *TranscodeSession) readStderr(stderr io.ReadCloser, wg *sync.WaitGroup) {
	defer wg.Done()

	bufReader := bufio.NewReader(stderr)
	var outBuf bytes.Buffer
	for {
		r, _, err := bufReader.ReadRune()
		if err != nil {
			break
		}

		switch r {
		case '\r':
			if outBuf.Len() > 0 {
				session.handleStderrLine(outBuf.String())
				outBuf.Reset()
			}
		case '\n':
			session.Lock()
			session.ffmpegOutput += outBuf.String() + "\n"
			session.Unlock()
			outBuf.Reset()
		default:
			outBuf.WriteRune(r)
		}
	}
}

func (session *TranscodeSession) handleStderrLine(line string) {
	if strings.Index(line, "size=") != 0 {
		return
	}

	var size int
	var timeH int
	var timeM int
	var timeS float32
	var bitrate float32
	var speed float32

	_, err := fmt.Sscanf(line, "size=%dkB time=%d:%d:%f bitrate=%fkbits/s speed=%fx", &size, &timeH, &timeM, &timeS, &bitrate, &speed)
	if err != nil {
		return
	}

	dur := time.Duration(timeH) * time.Hour
	dur += time.Duration(timeM) * time.Minute
	dur += time.Duration(timeS) * time.Second

	stats := &TranscodeStats{
		Size:     size,
		Duration: dur,
		Bitrate:  bitrate,
		Speed:    speed,
	}

	session.Lock()
	session.lastStats = stats
	session.Unlock()
}

func (session *TranscodeSession) readStdout(stdout io.ReadCloser) {
	for {
		frame := &Frame{
			data: make([]byte, 0),
		}

		frameBuf := &crunch.MiniBuffer{}
		crunch.NewMiniBuffer(&frameBuf)
		frameWrap := &CrunchMiniBuffer{frameBuf}

		bytesRead := 0
		for {
			if frameWrap.Len() == 0 {
				fmt.Println("Finished reading frame")
				break
			}

			tmp := make([]byte, 1)

			n, err := stdout.Read(tmp)
			if err == io.EOF {
				fmt.Println("Got EOF")
				break
			}
			if err != nil {
				break
			}

			if n == 0 {
				continue
			}

			//frameWrap.Grow(1)
			frameWrap.Write(tmp)

			bytesRead += n
			fmt.Println("Bytes read:", n)
		}

		if bytesRead == 0 {
			return
		}

		frameWrap.Bytes(&frame.data)
		fmt.Printf("Bytes read (%d): %v\n", bytesRead, frame.data)

		session.frameChannel <- frame

		session.Lock()
		session.lastFrame++
		session.Unlock()
	}
}

func (session *TranscodeSession) Stop() error {
	session.Lock()
	defer session.Unlock()
	if !session.running || session.process == nil {
		return errors.New("not running")
	}

	err := session.process.Kill()
	return err
}

func (session *TranscodeSession) ReadFrame() (frame []byte, err error) {
	//fmt.Println("Waiting for frame...")
	f := <-session.frameChannel
	if f == nil {
		fmt.Println("Got empty frame")
		return nil, io.EOF
	}

	//fmt.Println("Returning frame")
	return f.data, nil
}

func (session *TranscodeSession) Running() (running bool) {
	session.Lock()
	running = session.running
	session.Unlock()
	return
}

func (session *TranscodeSession) Stats() *TranscodeStats {
	s := &TranscodeStats{}
	session.Lock()
	if session.lastStats != nil {
		*s = *session.lastStats
	}
	session.Unlock()

	return s
}

func (session *TranscodeSession) Options() *TranscodeOptions {
	return session.options
}

func (session *TranscodeSession) Cleanup() {
	session.Stop()

	for _ = range session.frameChannel {
		//Wait until closed
	}
}

func (session *TranscodeSession) Read(p []byte) (n int, err error) {
	if session.unreadBuffer.Len() >= len(p) {
		//fmt.Printf("Length of unreadBuffer (%d) is greater than or equal to length of target buffer (%d)\n", session.unreadBuffer.Len(), len(p))
		n = len(p)
		return session.unreadBuffer.Read(p)
	}

	for session.unreadBuffer.Len() < len(p) {
		//fmt.Printf("Length of unreadBuffer (%d) is less than length of target buffer (%d)\n", session.unreadBuffer.Len(), len(p))
		f, err := session.ReadFrame()
		if err != nil {
			break
		}
		session.unreadBuffer.Write(f)
	}

	return session.unreadBuffer.Read(p)
}

func (session *TranscodeSession) FrameDuration() time.Duration {
	return time.Duration(session.options.FrameDuration) * time.Millisecond
}

func (session *TranscodeSession) Error() error {
	session.Lock()
	defer session.Unlock()
	return session.err
}

func (session *TranscodeSession) FFMPEGMessages() string {
	session.Lock()
	defer session.Unlock()
	return session.ffmpegOutput
}
