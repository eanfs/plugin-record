package record

import (
	"net"
	"time"

	"github.com/yapingcat/gomedia/go-mp4"
	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/util"
)

type MP4Recorder struct {
	Recorder
	*mp4.Movmuxer `json:"-" yaml:"-"`
	videoId       uint32
	audioId       uint32
	duration      uint32
}

func (r *MP4Recorder) SetId(string) {
	//TODO implement me
	panic("implement me")
}

func (r *MP4Recorder) GetRecordModeString(mode RecordMode) string {
	//TODO implement me
	panic("implement me")
}

func (r *MP4Recorder) StartWithDynamicTimeout(streamPath, fileName string, timeout time.Duration) error {
	//TODO implement me
	panic("implement me")
}

func (r *MP4Recorder) UpdateTimeout(timeout time.Duration) {
	//TODO implement me
	panic("implement me")
}

func NewMP4Recorder() *MP4Recorder {
	r := &MP4Recorder{}
	r.Record = RecordPluginConfig.Mp4
	r.Storage = RecordPluginConfig.Storage
	return r
}

func (r *MP4Recorder) Start(streamPath string) (err error) {
	r.ID = streamPath + "/mp4"
	return r.start(r, streamPath, SUBTYPE_RAW)
}

func (r *MP4Recorder) StartWithFileName(streamPath string, fileName string) error {
	r.ID = streamPath + "/mp4/" + fileName
	return r.start(r, streamPath, SUBTYPE_RAW)
}

func (r *MP4Recorder) Close() (err error) {
	if r.File != nil {
		// WriteTrailer 失败说明文件不完整，不上传损坏文件
		if err = r.Movmuxer.WriteTrailer(); err != nil {
			r.Error("mp4 write trailer", zap.Error(err))
			r.File.Close()
			return
		}
		r.Info("mp4 write trailer success")
		if err = r.File.Close(); err != nil {
			r.Error("mp4 File Close", zap.Error(err))
			return
		}
		r.Info("mp4 File Close success")
		go r.UploadFileWithTags(r.Path, r.filePath, r.duration)
	}
	return
}

func (r *MP4Recorder) setTracks() {
	if r.Audio != nil {
		switch r.Audio.CodecID {
		case codec.CodecID_AAC:
			r.audioId = r.AddAudioTrack(mp4.MP4_CODEC_AAC, mp4.WithExtraData(r.Audio.SequenceHead[2:]))
		case codec.CodecID_PCMA:
			r.audioId = r.AddAudioTrack(mp4.MP4_CODEC_G711A, mp4.WithAudioSampleRate(r.Audio.SampleRate), mp4.WithAudioChannelCount(r.Audio.Channels), mp4.WithAudioSampleBits(r.Audio.SampleSize))
		case codec.CodecID_PCMU:
			r.audioId = r.AddAudioTrack(mp4.MP4_CODEC_G711U, mp4.WithAudioSampleRate(r.Audio.SampleRate), mp4.WithAudioChannelCount(r.Audio.Channels), mp4.WithAudioSampleBits(r.Audio.SampleSize))
		}
	}
	if r.Video != nil {
		switch r.Video.CodecID {
		case codec.CodecID_H264:
			r.videoId = r.AddVideoTrack(mp4.MP4_CODEC_H264, mp4.WithExtraData(r.Video.SequenceHead[5:]))
		case codec.CodecID_H265:
			r.videoId = r.AddVideoTrack(mp4.MP4_CODEC_H265, mp4.WithExtraData(r.Video.SequenceHead[5:]))
		}
	}
}
func (r *MP4Recorder) OnEvent(event any) {
	var err error
	r.Recorder.OnEvent(event)
	switch v := event.(type) {
	case FileWr:
		r.Movmuxer, err = mp4.CreateMp4Muxer(v)
		if err != nil {
			r.Error("mp4 create muxer", zap.Error(err))
		} else {
			r.setTracks()
		}
	case AudioFrame:
		if v.AbsTime > r.duration {
			r.duration = v.AbsTime
		}
		if r.audioId != 0 {
			var audioData []byte
			if v.ADTS == nil {
				audioData = v.AUList.ToBytes()
			} else {
				audioData = util.ConcatBuffers(append(net.Buffers{v.ADTS.Value}, v.AUList.ToBuffers()...))
			}
			if err = r.Write(r.audioId, audioData, uint64(v.AbsTime+(v.PTS-v.DTS)/90), uint64(v.AbsTime)); err != nil {
				r.Stop(zap.Error(err))
			}
		}
	case VideoFrame:
		if v.AbsTime > r.duration {
			r.duration = v.AbsTime
		}
		if r.videoId != 0 {
			if err = r.Write(r.videoId, util.ConcatBuffers(v.GetAnnexB()), uint64(v.AbsTime+(v.PTS-v.DTS)/90), uint64(v.AbsTime)); err != nil {
				r.Stop(zap.Error(err))
			}
		}
	}
}
