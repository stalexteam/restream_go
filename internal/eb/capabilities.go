package eb

// capabilitiesJSON/clientJSON — синтетичний звіт про кодуючу машину; Twitch
// звіряє лише gpu[].device_id. Порядок полів 1:1 з.
var capabilitiesJSON = jObj{
	{"cpu", jObj{
		{"logical_cores", 16},
		{"name", "Generic CPU"},
		{"physical_cores", 8},
		{"speed", 3600},
	}},
	{"gaming_features", jObj{
		{"game_bar_enabled", nil},
		{"game_dvr_allowed", nil},
		{"game_dvr_bg_recording", false},
		{"game_dvr_enabled", false},
		{"game_mode_enabled", nil},
		{"hags_enabled", false},
	}},
	{"gpu", jArr{
		jObj{
			{"dedicated_video_memory", 16753098752},
			{"device_id", 11266},
			{"driver_version", "32.0.15.9649"},
			{"model", "Generic Video Adapter"},
			{"shared_system_memory", 8589934592},
			{"vendor_id", 4318},
		},
	}},
	{"memory", jObj{
		{"free", 8589934592},
		{"total", 17179869184},
	}},
	{"system", jObj{
		{"arm", false},
		{"armEmulation", false},
		{"bits", 64},
		{"build", 26200},
		{"name", "Windows"},
		{"release", "25H2"},
		{"revision", "8875"},
		{"version", "10.0"},
	}},
}

var clientJSON = jObj{
	{"name", "obs-studio"},
	{"supported_codecs", jArr{"h264"}},
	{"version", "32.2.1"},
}
