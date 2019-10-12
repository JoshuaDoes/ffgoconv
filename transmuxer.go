package ffgoconv

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
)

// Transmuxer contains all the data required to run a transmuxing session.
type Transmuxer struct {
	sync.Mutex

	Streamers   []*Streamer
	FinalStream *Streamer
	running     bool
	closed      bool
	Error       error

	buffer []float64

	MasterVolume float64

	Stderr io.ReadCloser
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}

// NewTransmuxer returns an initialized *Transmuxer or an error if one could not be created.
//
// If streamers is nil, it will be initialized automatically with an empty slice of *Streamer.
//
// If codec is not specified, the ffmpeg process will not start. A list of possible codecs can be found with "ffmpeg -codecs".
//
// If format is not specified, the ffmpeg process will not start. A list of possible formats can be found with "ffmpeg -formats".
//
// If bitrate is not specified, the ffmpeg process will not start.
//
// The variable masterVolume must be a floating-point number between 0 and 1, representing a percentage value. For example, 20% volume would be 0.2.
//
// If outputFilepath is empty, a buffer of float64 PCM values will be initialized and the returned *Transmuxer can be then used as an io.Reader.
//
// If outputFilepath is "pipe:1", the FinalStream *Streamer can be used as an io.Reader to receive encoded audio data of the chosen codec in the chosen format.
func NewTransmuxer(streamers []*Streamer, outputFilepath, codec, format, bitrate string, masterVolume float64) (*Transmuxer, error) {
	if streamers == nil {
		streamers = make([]*Streamer, 0)
	}

	args := []string{
		"-stats",
		"-acodec", "pcm_f64le",
		"-f", "f64le",
		"-ar", "48000",
		"-ac", "2",
		"-i", "-",
		"-acodec", codec,
		"-f", format,
		"-vol", "256",
		"-ar", "48000",
		"-ac", "2",
		"-b:a", bitrate,
		"-threads", "1",
		outputFilepath,
	}

	var finalStream *Streamer
	var err error

	if outputFilepath != "" {
		finalStream, err = NewStreamer(outputFilepath, args, 1.0)
		if err != nil {
			return nil, err
		}

		return &Transmuxer{
			Streamers:    streamers,
			FinalStream:  finalStream,
			Stderr:       finalStream.Stderr,
			Stdin:        finalStream.Stdin,
			Stdout:       finalStream.Stdout,
			MasterVolume: masterVolume,
		}, nil
	}

	return &Transmuxer{
		Streamers:    streamers,
		MasterVolume: masterVolume,
		buffer:       make([]float64, 0),
	}, nil
}

// AddStreamer initializes and adds a *Streamer to the transmuxing session, or returns an error if one could not be initialized.
// See NewStreamer for info on supported arguments.
func (transmuxer *Transmuxer) AddStreamer(filepath string, args []string, volume float64) (*Streamer, error) {
	if transmuxer.closed {
		return nil, errors.New("ffgoconv: transmuxer: closed")
	}

	streamer, err := NewStreamer(filepath, args, volume)
	if err != nil {
		return nil, err
	}

	transmuxer.Streamers = append(transmuxer.Streamers, streamer)
	return streamer, nil
}

// SetMasterVolume sets the master volume of the finalized audio.
func (transmuxer *Transmuxer) SetMasterVolume(volume float64) error {
	if transmuxer.closed {
		return errors.New("ffgoconv: transmuxer: closed")
	}

	if volume < 0.0 || volume > 2.0 {
		return errors.New("ffgoconv: volume: volume must not be less than 0.0 (0%) or greater than 2.0 (200%)")
	}

	transmuxer.MasterVolume = volume
	return nil
}

// IsRunning returns whether or not the transmuxing session is running.
func (transmuxer *Transmuxer) IsRunning() bool {
	return transmuxer.running
}

// Run starts the transmuxing session.
func (transmuxer *Transmuxer) Run() {
	if transmuxer.closed {
		return
	}

	if transmuxer.running {
		return
	}

	transmuxer.running = true

	for {
		var sample float64

		for _, streamer := range transmuxer.Streamers {
			newSample, err := streamer.ReadSample()
			if err != nil {
				streamer.setError(err)
				streamer.Close()
				continue
			}

			sample += newSample * streamer.Volume
		}

		sample = sample * transmuxer.MasterVolume

		if transmuxer.FinalStream != nil {
			err := transmuxer.FinalStream.WriteSample(sample)
			if err != nil {
				transmuxer.setError(err)
				transmuxer.Close()
				return
			}
		}

		if transmuxer.buffer != nil {
			transmuxer.buffer = append(transmuxer.buffer, sample)
		}
	}
}

// Read implements io.Reader using the internal buffer.
func (transmuxer *Transmuxer) Read(p []byte) (n int, err error) {
	if transmuxer.closed {
		return 0, errors.New("ffgoconv: transmuxer: closed")
	}

	if len(transmuxer.buffer) == 0 {
		return 0, io.EOF
	}

	sample := transmuxer.buffer[0]

	if len(transmuxer.buffer) > 1 {
		transmuxer.buffer = transmuxer.buffer[1:]
	} else {
		transmuxer.buffer = make([]float64, 0)
	}

	u64 := math.Float64bits(sample)
	binary.LittleEndian.PutUint64(p, u64)

	return 8, nil
}

// Err returns the latest transmuxing error.
func (transmuxer *Transmuxer) Err() error {
	return transmuxer.Error
}

// Close closes the transmuxing session and renders the transmuxer unusable.
func (transmuxer *Transmuxer) Close() {
	if transmuxer.closed {
		return
	}

	for _, streamer := range transmuxer.Streamers {
		streamer.Close()
	}

	transmuxer.FinalStream.Close()

	transmuxer.closed = true
	transmuxer.running = false
}

func (transmuxer *Transmuxer) setError(err error) {
	transmuxer.Error = err
}
