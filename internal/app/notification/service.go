// Package notification delivers scheduled-scan events to chat tools and
// webhooks that the workspace configures.
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/loomx-ai/steward/internal/app/scheduling"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/workspace"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

const channelsKey = "notification_channels"

type ChannelType string

const (
	ChannelWebhook  ChannelType = "webhook"
	ChannelSlack    ChannelType = "slack"
	ChannelFeishu   ChannelType = "feishu"
	ChannelDingTalk ChannelType = "dingtalk"
	ChannelWeCom    ChannelType = "wecom"
)

var channelTypes = []ChannelType{ChannelWebhook, ChannelSlack, ChannelFeishu, ChannelDingTalk, ChannelWeCom}

var AllEvents = []scheduling.EventKind{scheduling.EventScanFailed, scheduling.EventScanPartial, scheduling.EventSchedulePaused}

type Delivery struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

// Channel is one place notifications go. The address and signing secret are
// stored encrypted and never returned; Target shows a masked address.
type Channel struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Type     ChannelType            `json:"type"`
	Enabled  bool                   `json:"enabled"`
	Events   []scheduling.EventKind `json:"events"`
	Language string                 `json:"language"`
	Target   string                 `json:"target"`
	// HasSigningSecret tells clients a secret is stored without revealing it.
	HasSigningSecret bool      `json:"has_signing_secret"`
	LastDelivery     *Delivery `json:"last_delivery,omitempty"`
	CreatedBy        string    `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	SealedURL        string    `json:"sealed_url"`
	SealedSigning    string    `json:"sealed_signing,omitempty"`
}

// View is a channel as clients see it.
type View struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Type             ChannelType            `json:"type"`
	Enabled          bool                   `json:"enabled"`
	Events           []scheduling.EventKind `json:"events"`
	Language         string                 `json:"language"`
	Target           string                 `json:"target"`
	HasSigningSecret bool                   `json:"has_signing_secret"`
	LastDelivery     *Delivery              `json:"last_delivery,omitempty"`
	CreatedBy        string                 `json:"created_by"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

func (c Channel) View() View {
	return View{
		ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled, Events: c.Events, Language: c.Language, Target: c.Target,
		HasSigningSecret: c.HasSigningSecret, LastDelivery: c.LastDelivery, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type Input struct {
	Name    string                 `json:"name"`
	Type    ChannelType            `json:"type"`
	Enabled *bool                  `json:"enabled,omitempty"`
	Events  []scheduling.EventKind `json:"events"`
	// Language is "zh" or "en" for the message text.
	Language string `json:"language"`
	// URL is required when creating; an empty URL on update keeps the stored one.
	URL string `json:"url,omitempty"`
	// SigningSecret is for Feishu and DingTalk robots with signature checks.
	// Empty keeps the stored secret; ClearSigningSecret removes it.
	SigningSecret      string `json:"signing_secret,omitempty"`
	ClearSigningSecret bool   `json:"clear_signing_secret,omitempty"`
}

type Sealer interface {
	SealSecret(ctx context.Context, purpose, plaintext string) (string, error)
	OpenSecret(ctx context.Context, purpose, sealed string) (string, error)
}

type Options struct {
	// AllowPrivateNetworks lets webhooks reach loopback and private
	// addresses. Steward Cloud turns it off so a workspace cannot reach the
	// hosting network.
	AllowPrivateNetworks bool
	// PublicURL is where people open Steward; messages link to it when set.
	PublicURL string
	Clock     func() time.Time
	// RetryDelays are the waits before each delivery attempt.
	RetryDelays []time.Duration
}

type Service struct {
	repositories persistence.Repositories
	sealer       Sealer
	client       *http.Client
	publicURL    string
	clock        func() time.Time
	retryDelays  []time.Duration
	pending      sync.WaitGroup
}

func NewService(repositories persistence.Repositories, sealer Sealer, options Options) (*Service, error) {
	if repositories == nil || sealer == nil {
		return nil, fmt.Errorf("notifications require repositories and a secret sealer")
	}
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	retryDelays := options.RetryDelays
	if len(retryDelays) == 0 {
		retryDelays = []time.Duration{0, 2 * time.Second, 8 * time.Second}
	}
	return &Service{
		repositories: repositories, sealer: sealer, client: newClient(options.AllowPrivateNetworks),
		publicURL: strings.TrimRight(strings.TrimSpace(options.PublicURL), "/"), clock: clock, retryDelays: retryDelays,
	}, nil
}

var ErrBlockedAddress = errors.New("the webhook address points to a private or local network")

func newClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		// Checked on the resolved address of every connection, including
		// redirects, so DNS cannot point a public name at an internal host.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
				ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
				return ErrBlockedAddress
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
		},
	}
}

