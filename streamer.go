package ffgoconv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os/exec"
)

// Streamer contains all the data required to run a streaming session.
type Streamer struct {
	Process *exec.Cmd
	running bool
	closed  bool
	Error   error

	Volume float64

	Stderr io.ReadCloser
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}

// NewStreamer returns an initialized *Streamer or an error if one could not be created.
//
// If filepath is empty, the ffmpeg process will not start. You can specify any ffmpeg-supported location, such as a network URL or a local filepath.
// If args is nil or empty, the default values will be used. Do not specify your own args unless you understand how ffgoconv functions.
// The variable volume must be a floating-point number between 0 and 1, representing a percentage value. For example, 20% volume would be 0.2.
func NewStreamer(filepath string, args []string, volume float64) (*Streamer, error) {
	if filepath == "" {
		return nil, errors.New("ffgoconv: streamer: filepath must not be empty string")
	}
	if args == nil || len(args) == 0 {
		args = []string{
			"-stats",
			"-i", filepath,
			"-map", "0:a",
			"-acodec", "pcm_f64le",
			"-f", "f64le",
			"-vol", "256",
			"-ar", "48000",
			"-ac", "2",
			"-frame_duration", "20",
			"-threads", "1",
			"pipe:1",
		}
	}
	if volume < 0.0 || volume > 2.0 {
		return nil, errors.New("ffgoconv: streamer: volume must not be less than 0.0 (0%) or greater than 2.0 (200%)")
	}

	ffmpeg := exec.Command("ffmpeg", args...)

	stderrPipe, err := ffmpeg.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdinPipe, err := ffmpeg.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := ffmpeg.StdoutPipe()
	if err != nil {
		return nil, err
	}

	err = ffmpeg.Start()
	if err != nil {
		stderrData, _ := ioutil.ReadAll(stderrPipe)
		stdoutData, _ := ioutil.ReadAll(stdoutPipe)

		stderrPipe.Close()
		stdinPipe.Close()
		stdoutPipe.Close()

		err = fmt.Errorf("ffgoconv: streamer: error starting ffmpeg: %v; %v; %v", err, stderrData, stdoutData)
		return nil, err
	}

	go func() {
		if err := ffmpeg.Wait(); err != nil {
			stderrPipe.Close()
			stdinPipe.Close()
			stdoutPipe.Close()
		}
	}()

	return &Streamer{
		Process: ffmpeg,
		running: true,
		Stderr:  stderrPipe,
		Stdin:   stdinPipe,
		Stdout:  stdoutPipe,
		Volume:  volume,
	}, nil
}

// Read implements an io.Reader wrapper around *Streamer.Stdout.
func (streamer *Streamer) Read(data []byte) (n int, err error) {
	if streamer.closed {
		return 0, errors.New("ffgoconv: streamer: closed")
	}

	n, err = streamer.Stdout.Read(data)
	return n, err
}

// ReadSample returns the next audio sample from the streaming session.
func (streamer *Streamer) ReadSample() (float64, error) {
	if streamer.closed {
		return 0, errors.New("ffgoconv: streamer: closed")
	}

	sample := make([]byte, 8) // sizeof(float64) == 8

	n, err := streamer.Read(sample)
	if err != nil {
		return 0, err
	}
	if n != 8 {
		return 0, errors.New("streamer: read: size of sample must be 8")
	}

	u64 := binary.LittleEndian.Uint64(sample)
	fSample := math.Float64frombits(u64)

	return fSample, nil
}

// Write implements an io.Writer wrapper around *Streamer.Stdin.
func (streamer *Streamer) Write(data []byte) error {
	if streamer.closed {
		return errors.New("ffgoconv: streamer: closed")
	}

	_, err := streamer.Stdin.Write(data)
	if err != nil {
		return err
	}
	return nil
}

// WriteSample writes a new audio sample to the streaming session.
func (streamer *Streamer) WriteSample(sample float64) error {
	if streamer.closed {
		return errors.New("ffgoconv: streamer: closed")
	}

	var bs [8]byte
	u64 := math.Float64bits(sample)
	binary.LittleEndian.PutUint64(bs[:], u64)

	if _, err := streamer.Stdin.Write(bs[:]); err != nil {
		return err
	}

	return nil
}

// Err returns the latest streaming error.
func (streamer *Streamer) Err() error {
	return streamer.Error
}

// SetVolume sets the volume of the finalized audio.
func (streamer *Streamer) SetVolume(volume float64) error {
	if streamer.closed {
		return errors.New("ffgoconv: streamer: closed")
	}
	if volume < 0.0 || volume > 2.0 {
		return errors.New("ffgoconv: volume: volume must not be less than 0.0 (0%) or greater than 2.0 (200%)")
	}
	streamer.Volume = volume
	return nil
}

// Close closes the streaming session and renders the streamer unusable.
func (streamer *Streamer) Close() {
	if streamer.closed {
		return
	}
	streamer.Process.Process.Kill()
	streamer.Stderr.Close()
	streamer.Stdin.Close()
	streamer.Stdout.Close()
	streamer.closed = true
	streamer.running = false
}

func (streamer *Streamer) setError(err error) {
	streamer.Error = err
}
