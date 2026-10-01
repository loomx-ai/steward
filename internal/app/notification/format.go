package notification

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/scheduling"
)

// Message is the text sent to chat tools plus the structured event for plain
// webhooks.
type Message struct {
	Title string
	Lines []string
	Link  string
	Event *scheduling.Event
}

func (m Message) Text() string {
	parts := append([]string{m.Title}, m.Lines...)
	if m.Link != "" {
		parts = append(parts, m.Link)
	}
	return strings.Join(parts, "\n")
}

type phrases struct {
	failed, partial, paused, test, schedule, connection, reason, pausedHint, testBody, defaultName string
}

var language = map[string]phrases{
	"zh": {
		failed: "Steward 定时扫描失败", partial: "Steward 定时扫描部分失败", paused: "Steward 定时扫描已暂停", test: "Steward 测试通知",
		schedule: "计划", connection: "云连接", reason: "原因",
		pausedHint: "连续 %d 次扫描失败。替换凭证并通过验证后会自动恢复。", testBody: "通知渠道「%s」已连通。", defaultName: "每日全量",
	},
	"en": {
		failed: "Steward scheduled scan failed", partial: "Steward scheduled scan partly failed", paused: "Steward scheduled scan paused", test: "Steward test notification",
		schedule: "Schedule", connection: "Connection", reason: "Reason",
		pausedHint: "%d scans in a row failed. It resumes on its own once the connection's credential is replaced and validated.", testBody: "The channel \"%s\" is connected.", defaultName: "Daily full scan",
	},
}

func phrasesFor(code string) phrases {
	if value, ok := language[code]; ok {
		return value
	}
	return language["en"]
}

func eventMessage(channel Channel, event scheduling.Event, publicURL string) Message {
	words := phrasesFor(channel.Language)
	name := event.Schedule.Name
	if name == "" {
		name = words.defaultName
	}
	message := Message{Event: &event}
	switch event.Kind {
	case scheduling.EventScanFailed:
		message.Title = words.failed
	case scheduling.EventScanPartial:
		message.Title = words.partial
	default:
		message.Title = words.paused
	}
	message.Lines = []string{
		words.schedule + ": " + name,
		words.connection + ": " + event.Connection.Name,
	}
	if event.Detail != "" {
		message.Lines = append(message.Lines, words.reason+": "+event.Detail)
	}
	if event.Kind == scheduling.EventSchedulePaused {
		message.Lines = append(message.Lines, fmt.Sprintf(words.pausedHint, event.Schedule.ConsecutiveFailures))
	}
	if publicURL != "" {
		message.Link = publicURL + "/scans"
		if event.ScanTaskID != "" {
			message.Link += "/" + string(event.ScanTaskID)
		}
	}
	return message
}

func testMessage(channel Channel) Message {
	words := phrasesFor(channel.Language)
	return Message{Title: words.test, Lines: []string{fmt.Sprintf(words.testBody, channel.Name)}}
}

type webhookPayload struct {
	Event      string    `json:"event"`
	Text       string    `json:"text"`
	OccurredAt time.Time `json:"occurred_at"`
	Schedule   *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"schedule,omitempty"`
	Connection *struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	} `json:"connection,omitempty"`
	ScanTaskID string `json:"scan_task_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Link       string `json:"link,omitempty"`
}

// encodeRequest builds the request body each service expects and signs it
// when the robot requires a signature.
func encodeRequest(channelType ChannelType, target, signing string, message Message, now time.Time) (string, []byte, error) {
	text := message.Text()
	var body any
	switch channelType {
	case ChannelSlack:
		body = map[string]any{"text": text}
	case ChannelFeishu:
		value := map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		if signing != "" {
			timestamp := strconv.FormatInt(now.Unix(), 10)
			mac := hmac.New(sha256.New, []byte(timestamp+"\n"+signing))
			value["timestamp"] = timestamp
			value["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		body = value
	case ChannelDingTalk:
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
		if signing != "" {
			timestamp := strconv.FormatInt(now.UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(signing))
			mac.Write([]byte(timestamp + "\n" + signing))
			parsed, err := url.Parse(target)
			if err != nil {
				return "", nil, err
			}
			query := parsed.Query()
			query.Set("timestamp", timestamp)
			query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			parsed.RawQuery = query.Encode()
			target = parsed.String()
		}
	case ChannelWeCom:
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	default:
		payload := webhookPayload{Event: "test", Text: text, OccurredAt: now, Link: message.Link}
		if event := message.Event; event != nil {
			payload.Event = string(event.Kind)
			payload.OccurredAt = event.OccurredAt
			payload.ScanTaskID = string(event.ScanTaskID)
			payload.Detail = event.Detail
			payload.Schedule = &struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{string(event.Schedule.ID), event.Schedule.Name}
			payload.Connection = &struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Provider string `json:"provider"`
			}{string(event.Connection.ID), event.Connection.Name, string(event.Connection.Provider)}
		}
		body = payload
	}
	encoded, err := json.Marshal(body)
	return target, encoded, err
}

// checkResponse reads the application-level result that chat robots return
// with HTTP 200, such as a wrong keyword or signature.
func checkResponse(channelType ChannelType, payload []byte) error {
	var result struct {
		Code       *int   `json:"code"`
		StatusCode *int   `json:"StatusCode"`
		Msg        string `json:"msg"`
		ErrCode    *int   `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
	}
	switch channelType {
	case ChannelFeishu:
		if json.Unmarshal(payload, &result) != nil {
			return nil
		}
		for _, code := range []*int{result.Code, result.StatusCode} {
			if code != nil && *code != 0 {
				return errors.New("Feishu rejected the message: " + result.Msg)
			}
		}
	case ChannelDingTalk, ChannelWeCom:
		if json.Unmarshal(payload, &result) != nil {
			return nil
		}
		if result.ErrCode != nil && *result.ErrCode != 0 {
			return errors.New("the robot rejected the message: " + result.ErrMsg)
		}
	}
	return nil
}
