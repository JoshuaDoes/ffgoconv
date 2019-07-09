package ffgoconv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/superwhiskers/crunch"

	"github.com/google/uuid"
)

type MuxSession struct {
	sync.Mutex

	muxTranscoders []*MuxTranscoder  //An array of the transcode sessions to mux
	finalSession   *TranscodeSession //The finalized transcode session where muxed PCM audio will be transcoded to the desired format
	finalBuffer    *CrunchMiniBuffer //The finalized transcode session's unread buffer
	options        *TranscodeOptions //The options to use for the output transcoding session

	err error //If non-nil, the error that stopped the muxing process
}

type MuxTranscoder struct {
	TranscodeSession *TranscodeSession //The transcode session where PCM audio data will be pulled from
	Identifier       uuid.UUID         //A unique identifier for transcoder manipulation
	Volume           float32           //Volume percentage from 0.0 to 1.0
	Callback         func()            //A function to call once the transcoder finishes
}

func NewMuxer(options *TranscodeOptions) *MuxSession {
	finalBuffer := &crunch.MiniBuffer{}
	crunch.NewMiniBuffer(&finalBuffer, nil)

	muxer := &MuxSession{
		muxTranscoders: make([]*MuxTranscoder, 0),
		options:        options,
		finalBuffer:    &CrunchMiniBuffer{finalBuffer},
	}

	return muxer
}

func (muxer *MuxSession) AddSourceFile(path string, callback func()) (uuid.UUID, error) {
	transcoder, err := TranscodeFile(path, RawTranscodeOptions)
	if err != nil {
		return [16]byte{}, err
	}

	identifier, err := uuid.NewRandom()
	if err != nil {
		return [16]byte{}, err
	}

	muxTranscoder := &MuxTranscoder{
		TranscodeSession: transcoder,
		Identifier:       identifier,
		Volume:           1,
		Callback:         callback,
	}

	muxer.Lock()
	muxer.muxTranscoders = append(muxer.muxTranscoders, muxTranscoder)
	muxer.Unlock()

	return identifier, nil
}

func (muxer *MuxSession) AddSourceMem(r io.Reader, callback func()) (uuid.UUID, error) {
	transcoder, err := TranscodeMem(r, RawTranscodeOptions)
	if err != nil {
		return [16]byte{}, err
	}

	identifier, err := uuid.NewRandom()
	if err != nil {
		return [16]byte{}, err
	}

	muxTranscoder := &MuxTranscoder{
		TranscodeSession: transcoder,
		Identifier:       identifier,
		Volume:           1,
		Callback:         callback,
	}

	muxer.Lock()
	muxer.muxTranscoders = append(muxer.muxTranscoders, muxTranscoder)
	muxer.Unlock()

	return identifier, nil
}

func (muxer *MuxSession) AddSource(data interface{}, callback func()) (uuid.UUID, error) {
	switch data.(type) {
	case string:
		return muxer.AddSourceFile(data.(string), callback)
	case io.Reader:
		return muxer.AddSourceMem(data.(io.Reader), callback)
	}

	return [16]byte{}, errors.New("invalid source type")
}

func (muxer *MuxSession) SetVolume(identifier uuid.UUID, volume float32) error {
	muxer.Lock()
	defer muxer.Unlock()

	for _, muxTranscoder := range muxer.muxTranscoders {
		if muxTranscoder.Identifier == identifier {
			muxTranscoder.Volume = volume
			return nil
		}
	}

	return errors.New("invalid identifier")
}

func (muxer *MuxSession) SetCallback(identifier uuid.UUID, callback func()) error {
	muxer.Lock()
	defer muxer.Unlock()

	for _, muxTranscoder := range muxer.muxTranscoders {
		if muxTranscoder.Identifier == identifier {
			muxTranscoder.Callback = callback
			return nil
		}
	}

	return errors.New("invalid identifier")
}

func (muxer *MuxSession) RemoveSource(identifier uuid.UUID) error {
	muxer.Lock()
	defer muxer.Unlock()

	for i := 0; i < len(muxer.muxTranscoders); i++ {
		if muxer.muxTranscoders[i].Identifier == identifier {
			muxer.muxTranscoders = append(muxer.muxTranscoders[:i], muxer.muxTranscoders[i+1:]...)
			return nil
		}
	}

	return errors.New("invalid identifier")
}

func (muxer *MuxSession) Start() (err error) {
	go func() {
		for len(muxer.muxTranscoders) > 0 {
			muxer.Lock() //Lock to prevent unexpected sources from being added mid-muxing

			samples := make([]int16, 0)

			for _, muxTranscoder := range muxer.muxTranscoders {
				if muxTranscoder.TranscodeSession.Error() != nil {
					muxer.err = muxTranscoder.TranscodeSession.Error()
					muxer.RemoveSource(muxTranscoder.Identifier)
					fmt.Println(err)
				}

				sample := make([]byte, 1)

				n, err := muxTranscoder.TranscodeSession.Read(sample)
				if err == io.EOF {
					if muxTranscoder.Callback != nil {
						go muxTranscoder.Callback()
					}

					muxer.RemoveSource(muxTranscoder.Identifier)

					continue
				}
				if n != 1 {
					continue
				}

				sampleInt16 := int16(binary.LittleEndian.Uint16(sample))
				samples = append(samples, sampleInt16)
			}

			muxer.Unlock() //Unlock so new sources can be added

			if len(samples) == 0 {
				continue
			}

			var muxedSampleLarge int64
			var muxedSampleSmall int16

			for _, sample := range samples {
				fmt.Println("1")
				muxedSampleLarge += int64(sample)
			}

			fmt.Println("2")
			muxedSampleSmall = int16(muxedSampleLarge / int64(len(samples)))

			fmt.Println("3")
			muxer.finalBuffer.Grow(2)
			fmt.Println("4")
			muxer.finalBuffer.WriteU16LENext([]uint16{uint16(muxedSampleSmall)})

			bytes := make([]byte, 0)
			muxer.finalBuffer.Bytes(&bytes)
			fmt.Println(bytes)
		}

		muxer.Cleanup() //Perform cleanup in case package user forgets
	}()

	muxer.finalSession, err = TranscodeMem(muxer.finalBuffer, muxer.options)
	if err != nil {
		return err
	}

	return nil
}

func (muxer *MuxSession) Read(p []byte) (n int, err error) {
	if muxer.finalBuffer.Len() >= len(p) {
		return muxer.finalBuffer.Read(p)
	}

	for muxer.finalBuffer.Len() < len(p) {
		f, err := muxer.finalSession.ReadFrame()
		if err != nil {
			break
		}
		muxer.finalBuffer.Write(f)
	}

	return muxer.finalBuffer.Read(p)
}

func (muxer *MuxSession) Cleanup() {
	muxer.Lock()

	muxer.finalSession.Cleanup()

	for _, transcoder := range muxer.muxTranscoders {
		transcoder.TranscodeSession.Cleanup()
	}

	muxer.muxTranscoders = make([]*MuxTranscoder, 0)

	muxer.Unlock()
}

func (muxer *MuxSession) Error() error {
	muxer.Lock()
	defer muxer.Unlock()
	return muxer.err
}