type InvalidError struct {
	Code    string
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

func invalid(code, message string) error { return &InvalidError{Code: code, Message: message} }

func (s *Service) load(ctx context.Context) ([]Channel, error) {
	return loadChannels(ctx, s.repositories)
}

func loadChannels(ctx context.Context, repositories persistence.Repositories) ([]Channel, error) {
	payload, err := repositories.Schedules().GetSetting(ctx, channelsKey)
	if errors.Is(err, persistence.ErrNotFound) {
		return []Channel{}, nil
	}
	if err != nil {
		return nil, err
	}
	var channels []Channel
	if err := json.Unmarshal([]byte(payload), &channels); err != nil {
		return nil, err
	}
	return channels, nil
}

func (s *Service) save(ctx context.Context, repositories persistence.Repositories, channels []Channel) error {
	payload, err := json.Marshal(channels)
	if err != nil {
		return err
	}
	return repositories.Schedules().PutSetting(ctx, channelsKey, string(payload), s.clock())
}

func (s *Service) List(ctx context.Context) ([]View, error) {
	channels, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(channels))
	for _, channel := range channels {
		views = append(views, channel.View())
	}
	return views, nil
}

func validate(input Input, creating bool) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]rune(input.Name)) > 100 {
		return Input{}, invalid("notification.name_invalid", "give the channel a name of up to 100 characters")
	}
	if !slices.Contains(channelTypes, input.Type) {
		return Input{}, invalid("notification.type_invalid", "type must be webhook, slack, feishu, dingtalk or wecom")
	}
	if input.Language != "zh" && input.Language != "en" {
		input.Language = "en"
	}
	if len(input.Events) == 0 {
		return Input{}, invalid("notification.events_required", "pick at least one event")
	}
	for _, event := range input.Events {
		if !slices.Contains(AllEvents, event) {
			return Input{}, invalid("notification.events_invalid", fmt.Sprintf("unknown event %q", event))
		}
	}
	input.URL = strings.TrimSpace(input.URL)
	if creating && input.URL == "" {
		return Input{}, invalid("notification.url_required", "enter the webhook address")
	}
	if input.URL != "" {
		parsed, err := url.Parse(input.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return Input{}, invalid("notification.url_invalid", "the webhook address must be an http or https URL")
		}
	}
	return input, nil
}

func maskURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	tail := strings.TrimRight(parsed.Path, "/")
	if index := strings.LastIndex(tail, "/"); index >= 0 {
		tail = tail[index+1:]
	}
	if query := parsed.Query().Get("access_token") + parsed.Query().Get("key"); query != "" {
		tail = query
	}
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	if tail == "" {
		return parsed.Scheme + "://" + parsed.Host
	}
	return parsed.Scheme + "://" + parsed.Host + "/…" + tail
}

func (s *Service) Create(ctx context.Context, input Input, actor string) (View, error) {
	input, err := validate(input, true)
	if err != nil {
		return View{}, err
	}
	now := s.clock()
	channel := Channel{
		ID: idgen.MustNew("ntf"), Name: input.Name, Type: input.Type, Enabled: input.Enabled == nil || *input.Enabled,
		Events: input.Events, Language: input.Language, CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.applySecrets(ctx, &channel, input); err != nil {
		return View{}, err
	}
	err = s.mutate(ctx, actor, "notification.channel.create", channel.ID, func(channels []Channel) ([]Channel, error) {
		return append(channels, channel), nil
	})
	return channel.View(), err
}

func (s *Service) applySecrets(ctx context.Context, channel *Channel, input Input) error {
	if input.URL != "" {
		sealed, err := s.sealer.SealSecret(ctx, "notification-url:"+channel.ID, input.URL)
		if err != nil {
			return err
		}
		channel.SealedURL = sealed
		channel.Target = maskURL(input.URL)
	}
	switch {
	case input.ClearSigningSecret:
		channel.SealedSigning = ""
	case strings.TrimSpace(input.SigningSecret) != "":
		sealed, err := s.sealer.SealSecret(ctx, "notification-signing:"+channel.ID, strings.TrimSpace(input.SigningSecret))
		if err != nil {
			return err
		}
		channel.SealedSigning = sealed
	}
	channel.HasSigningSecret = channel.SealedSigning != ""
	return nil
}

func (s *Service) Update(ctx context.Context, id string, input Input, actor string) (View, error) {
	input, err := validate(input, false)
	if err != nil {
		return View{}, err
	}
	var updated Channel
	err = s.mutate(ctx, actor, "notification.channel.update", id, func(channels []Channel) ([]Channel, error) {
		index := slices.IndexFunc(channels, func(channel Channel) bool { return channel.ID == id })
		if index < 0 {
			return nil, persistence.ErrNotFound
		}
		channel := channels[index]
		channel.Name, channel.Type, channel.Events, channel.Language = input.Name, input.Type, input.Events, input.Language
		if input.Enabled != nil {
			channel.Enabled = *input.Enabled
		}
		if err := s.applySecrets(ctx, &channel, input); err != nil {
			return nil, err
		}
		channel.UpdatedAt = s.clock()
		channels[index] = channel
		updated = channel
		return channels, nil
	})
	return updated.View(), err
}

func (s *Service) Delete(ctx context.Context, id, actor string) error {
	return s.mutate(ctx, actor, "notification.channel.delete", id, func(channels []Channel) ([]Channel, error) {
		index := slices.IndexFunc(channels, func(channel Channel) bool { return channel.ID == id })
		if index < 0 {
			return nil, persistence.ErrNotFound
		}
		return slices.Delete(channels, index, index+1), nil
	})
}

func (s *Service) mutate(ctx context.Context, actor, action, id string, change func([]Channel) ([]Channel, error)) error {
	// The channels share one settings value; the lock keeps read-modify-write
	// from losing another server's change.
	tenant, _ := workspace.From(ctx)
	return s.repositories.WithLock(ctx, "notification-channels:"+string(tenant), func(ctx context.Context) error {
		return s.mutateLocked(ctx, actor, action, id, change)
	})
}

func (s *Service) mutateLocked(ctx context.Context, actor, action, id string, change func([]Channel) ([]Channel, error)) error {
	return s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		channels, err := loadChannels(ctx, repositories)
		if err != nil {
			return err
		}
		channels, err = change(channels)
		if err != nil {
			return err
		}
		if err := s.save(ctx, repositories, channels); err != nil {
			return err
		}
		if actor == "" {
			return nil
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), Actor: actor, Action: action,
			TargetType: "notification_channel", TargetID: id, Result: "ok", CreatedAt: s.clock(),
		})
	})
}

