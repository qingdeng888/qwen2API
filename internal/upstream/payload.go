package upstream

import (
	"crypto/rand"
	"fmt"
	"time"
)

type StreamOptions struct {
	HasCustomTools  bool
	Files           []map[string]interface{}
	ChatType        string
	ImageOptions    map[string]interface{}
	ThinkingEnabled *bool
	EnableSearch    bool
}

func BuildChatPayload(chatID, model, content string, opts StreamOptions) map[string]interface{} {
	ts := time.Now().Unix()
	chatType := opts.ChatType
	if chatType == "" {
		chatType = "t2t"
	}

	var featureConfig map[string]interface{}
	var msgChatType string
	var extraMeta map[string]interface{}

	isImage := chatType == "image_gen" || chatType == "t2i"
	isVideo := chatType == "t2v"

	if isImage {
		featureConfig = imgFeatureConfig(opts.ImageOptions)
		msgChatType = "t2t"
		extraMeta = map[string]interface{}{"subChatType": "t2i", "mode": "image_generation", "aspectRatio": imgRatio(opts.ImageOptions)}
	} else if isVideo {
		featureConfig = vidFeatureConfig(opts.ImageOptions)
		msgChatType = "t2v"
		extraMeta = map[string]interface{}{"subChatType": "t2v", "mode": "video_generation", "aspectRatio": imgRatio(opts.ImageOptions)}
	} else {
		featureConfig = map[string]interface{}{
			"thinking_enabled": true, "output_schema": "phase", "research_mode": "normal",
			"auto_thinking": true, "thinking_mode": "Auto", "thinking_format": "summary",
			"auto_search": opts.EnableSearch || chatType == "deep_research",
			"code_interpreter": false, "plugins_enabled": false,
			"function_calling": false, "enable_tools": false, "tool_choice": "none",
		}
		if opts.HasCustomTools {
			featureConfig["thinking_enabled"] = false
			featureConfig["auto_thinking"] = false
			featureConfig["thinking_mode"] = "Disabled"
		}
		if opts.ThinkingEnabled != nil {
			en := *opts.ThinkingEnabled
			featureConfig["thinking_enabled"] = en
			featureConfig["auto_thinking"] = en
			if en {
				featureConfig["thinking_mode"] = "Auto"
			} else {
				featureConfig["thinking_mode"] = "Disabled"
			}
		}
		msgChatType = chatType
		extraMeta = map[string]interface{}{"subChatType": chatType}
	}

	files := opts.Files
	if files == nil {
		files = []map[string]interface{}{}
	}

	payload := map[string]interface{}{
		"stream": true, "version": "2.1", "incremental_output": true,
		"chat_id": chatID, "chat_mode": "normal", "model": model, "parent_id": nil,
		"messages": []map[string]interface{}{{
			"fid": newUUID(), "parentId": nil, "childrenIds": []string{newUUID()},
			"role": "user", "content": content, "user_action": "chat",
			"files": files, "timestamp": ts, "models": []string{model},
			"chat_type": msgChatType, "feature_config": featureConfig,
			"extra": map[string]interface{}{"meta": extraMeta},
			"sub_chat_type": chatType, "parent_id": nil,
		}},
		"timestamp": ts,
	}
	if isImage || isVideo {
		payload["size"] = imgRatio(opts.ImageOptions)
	}
	return payload
}

func NormalizeChatType(ct string) string {
	if ct == "image_gen" || ct == "t2i" {
		return "t2i"
	}
	return ct
}

func NormalizeImageRatioFromSize(size string) string {
	switch size {
	case "1024x1024", "512x512":
		return "1:1"
	case "1792x1024", "1024x576":
		return "16:9"
	case "1024x1792", "576x1024":
		return "9:16"
	default:
		return "1:1"
	}
}

func imgRatio(opts map[string]interface{}) string {
	if opts != nil {
		for _, k := range []string{"ratio", "aspect_ratio", "aspectRatio"} {
			if v, ok := opts[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return "1:1"
}

func imgFeatureConfig(opts map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"thinking_enabled": false, "output_schema": "phase", "auto_thinking": false,
		"thinking_mode": "off", "auto_search": false, "code_interpreter": false,
		"function_calling": false, "plugins_enabled": true, "image_generation": true,
		"default_aspect_ratio": imgRatio(opts),
	}
}

func vidFeatureConfig(opts map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"thinking_enabled": false, "output_schema": "phase", "auto_thinking": false,
		"thinking_mode": "off", "auto_search": false, "code_interpreter": false,
		"function_calling": false, "plugins_enabled": true, "video_generation": true,
		"default_aspect_ratio": imgRatio(opts),
	}
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
