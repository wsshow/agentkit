package agentkit

import "github.com/cloudwego/eino/schema"

// ContentPart 表示用户输入的一个内容片段（文本、图片、音频、视频、文件）。
// 通过 Text、ImageURL 等构造函数创建，用于 Agent.Send 多模态输入。
type ContentPart = schema.MessageInputPart

// ImageURLDetail 控制图片识别质量。
type ImageURLDetail = schema.ImageURLDetail

const (
	ImageDetailHigh ImageURLDetail = schema.ImageURLDetailHigh
	ImageDetailLow  ImageURLDetail = schema.ImageURLDetailLow
	ImageDetailAuto ImageURLDetail = schema.ImageURLDetailAuto
)

// Text 创建文本内容片段。
func Text(s string) ContentPart {
	return ContentPart{
		Type: schema.ChatMessagePartTypeText,
		Text: s,
	}
}

// ImageURL 创建图片 URL 内容片段。
// detail 可选，控制图片识别质量（默认由模型决定）。
func ImageURL(url string, detail ...ImageURLDetail) ContentPart {
	img := &schema.MessageInputImage{
		MessagePartCommon: schema.MessagePartCommon{URL: &url},
	}
	if len(detail) > 0 {
		img.Detail = detail[0]
	}
	return ContentPart{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: img,
	}
}

// ImageBase64 创建 Base64 编码图片内容片段。
func ImageBase64(data, mimeType string, detail ...ImageURLDetail) ContentPart {
	img := &schema.MessageInputImage{
		MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: mimeType},
	}
	if len(detail) > 0 {
		img.Detail = detail[0]
	}
	return ContentPart{
		Type:  schema.ChatMessagePartTypeImageURL,
		Image: img,
	}
}

// AudioURL 创建音频 URL 内容片段。
func AudioURL(url string) ContentPart {
	return ContentPart{
		Type:  schema.ChatMessagePartTypeAudioURL,
		Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
	}
}

// AudioBase64 创建 Base64 编码音频内容片段。
func AudioBase64(data, mimeType string) ContentPart {
	return ContentPart{
		Type:  schema.ChatMessagePartTypeAudioURL,
		Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: mimeType}},
	}
}

// VideoURL 创建视频 URL 内容片段。
func VideoURL(url string) ContentPart {
	return ContentPart{
		Type:  schema.ChatMessagePartTypeVideoURL,
		Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
	}
}

// VideoBase64 创建 Base64 编码视频内容片段。
func VideoBase64(data, mimeType string) ContentPart {
	return ContentPart{
		Type:  schema.ChatMessagePartTypeVideoURL,
		Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: mimeType}},
	}
}

// FileURL 创建文件 URL 内容片段。
func FileURL(url string) ContentPart {
	return ContentPart{
		Type: schema.ChatMessagePartTypeFileURL,
		File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
	}
}

// FileBase64 创建 Base64 编码文件内容片段。name 为文件名（可选）。
func FileBase64(data, mimeType string, name ...string) ContentPart {
	f := &schema.MessageInputFile{
		MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: mimeType},
	}
	if len(name) > 0 {
		f.Name = name[0]
	}
	return ContentPart{
		Type: schema.ChatMessagePartTypeFileURL,
		File: f,
	}
}
