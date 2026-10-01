package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/notification"
	"github.com/loomx-ai/steward/internal/app/scheduling"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

type received struct {
	mu       sync.Mutex
	bodies   []map[string]any
	queries  []string
	response string
}

func (r *received) handler(w http.ResponseWriter, request *http.Request) {
	payload, _ := io.ReadAll(request.Body)
	var body map[string]any
	_ = json.Unmarshal(payload, &body)
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.queries = append(r.queries, request.URL.RawQuery)
	response := r.response
	r.mu.Unlock()
	_, _ = io.WriteString(w, response)
}

func newService(t *testing.T, allowPrivate bool) (*notification.Service, persistence.Repositories) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "steward.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	service, err := notification.NewService(repositories, vault, notification.Options{AllowPrivateNetworks: allowPrivate, PublicURL: "https://steward.example.com/", Clock: func() time.Time { return now }, RetryDelays: []time.Duration{0, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	return service, repositories
}

func event(kind scheduling.EventKind) scheduling.Event {
	return scheduling.Event{
		Kind:       kind,
		Schedule:   schedule.ScanSchedule{ID: "sch-1", Name: "", ConsecutiveFailures: 3},
		Connection: asset.CloudConnection{ID: "con-1", Name: "production", Provider: asset.ProviderAliCloud},
		ScanTaskID: "scn-1", Detail: "AccessKey disabled", OccurredAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestChannelsKeepTheAddressSecretAndDeliverByEvent(t *testing.T) {
	service, repositories := newService(t, true)
	ctx := context.Background()
	feishu := &received{response: `{"code":0}`}
	feishuServer := httptest.NewServer(http.HandlerFunc(feishu.handler))
	defer feishuServer.Close()
	ding := &received{response: `{"errcode":310000,"errmsg":"sign not match"}`}
	dingServer := httptest.NewServer(http.HandlerFunc(ding.handler))
	defer dingServer.Close()

	created, err := service.Create(ctx, notification.Input{
		Name: "运维群", Type: notification.ChannelFeishu, Language: "zh", URL: feishuServer.URL + "/open-apis/bot/v2/hook/abcd1234",
		SigningSecret: "s3cret", Events: []scheduling.EventKind{scheduling.EventSchedulePaused, scheduling.EventScanFailed},
	}, "carol")
	if err != nil {
		t.Fatal(err)
	}
	if created.Target != feishuServer.URL+"/…1234" || !created.HasSigningSecret {
		t.Fatalf("created = %+v", created)
	}
	if _, err := service.Create(ctx, notification.Input{
		Name: "DingTalk", Type: notification.ChannelDingTalk, Language: "en", URL: dingServer.URL + "/robot/send?access_token=tok9876",
		SigningSecret: "SEC1", Events: []scheduling.EventKind{scheduling.EventSchedulePaused},
	}, "carol"); err != nil {
		t.Fatal(err)
	}
	stored, _ := repositories.Schedules().GetSetting(ctx, "notification_channels")
	if strings.Contains(stored, "abcd1234") || strings.Contains(stored, "s3cret") || strings.Contains(stored, "tok9876") {
		t.Fatalf("secrets stored in plain text: %s", stored)
	}
	views, _ := service.List(ctx)
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), "sealed") || strings.Contains(string(encoded), "abcd1234") {
		t.Fatalf("list exposes secrets: %s", encoded)
	}

	service.Notify(ctx, event(scheduling.EventScanPartial))
	service.Notify(ctx, event(scheduling.EventSchedulePaused))
	service.Wait()

	if len(feishu.bodies) != 1 {
		t.Fatalf("feishu deliveries = %d", len(feishu.bodies))
	}
	body := feishu.bodies[0]
	text := body["content"].(map[string]any)["text"].(string)
	if body["msg_type"] != "text" || body["sign"] == "" || body["timestamp"] != "1790856000" ||
		!strings.Contains(text, "Steward 定时扫描已暂停") || !strings.Contains(text, "计划: 每日全量") || !strings.Contains(text, "连续 3 次") ||
		!strings.Contains(text, "https://steward.example.com/scans/scn-1") {
		t.Fatalf("feishu body = %#v", body)
	}
	if len(ding.queries) == 0 || !strings.Contains(ding.queries[0], "sign=") || !strings.Contains(ding.queries[0], "access_token=tok9876") {
		t.Fatalf("dingtalk queries = %v", ding.queries)
	}
	views, _ = service.List(ctx)
	for _, view := range views {
		switch view.Type {
		case notification.ChannelFeishu:
			if view.LastDelivery == nil || !view.LastDelivery.OK {
				t.Fatalf("feishu delivery = %+v", view.LastDelivery)
			}
		case notification.ChannelDingTalk:
			if view.LastDelivery == nil || view.LastDelivery.OK || !strings.Contains(view.LastDelivery.Error, "sign not match") || len(ding.queries) != 3 {
				t.Fatalf("dingtalk delivery = %+v attempts=%d", view.LastDelivery, len(ding.queries))
			}
		}
	}
}

func TestPrivateNetworksAreBlockedWhenNotAllowed(t *testing.T) {
	service, _ := newService(t, false)
	ctx := context.Background()
	target := &received{response: "ok"}
	server := httptest.NewServer(http.HandlerFunc(target.handler))
	defer server.Close()
	created, err := service.Create(ctx, notification.Input{Name: "internal", Type: notification.ChannelWebhook, URL: server.URL + "/hook", Events: notification.AllEvents}, "carol")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Test(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastDelivery == nil || view.LastDelivery.OK || !strings.Contains(view.LastDelivery.Error, "private or local network") || len(target.bodies) != 0 {
		t.Fatalf("delivery = %+v bodies=%d", view.LastDelivery, len(target.bodies))
	}
}

func TestGenericWebhookCarriesTheStructuredEvent(t *testing.T) {
	service, _ := newService(t, true)
	ctx := context.Background()
	target := &received{response: "ok"}
	server := httptest.NewServer(http.HandlerFunc(target.handler))
	defer server.Close()
	if _, err := service.Create(ctx, notification.Input{Name: "hook", Type: notification.ChannelWebhook, URL: server.URL, Events: notification.AllEvents}, "carol"); err != nil {
		t.Fatal(err)
	}
	service.Notify(ctx, event(scheduling.EventScanFailed))
	service.Wait()
	if len(target.bodies) != 1 {
		t.Fatalf("deliveries = %d", len(target.bodies))
	}
	body := target.bodies[0]
	if body["event"] != "scan_failed" || body["scan_task_id"] != "scn-1" || body["detail"] != "AccessKey disabled" ||
		body["connection"].(map[string]any)["name"] != "production" || !strings.Contains(body["text"].(string), "Steward scheduled scan failed") {
		t.Fatalf("body = %#v", body)
	}
}