// Test sends a test message and records the result on the channel.
func (s *Service) Test(ctx context.Context, id string) (View, error) {
	channels, err := s.load(ctx)
	if err != nil {
		return View{}, err
	}
	index := slices.IndexFunc(channels, func(channel Channel) bool { return channel.ID == id })
	if index < 0 {
		return View{}, persistence.ErrNotFound
	}
	channel := channels[index]
	delivery := s.send(ctx, channel, testMessage(channel))
	channel.LastDelivery = &delivery
	if err := s.recordDelivery(ctx, id, delivery); err != nil {
		return View{}, err
	}
	return channel.View(), nil
}

// Notify implements scheduling.Notifier. Delivery runs in the background so a
// slow chat service never holds up the scheduler.
func (s *Service) Notify(ctx context.Context, event scheduling.Event) {
	channels, err := s.load(ctx)
	if err != nil {
		slog.Error("notification channels could not be loaded", "error", err)
		return
	}
	for _, channel := range channels {
		if !channel.Enabled || !slices.Contains(channel.Events, event.Kind) {
			continue
		}
		s.pending.Add(1)
		go func() {
			defer s.pending.Done()
			// Detached from the scheduler tick, but still in its workspace.
			deliverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			message := eventMessage(channel, event, s.publicURL)
			var delivery Delivery
			for _, wait := range s.retryDelays {
				time.Sleep(wait)
				if delivery = s.send(deliverCtx, channel, message); delivery.OK {
					break
				}
			}
			if !delivery.OK {
				slog.Warn("notification could not be delivered", "channel_id", channel.ID, "error", delivery.Error)
			}
			if err := s.recordDelivery(deliverCtx, channel.ID, delivery); err != nil {
				slog.Error("notification delivery could not be recorded", "channel_id", channel.ID, "error", err)
			}
		}()
	}
}

// Wait blocks until background deliveries finish. Tests use it.
func (s *Service) Wait() { s.pending.Wait() }

func (s *Service) recordDelivery(ctx context.Context, id string, delivery Delivery) error {
	err := s.mutate(ctx, "", "", id, func(channels []Channel) ([]Channel, error) {
		index := slices.IndexFunc(channels, func(channel Channel) bool { return channel.ID == id })
		if index < 0 {
			return nil, persistence.ErrNotFound
		}
		channels[index].LastDelivery = &delivery
		return channels, nil
	})
	if errors.Is(err, persistence.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) send(ctx context.Context, channel Channel, message Message) Delivery {
	delivery := Delivery{At: s.clock()}
	target, err := s.sealer.OpenSecret(ctx, "notification-url:"+channel.ID, channel.SealedURL)
	if err != nil {
		delivery.Error = "the stored webhook address cannot be read; enter it again"
		return delivery
	}
	signing := ""
	if channel.SealedSigning != "" {
		if signing, err = s.sealer.OpenSecret(ctx, "notification-signing:"+channel.ID, channel.SealedSigning); err != nil {
			delivery.Error = "the stored signing secret cannot be read; enter it again"
			return delivery
		}
	}
	target, body, err := encodeRequest(channel.Type, target, signing, message, s.clock())
	if err != nil {
		delivery.Error = err.Error()
		return delivery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		delivery.Error = err.Error()
		return delivery
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Steward")
	response, err := s.client.Do(request)
	if err != nil {
		delivery.Error = describeTransportError(err)
		return delivery
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		delivery.Error = fmt.Sprintf("the webhook answered HTTP %d", response.StatusCode)
		return delivery
	}
	if err := checkResponse(channel.Type, payload); err != nil {
		delivery.Error = err.Error()
		return delivery
	}
	delivery.OK = true
	return delivery
}

func describeTransportError(err error) string {
	if errors.Is(err, ErrBlockedAddress) {
		return ErrBlockedAddress.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "the webhook did not answer in time"
		}
		return "the webhook could not be reached: " + urlErr.Err.Error()
	}
	return err.Error()
}
