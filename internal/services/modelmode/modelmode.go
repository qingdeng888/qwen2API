package modelmode

import "strings"

type ModelMode struct {
	BaseModel     string
	Mode          string
	ChatType      string
	ForceThinking bool
}

func Parse(name string) ModelMode {
	r := ModelMode{BaseModel: name, Mode: "chat", ChatType: "t2t"}
	suffixes := []struct {
		s string
		m string
		c string
		t bool
	}{
		{"-deep-research", "deep_research", "deep_research", false},
		{"-deep_research", "deep_research", "deep_research", false},
		{"-thinking", "thinking", "t2t", true},
		{"-think", "thinking", "t2t", true},
		{"-search", "search", "t2t", false},
		{"-image", "image", "image_gen", false},
		{"-img", "image", "image_gen", false},
		{"-video", "video", "t2v", false},
		{"-t2i", "image", "t2i", false},
		{"-t2v", "video", "t2v", false},
		{"-webdev", "web_dev", "webdev", false},
		{"-web-dev", "web_dev", "webdev", false},
		{"-slides", "slides", "slides", false},
	}
	low := strings.ToLower(name)
	for _, sf := range suffixes {
		if strings.HasSuffix(low, sf.s) {
			r.BaseModel = name[:len(name)-len(sf.s)]
			r.Mode = sf.m
			r.ChatType = sf.c
			r.ForceThinking = sf.t
			return r
		}
	}
	return r
}
