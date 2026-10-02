package probe

import "testing"

const sample = `{
 "streams":[
  {"index":0,"codec_name":"h264","codec_type":"video","codec_tag_string":"avc1","profile":"High","width":1920,"height":1080,
   "display_aspect_ratio":"16:9","avg_frame_rate":"24000/1001","bit_rate":"5000000","pix_fmt":"yuv420p"},
  {"index":1,"codec_name":"ac3","codec_type":"audio","channels":6,"channel_layout":"5.1(side)","sample_rate":"48000","bit_rate":"640000","tags":{"language":"eng"}},
  {"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"fre"}}
 ],
 "format":{"filename":"/m/a.mkv","nb_streams":3,"format_long_name":"Matroska","duration":"3600.5"}}`

func TestParse(t *testing.T) {
	in, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Streams) != 3 || in.Format.NumStreams != 3 {
		t.Fatalf("bad streams: %+v", in)
	}
	v, a, s := in.Streams[0], in.Streams[1], in.Streams[2]
	if v.Resolution() != "1920x1080" || v.Bitrate() != 5000000 {
		t.Fatalf("video: %+v", v)
	}
	if fr := v.FrameRate(); fr < 23.97 || fr > 23.98 {
		t.Fatalf("frame rate %v", fr)
	}
	if a.Channels != 6 || a.Language() != "eng" || s.Language() != "fre" {
		t.Fatalf("audio/sub: %+v %+v", a, s)
	}
}

func TestFrameRateEdgeCases(t *testing.T) {
	for in, want := range map[string]float64{"25": 25, "0/0": 0, "": 0, "x/2": 0, "30/1": 30} {
		if got := (Stream{AvgFrameRate: in}).FrameRate(); got != want {
			t.Errorf("FrameRate(%q)=%v want %v", in, got, want)
		}
	}
}

func TestAspectString(t *testing.T) {
	for in, want := range map[string]string{
		"16:9":      "16:9",
		"1920:1080": "1.78:1",
		"1080:1920": "1:1.78",
		"4:3":       "4:3",
		"":          "",
		"N/A":       "",
	} {
		if got := AspectString(in); got != want {
			t.Errorf("AspectString(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTracks(t *testing.T) {
	in, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	tr := in.Tracks()
	if len(tr) != 3 {
		t.Fatalf("tracks: %+v", tr)
	}
	if v := tr[0]; v.Kind != "video" || v.Codec != "h264" || v.AspectRatio != "16:9" || v.Resolution != "1920x1080" {
		t.Fatalf("video track: %+v", v)
	}
	if a := tr[1]; a.Kind != "audio" || a.Channels != 6 || a.Language != "eng" || a.BitRate != 640000 {
		t.Fatalf("audio track: %+v", a)
	}
	if s := tr[2]; s.Kind != "subtitle" || s.Language != "fre" {
		t.Fatalf("subtitle track: %+v", s)
	}
}
